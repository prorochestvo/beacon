package hashedassets

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

	"github.com/seilbekskindirov/beacon/internal/tools/httpenc"
)

// Spec describes a hashable static asset and its optional precompressed sibling.
type Spec struct {
	SourcePath  string // path inside fsys, e.g. "app.wasm"
	ContentType string // MIME type, e.g. "application/wasm"
	GzipPath    string // optional sibling inside fsys, e.g. "app.wasm.gz"; "" if none
}

// hashedAssetEntry is the resolved form of an Spec, ready for serving.
type hashedAssetEntry struct {
	sourcePath  string
	hashedURL   string // "/app.<8hex>.wasm"
	contentType string
	gzipPath    string // "" if no precompressed sibling
}

// Registry maps hashed public URLs back to their source-file metadata.
// Construction reads each declared asset, hashes its raw bytes, and registers an
// in-memory entry. A missing asset is a fatal startup error — call log.Fatalf on
// the returned error.
type Registry struct {
	byURL map[string]hashedAssetEntry
}

// NewRegistry returns a registry built from specs against fsys, or an
// error if any spec's source file cannot be read. The hash is over raw
// (uncompressed) bytes, so a gzip-level change alone does not change the hashed URL.
//
// A declared gzipPath that does not resolve is an error too, not a shrug. serveHashedAsset
// treats a failed open of the sibling as a signal to serve the plain file, which is the
// right behaviour for a spec that declares no sibling and the wrong one for a build that
// was supposed to produce it: the release workflow omitted the gzip step for months and
// production served 4.59 MB where 1.21 MB would do, with nothing anywhere reporting it
// (#79). Startup is where a missing build artifact should be noticed.
func NewRegistry(fsys fs.FS, specs []Spec) (*Registry, error) {
	r := &Registry{byURL: make(map[string]hashedAssetEntry, len(specs))}
	for _, s := range specs {
		b, err := fs.ReadFile(fsys, s.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("hashed asset: read %s: %w", s.SourcePath, err)
		}
		if s.GzipPath != "" {
			if _, statErr := fs.Stat(fsys, s.GzipPath); statErr != nil {
				return nil, fmt.Errorf("hashed asset: %s declares gzip sibling %s: %w", s.SourcePath, s.GzipPath, statErr)
			}
		}
		sum := sha256.Sum256(b)
		prefix := hex.EncodeToString(sum[:4]) // 8 hex characters
		hashedURL := insertHash(s.SourcePath, prefix)
		r.byURL[hashedURL] = hashedAssetEntry{
			sourcePath:  s.SourcePath,
			hashedURL:   hashedURL,
			contentType: s.ContentType,
			gzipPath:    s.GzipPath,
		}
	}
	return r, nil
}

// LogEntries writes one log line listing the active hashes for operator verification.
func (reg *Registry) LogEntries() {
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

// lookup returns the entry for a given hashed URL, or false if not registered.
func (reg *Registry) lookup(url string) (hashedAssetEntry, bool) {
	e, ok := reg.byURL[url]
	return e, ok
}

// HTMLCache holds the boot-time rewritten HTML body for a single entry point.
type HTMLCache struct {
	body     []byte
	etag     string // strong validator over body, quoted per RFC 9110
	modTime  time.Time
	filename string // for http.ServeContent name hint
}

// NewHTMLCache reads filePath from fsys, replaces /app.wasm and /wasm_exec.js with
// their hashed forms from the registry, and returns an immutable cache entry.
// modTime is the stable boot-time timestamp for If-Modified-Since support.
func NewHTMLCache(fsys fs.FS, filePath string, reg *Registry, modTime time.Time) (*HTMLCache, error) {
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

	// The validator is the content, not the clock. modTime is the process start, so
	// without this every restart invalidates the document for every client at once and
	// re-sends it whole — even for a release that did not touch the HTML, which is most
	// of them. Cheap to compute: the rewritten body is already in hand (#83).
	sum := sha256.Sum256(b)

	return &HTMLCache{
		body:     b,
		etag:     `"` + hex.EncodeToString(sum[:]) + `"`,
		modTime:  modTime,
		filename: path.Base(filePath),
	}, nil
}

// serve writes the cached HTML to w. It returns false without writing if the
// request method is neither GET nor HEAD, so the caller can fall through to the
// FileServer. Sets Content-Type: text/html; charset=utf-8 and Cache-Control: no-cache.
//
// The no-cache directive is what makes the "immutable" policy on the hashed asset
// URLs safe: this HTML is the only document that names them, so a client that
// reuses it without revalidating stays pinned to the previous build for the whole
// seven-day asset lifetime. Without an explicit directive the response falls to
// heuristic caching off Last-Modified, whose window grows with process uptime.
// no-cache still permits storing, so ServeContent answers If-Modified-Since with a
// 304 and the body is not retransmitted.
func (c *HTMLCache) serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Set before ServeContent, which then answers If-None-Match itself and prefers it
	// over If-Modified-Since. Last-Modified stays for the 200 — it says when this
	// process started, which is worth having in a header — but it no longer decides
	// revalidation, and writeNotModified strips it from the 304 anyway.
	w.Header().Set("Etag", c.etag)
	http.ServeContent(w, r, c.filename, c.modTime, bytes.NewReader(c.body))
	return true
}

// Handler returns an http.Handler that:
//  1. Serves hashed asset URLs (*.wasm with gz-sibling handoff, *.js plain) directly.
//  2. Serves rewritten index.html from in-memory cache for GET/HEAD on / and /index.html.
//  3. Serves rewritten admin/index.html from in-memory cache for GET/HEAD on /admin/ and /admin/index.html.
//  4. Falls through to fileHandler for everything else (unhashed /app.wasm stale-HTML
//     recovery, every API path that already has its own mux route, etc.).
//
// It does not modify the provided fileHandler or fsys. The gzip-sibling handoff for
// hashed wasm paths uses whichever fs.FS the registry was built from.
func Handler(
	fileHandler http.Handler,
	fsys fs.FS,
	indexCache *HTMLCache,
	adminCache *HTMLCache,
	registry *Registry,
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
