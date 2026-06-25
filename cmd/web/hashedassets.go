package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal/tools/httpenc"
)

// assetSpec describes a hashable static asset and its optional precompressed sibling.
type assetSpec struct {
	sourcePath  string // path inside fsys, e.g. "app.wasm"
	contentType string // MIME type, e.g. "application/wasm"
	gzipPath    string // optional sibling inside fsys, e.g. "app.wasm.gz"; "" if none
}

// hashedAssetEntry is the resolved form of an assetSpec, ready for serving.
type hashedAssetEntry struct {
	sourcePath  string
	hashedURL   string // "/app.<8hex>.wasm"
	contentType string
	gzipPath    string // "" if no precompressed sibling
}

// hashedAssetRegistry maps hashed public URLs back to their source-file metadata.
// Construction reads each declared asset, hashes its raw bytes, and registers an
// in-memory entry. A missing asset is a fatal startup error — call log.Fatalf on
// the returned error.
type hashedAssetRegistry struct {
	byURL map[string]hashedAssetEntry
}

// newHashedAssetRegistry returns a registry built from specs against fsys, or an
// error if any spec's source file cannot be read. The hash is over raw
// (uncompressed) bytes, so a gzip-level change alone does not change the hashed URL.
func newHashedAssetRegistry(fsys fs.FS, specs []assetSpec) (*hashedAssetRegistry, error) {
	r := &hashedAssetRegistry{byURL: make(map[string]hashedAssetEntry, len(specs))}
	for _, s := range specs {
		b, err := fs.ReadFile(fsys, s.sourcePath)
		if err != nil {
			return nil, fmt.Errorf("hashed asset: read %s: %w", s.sourcePath, err)
		}
		sum := sha256.Sum256(b)
		prefix := hex.EncodeToString(sum[:4]) // 8 hex characters
		hashedURL := insertHash(s.sourcePath, prefix)
		r.byURL[hashedURL] = hashedAssetEntry{
			sourcePath:  s.sourcePath,
			hashedURL:   hashedURL,
			contentType: s.contentType,
			gzipPath:    s.gzipPath,
		}
	}
	return r, nil
}

// lookup returns the entry for a given hashed URL, or false if not registered.
func (reg *hashedAssetRegistry) lookup(url string) (hashedAssetEntry, bool) {
	e, ok := reg.byURL[url]
	return e, ok
}

// logEntries writes one log line listing the active hashes for operator verification.
func (reg *hashedAssetRegistry) logEntries() {
	var parts []string
	// Key by the source filename's base stem so the log line is deterministic
	// despite Go map-iteration randomness.
	stems := make(map[string]hashedAssetEntry, len(reg.byURL))
	for _, e := range reg.byURL {
		stem := strings.TrimSuffix(path.Base(e.sourcePath), path.Ext(e.sourcePath))
		stems[stem] = e
	}
	for _, stem := range []string{"app", "wasm_exec"} {
		if e, ok := stems[stem]; ok {
			// "/app.<hash>.wasm" → "<hash>".
			base := strings.TrimPrefix(e.hashedURL, "/")
			parts = append(parts, stem+"="+extractHashFromBase(base))
		}
	}
	log.Printf("hashed assets: %s", strings.Join(parts, " "))
}

// insertHash derives the public hashed URL from a source path.
// "app.wasm" → "/app.<hash>.wasm"; "wasm_exec.js" → "/wasm_exec.<hash>.js".
func insertHash(sourcePath, hashPrefix string) string {
	ext := path.Ext(sourcePath)
	base := strings.TrimSuffix(path.Base(sourcePath), ext)
	return "/" + base + "." + hashPrefix + ext
}

// extractHashFromBase extracts the 8-hex segment from a base filename.
// "app.deadbeef.wasm" → "deadbeef".
func extractHashFromBase(base string) string {
	parts := strings.Split(base, ".")
	if len(parts) >= 3 {
		return parts[len(parts)-2]
	}
	return base
}

// htmlCache holds the boot-time rewritten HTML body for a single entry point.
type htmlCache struct {
	body     []byte
	modTime  time.Time
	filename string // for http.ServeContent name hint
}

// serve writes the cached HTML to w. It returns false without writing if the
// request method is neither GET nor HEAD, so the caller can fall through to the
// FileServer. Sets Content-Type: text/html; charset=utf-8.
func (c *htmlCache) serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, c.filename, c.modTime, bytes.NewReader(c.body))
	return true
}

// newHTMLCache reads filePath from fsys, replaces /app.wasm and /wasm_exec.js with
// their hashed forms from the registry, and returns an immutable cache entry.
// modTime is the stable boot-time timestamp for If-Modified-Since support.
func newHTMLCache(fsys fs.FS, filePath string, reg *hashedAssetRegistry, modTime time.Time) (*htmlCache, error) {
	b, err := fs.ReadFile(fsys, filePath)
	if err != nil {
		return nil, fmt.Errorf("html cache: read %s: %w", filePath, err)
	}

	// Find the hashed URLs for the two known assets.
	var appHashedURL, jsHashedURL string
	for _, e := range reg.byURL {
		switch e.sourcePath {
		case "app.wasm":
			appHashedURL = e.hashedURL
		case "wasm_exec.js":
			jsHashedURL = e.hashedURL
		}
	}

	// Replace only the URL-form occurrences (leading slash) to avoid touching
	// prose like "baked into app.wasm at build time" in HTML comments.
	if appHashedURL != "" {
		b = bytes.ReplaceAll(b, []byte("/app.wasm"), []byte(appHashedURL))
	}
	if jsHashedURL != "" {
		b = bytes.ReplaceAll(b, []byte("/wasm_exec.js"), []byte(jsHashedURL))
	}

	return &htmlCache{
		body:     b,
		modTime:  modTime,
		filename: path.Base(filePath),
	}, nil
}

// staticHandler returns an http.Handler that:
//  1. Serves hashed asset URLs (*.wasm with gz-sibling handoff, *.js plain) directly.
//  2. Serves rewritten index.html from in-memory cache for GET/HEAD on / and /index.html.
//  3. Serves rewritten admin/index.html from in-memory cache for GET/HEAD on /admin/ and /admin/index.html.
//  4. Falls through to fileHandler for everything else (unhashed /app.wasm stale-HTML
//     recovery, every API path that already has its own mux route, etc.).
//
// It does not modify the provided fileHandler or fsys. The gzip-sibling handoff for
// hashed wasm paths uses whichever fs.FS the registry was built from.
func staticHandler(
	fileHandler http.Handler,
	fsys fs.FS,
	indexCache *htmlCache,
	adminCache *htmlCache,
	registry *hashedAssetRegistry,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Exact hashed-URL dispatch (map lookup, not prefix matching).
		if entry, ok := registry.lookup(r.URL.Path); ok {
			serveHashedAsset(w, r, fsys, entry)
			return
		}

		// 2 & 3. HTML cache dispatch for the two SPA roots.
		switch r.URL.Path {
		case "/", "/index.html":
			if indexCache.serve(w, r) {
				return
			}
		case "/admin/", "/admin/index.html":
			if adminCache.serve(w, r) {
				return
			}
		}

		// 4. Everything else: the mux's API routes already shadow this catch-all,
		// so what arrives here is unhashed static files.
		fileHandler.ServeHTTP(w, r)
	})
}

// serveHashedAsset writes the content for a hashed asset entry. For wasm it serves
// the precompressed sibling when the client accepts gzip, else the plain file; for
// JS and other types it serves raw bytes.
// The origin sets no Cache-Control; nginx adds the
// "public, max-age=604800, immutable" header at the edge via its regex location in
// common_settings.conf.
func serveHashedAsset(w http.ResponseWriter, r *http.Request, fsys fs.FS, entry hashedAssetEntry) {
	// Wasm: gzip-sibling handoff when the client accepts it.
	if entry.gzipPath != "" && httpenc.AcceptsGzip(r.Header.Get("Accept-Encoding")) {
		f, err := fsys.Open(entry.gzipPath)
		if err == nil {
			defer func() { _ = f.Close() }()
			fi, statErr := f.Stat()
			if statErr != nil {
				log.Printf("hashed asset: stat %s: %v", entry.gzipPath, statErr)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			rs, ok := f.(io.ReadSeeker)
			if !ok {
				log.Printf("hashed asset: %s does not implement io.ReadSeeker", entry.gzipPath)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", entry.contentType)
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
			return
		}
		// gz sibling not found — fall through to plain serve below.
	}

	// Plain serve (wasm without gz available, or JS).
	f, err := fsys.Open(entry.sourcePath)
	if err != nil {
		log.Printf("hashed asset: open %s: %v", entry.sourcePath, err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		log.Printf("hashed asset: stat %s: %v", entry.sourcePath, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		log.Printf("hashed asset: %s does not implement io.ReadSeeker", entry.sourcePath)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", entry.contentType)
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
}
