package hashedassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubWasm, stubGz, and stubJS are deterministic bytes used across tests. Subtests
// derive expected hashes inline via sha256.Sum256, so changing these stays consistent.
var (
	stubWasm = []byte("WASM_STUB")
	stubGz   = []byte("GZ_STUB") // distinct bytes simulating a pre-gzipped payload
	stubJS   = []byte("JS_STUB")
)

// stubHash returns the 8-hex SHA-256 prefix for b, matching NewRegistry's
// algorithm exactly.
func stubHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

// minimalMapFS returns a fstest.MapFS covering the two hashable assets, their gzip
// sibling, and both HTML entry points. The HTML contains the URL patterns the
// rewriter targets (/app.wasm, /wasm_exec.js) plus a prose line that must NOT be
// rewritten.
func minimalMapFS() fstest.MapFS {
	indexHTML := []byte(`<html>
<!-- baked into app.wasm at build time -->
<script src="/wasm_exec.js"></script>
<script>fetch('/app.wasm')</script>
</html>`)
	adminHTML := []byte(`<html>
<script src="/wasm_exec.js"></script>
<script>fetch('/app.wasm')</script>
</html>`)
	return fstest.MapFS{
		"app.wasm":         {Data: stubWasm},
		"app.wasm.gz":      {Data: stubGz},
		"wasm_exec.js":     {Data: stubJS},
		"index.html":       {Data: indexHTML},
		"admin/index.html": {Data: adminHTML},
	}
}

// defaultSpecs returns the same asset specs used by production main.go.
func defaultSpecs() []Spec {
	return []Spec{
		{SourcePath: "app.wasm", ContentType: "application/wasm", GzipPath: "app.wasm.gz"},
		{SourcePath: "wasm_exec.js", ContentType: "text/javascript; charset=utf-8"},
	}
}

func TestNewHashedAssetRegistry(t *testing.T) {
	t.Parallel()

	t.Run("happy path builds registry with correct URLs", func(t *testing.T) {
		t.Parallel()
		mapFS := minimalMapFS()
		reg, err := NewRegistry(mapFS, defaultSpecs())
		require.NoError(t, err)
		require.NotNil(t, reg)

		wasmHash := stubHash(stubWasm)
		jsHash := stubHash(stubJS)

		expectedWasmURL := fmt.Sprintf("/app.%s.wasm", wasmHash)
		expectedJsURL := fmt.Sprintf("/wasm_exec.%s.js", jsHash)

		e, ok := reg.lookup(expectedWasmURL)
		require.True(t, ok, "expected hashed wasm URL to be registered: %s", expectedWasmURL)
		assert.Equal(t, "app.wasm", e.sourcePath)
		assert.Equal(t, "application/wasm", e.contentType)
		assert.Equal(t, "app.wasm.gz", e.gzipPath)

		e, ok = reg.lookup(expectedJsURL)
		require.True(t, ok, "expected hashed js URL to be registered: %s", expectedJsURL)
		assert.Equal(t, "wasm_exec.js", e.sourcePath)
		assert.Equal(t, "text/javascript; charset=utf-8", e.contentType)
		assert.Equal(t, "", e.gzipPath)
	})

	t.Run("missing asset returns error naming the path", func(t *testing.T) {
		t.Parallel()
		emptyFS := fstest.MapFS{}
		specs := []Spec{
			{SourcePath: "app.wasm", ContentType: "application/wasm"},
		}
		reg, err := NewRegistry(emptyFS, specs)
		require.Error(t, err)
		require.Nil(t, reg)
		assert.Contains(t, err.Error(), "app.wasm")
	})

	// The regression guard for #79: the release workflow built app.wasm without gzipping
	// it, the sibling was absent from the embedded FS, and serveHashedAsset's fall-through
	// turned that into 4.59 MB served raw instead of 1.21 MB — silently, for months.
	t.Run("declared gzip sibling that is absent is a startup error", func(t *testing.T) {
		t.Parallel()
		noGz := fstest.MapFS{
			"app.wasm": {Data: stubWasm},
		}
		specs := []Spec{
			{SourcePath: "app.wasm", ContentType: "application/wasm", GzipPath: "app.wasm.gz"},
		}
		reg, err := NewRegistry(noGz, specs)
		require.Error(t, err, "a build that skipped the gzip step must not start")
		require.Nil(t, reg)
		assert.Contains(t, err.Error(), "app.wasm.gz")
	})

	t.Run("no gzip sibling declared is not an error", func(t *testing.T) {
		t.Parallel()
		jsOnly := fstest.MapFS{
			"wasm_exec.js": {Data: stubJS},
		}
		specs := []Spec{
			{SourcePath: "wasm_exec.js", ContentType: "text/javascript; charset=utf-8"},
		}
		reg, err := NewRegistry(jsOnly, specs)
		require.NoError(t, err, "an empty gzipPath declares no sibling and must stay valid")
		require.NotNil(t, reg)
	})

	t.Run("same bytes produce same hash on repeated calls", func(t *testing.T) {
		t.Parallel()
		mapFS := minimalMapFS()
		specs := defaultSpecs()
		reg1, err := NewRegistry(mapFS, specs)
		require.NoError(t, err)
		reg2, err := NewRegistry(mapFS, specs)
		require.NoError(t, err)

		// Both registries must have identical URL maps.
		for url, e1 := range reg1.byURL {
			e2, ok := reg2.byURL[url]
			require.True(t, ok, "URL %s missing from second registry", url)
			assert.Equal(t, e1.hashedURL, e2.hashedURL)
		}
	})

	t.Run("different bytes produce different hash", func(t *testing.T) {
		t.Parallel()
		mapFS1 := fstest.MapFS{
			"app.wasm": {Data: []byte("VERSION_ONE")},
		}
		mapFS2 := fstest.MapFS{
			"app.wasm": {Data: []byte("VERSION_TWO")},
		}
		specs := []Spec{{SourcePath: "app.wasm", ContentType: "application/wasm"}}

		reg1, err := NewRegistry(mapFS1, specs)
		require.NoError(t, err)
		reg2, err := NewRegistry(mapFS2, specs)
		require.NoError(t, err)

		var url1, url2 string
		for u := range reg1.byURL {
			url1 = u
		}
		for u := range reg2.byURL {
			url2 = u
		}
		assert.NotEqual(t, url1, url2, "different content must produce different hashed URLs")
	})

	t.Run("URL shape is /basename.8hex.ext", func(t *testing.T) {
		t.Parallel()
		mapFS := minimalMapFS()
		reg, err := NewRegistry(mapFS, defaultSpecs())
		require.NoError(t, err)

		for url := range reg.byURL {
			// URL must start with /
			assert.True(t, strings.HasPrefix(url, "/"), "URL must start with /: %s", url)
			// Extension must be .wasm or .js
			assert.True(t, strings.HasSuffix(url, ".wasm") || strings.HasSuffix(url, ".js"),
				"URL must end with .wasm or .js: %s", url)
			// Hash segment must be 8 lowercase hex chars
			base := strings.TrimPrefix(url, "/")
			parts := strings.Split(base, ".")
			require.GreaterOrEqual(t, len(parts), 3, "URL must have at least 3 dot-separated segments: %s", url)
			hashPart := parts[len(parts)-2]
			assert.Len(t, hashPart, 8, "hash segment must be 8 chars: %s", hashPart)
			assert.Regexp(t, `^[a-f0-9]+$`, hashPart, "hash segment must be hex: %s", hashPart)
		}
	})

	t.Run("hash is over raw bytes, not gz sibling", func(t *testing.T) {
		t.Parallel()
		// Same raw wasm, different gz content: the hashed URL must match in both,
		// proving the gz bytes are not hashed.
		rawBytes := []byte("SAME_WASM_BYTES")
		mapFS1 := fstest.MapFS{
			"app.wasm":    {Data: rawBytes},
			"app.wasm.gz": {Data: []byte("GZ_V1")},
		}
		mapFS2 := fstest.MapFS{
			"app.wasm":    {Data: rawBytes},
			"app.wasm.gz": {Data: []byte("GZ_V2_DIFFERENT")},
		}
		specs := []Spec{{SourcePath: "app.wasm", ContentType: "application/wasm", GzipPath: "app.wasm.gz"}}

		reg1, err := NewRegistry(mapFS1, specs)
		require.NoError(t, err)
		reg2, err := NewRegistry(mapFS2, specs)
		require.NoError(t, err)

		var url1, url2 string
		for u := range reg1.byURL {
			url1 = u
		}
		for u := range reg2.byURL {
			url2 = u
		}
		assert.Equal(t, url1, url2, "gz-only change must not change hashed URL")
	})
}

func TestHashedAssetRegistry_Serve(t *testing.T) {
	t.Parallel()

	mapFS := minimalMapFS()
	reg, err := NewRegistry(mapFS, defaultSpecs())
	require.NoError(t, err)

	wasmHash := stubHash(stubWasm)
	jsHash := stubHash(stubJS)
	wasmURL := fmt.Sprintf("/app.%s.wasm", wasmHash)
	jsURL := fmt.Sprintf("/wasm_exec.%s.js", jsHash)

	t.Run("hashed wasm without gzip header returns plain bytes with application/wasm", func(t *testing.T) {
		t.Parallel()
		entry, ok := reg.lookup(wasmURL)
		require.True(t, ok)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, wasmURL, nil)
		serveHashedAsset(w, r, mapFS, entry)

		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Equal(t, "application/wasm", result.Header.Get("Content-Type"))
		assert.Empty(t, result.Header.Get("Content-Encoding"), "plain response must not set Content-Encoding")

		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubWasm), "body must be byte-identical to source file")
	})

	t.Run("hashed wasm with Accept-Encoding: gzip returns gz sibling with correct headers", func(t *testing.T) {
		t.Parallel()
		entry, ok := reg.lookup(wasmURL)
		require.True(t, ok)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, wasmURL, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		serveHashedAsset(w, r, mapFS, entry)

		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Equal(t, "application/wasm", result.Header.Get("Content-Type"))
		assert.Equal(t, "gzip", result.Header.Get("Content-Encoding"))
		assert.Equal(t, "Accept-Encoding", result.Header.Get("Vary"))

		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubGz), "gz response body must match gz sibling bytes")
	})

	t.Run("hashed wasm falls back to plain when gz sibling is absent", func(t *testing.T) {
		t.Parallel()
		noGzFS := fstest.MapFS{
			"app.wasm": {Data: stubWasm},
			// no app.wasm.gz
		}
		// The entry is built directly rather than through NewRegistry, which
		// now refuses a declared sibling it cannot stat (#79). This subtest is about
		// serveHashedAsset's runtime fall-through, which stays: it is the right answer
		// for a spec that declares no sibling, and a last resort if one vanishes under a
		// running process.
		url := fmt.Sprintf("/app.%s.wasm", wasmHash)
		entry := hashedAssetEntry{
			sourcePath:  "app.wasm",
			hashedURL:   url,
			contentType: "application/wasm",
			gzipPath:    "app.wasm.gz",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, url, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		serveHashedAsset(w, r, noGzFS, entry)

		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Empty(t, result.Header.Get("Content-Encoding"), "fallback must not set Content-Encoding")

		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubWasm))
	})

	t.Run("hashed js URL returns correct content type and raw bytes", func(t *testing.T) {
		t.Parallel()
		entry, ok := reg.lookup(jsURL)
		require.True(t, ok)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, jsURL, nil)
		serveHashedAsset(w, r, mapFS, entry)

		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Equal(t, "text/javascript; charset=utf-8", result.Header.Get("Content-Type"))
		assert.Empty(t, result.Header.Get("Content-Encoding"))

		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubJS))
	})
}

func TestHTMLCacheRewrite(t *testing.T) {
	t.Parallel()

	mapFS := minimalMapFS()
	reg, err := NewRegistry(mapFS, defaultSpecs())
	require.NoError(t, err)

	wasmHash := stubHash(stubWasm)
	jsHash := stubHash(stubJS)
	hashedWasmURL := fmt.Sprintf("/app.%s.wasm", wasmHash)
	hashedJsURL := fmt.Sprintf("/wasm_exec.%s.js", jsHash)
	bootTime := time.Now()

	t.Run("index.html wasm and js URLs are rewritten", func(t *testing.T) {
		t.Parallel()
		cache, cacheErr := NewHTMLCache(mapFS, "index.html", reg, bootTime)
		require.NoError(t, cacheErr)
		body := cache.body

		// Hashed forms must be present.
		assert.True(t, bytes.Contains(body, []byte(hashedWasmURL)),
			"rewritten HTML must contain hashed wasm URL %s", hashedWasmURL)
		assert.True(t, bytes.Contains(body, []byte(hashedJsURL)),
			"rewritten HTML must contain hashed js URL %s", hashedJsURL)

		// Unhashed URL forms in attribute/fetch positions must be gone.
		assert.False(t, bytes.Contains(body, []byte("'/app.wasm'")),
			"fetch('/app.wasm') must not remain in rewritten HTML")
		assert.False(t, bytes.Contains(body, []byte(`src="/wasm_exec.js"`)),
			`src="/wasm_exec.js" must not remain in rewritten HTML`)

		// Prose "app.wasm at build time" must survive — only the URL form
		// (/app.wasm) is replaced, not the bare token.
		assert.True(t, bytes.Contains(body, []byte("app.wasm at build time")),
			"prose reference 'app.wasm at build time' must not be rewritten")
	})

	t.Run("admin/index.html wasm and js URLs are rewritten", func(t *testing.T) {
		t.Parallel()
		cache, cacheErr := NewHTMLCache(mapFS, "admin/index.html", reg, bootTime)
		require.NoError(t, cacheErr)
		body := cache.body

		assert.True(t, bytes.Contains(body, []byte(hashedWasmURL)))
		assert.True(t, bytes.Contains(body, []byte(hashedJsURL)))

		assert.False(t, bytes.Contains(body, []byte("'/app.wasm'")))
		assert.False(t, bytes.Contains(body, []byte(`src="/wasm_exec.js"`)))
	})

	t.Run("restarting with different wasm bytes produces new hashed URL in HTML", func(t *testing.T) {
		t.Parallel()
		mapFS1 := fstest.MapFS{
			"app.wasm":         {Data: []byte("WASM_V1")},
			"app.wasm.gz":      {Data: stubGz},
			"wasm_exec.js":     {Data: stubJS},
			"index.html":       {Data: []byte(`<script src="/wasm_exec.js"></script><script>fetch('/app.wasm')</script>`)},
			"admin/index.html": {Data: []byte(`<script>fetch('/app.wasm')</script>`)},
		}
		mapFS2 := fstest.MapFS{
			"app.wasm":         {Data: []byte("WASM_V2_DIFFERENT")},
			"app.wasm.gz":      {Data: stubGz},
			"wasm_exec.js":     {Data: stubJS},
			"index.html":       {Data: []byte(`<script src="/wasm_exec.js"></script><script>fetch('/app.wasm')</script>`)},
			"admin/index.html": {Data: []byte(`<script>fetch('/app.wasm')</script>`)},
		}

		reg1, err1 := NewRegistry(mapFS1, defaultSpecs())
		require.NoError(t, err1)
		reg2, err2 := NewRegistry(mapFS2, defaultSpecs())
		require.NoError(t, err2)

		c1, err1 := NewHTMLCache(mapFS1, "index.html", reg1, bootTime)
		require.NoError(t, err1)
		c2, err2 := NewHTMLCache(mapFS2, "index.html", reg2, bootTime)
		require.NoError(t, err2)

		assert.False(t, bytes.Equal(c1.body, c2.body),
			"HTML from different wasm builds must differ")
	})

	t.Run("missing HTML file returns error", func(t *testing.T) {
		t.Parallel()
		emptyFS := fstest.MapFS{
			"app.wasm":     {Data: stubWasm},
			"app.wasm.gz":  {Data: stubGz},
			"wasm_exec.js": {Data: stubJS},
		}
		reg2, buildErr := NewRegistry(emptyFS, defaultSpecs())
		require.NoError(t, buildErr)

		_, cacheErr := NewHTMLCache(emptyFS, "index.html", reg2, bootTime)
		require.Error(t, cacheErr)
		assert.Contains(t, cacheErr.Error(), "index.html")
	})
}

func TestStaticHandler(t *testing.T) {
	t.Parallel()

	mapFS := minimalMapFS()
	reg, err := NewRegistry(mapFS, defaultSpecs())
	require.NoError(t, err)

	bootTime := time.Now()
	indexCache, err := NewHTMLCache(mapFS, "index.html", reg, bootTime)
	require.NoError(t, err)
	adminCache, err := NewHTMLCache(mapFS, "admin/index.html", reg, bootTime)
	require.NoError(t, err)

	wasmHash := stubHash(stubWasm)
	jsHash := stubHash(stubJS)
	hashedWasmURL := fmt.Sprintf("/app.%s.wasm", wasmHash)
	hashedJsURL := fmt.Sprintf("/wasm_exec.%s.js", jsHash)

	fileHandler := http.FileServer(http.FS(mapFS))
	handler := Handler(fileHandler, mapFS, indexCache, adminCache, reg)

	// helper to issue a GET and return the recorder.
	get := func(t *testing.T, url string, headers ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, url, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	t.Run("GET / returns rewritten HTML", func(t *testing.T) {
		t.Parallel()
		w := get(t, "/")
		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Contains(t, result.Header.Get("Content-Type"), "text/html")
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Contains(body, []byte(hashedWasmURL)))
		assert.True(t, bytes.Contains(body, []byte(hashedJsURL)))
	})

	t.Run("GET /index.html returns same rewritten HTML as GET /", func(t *testing.T) {
		t.Parallel()
		wRoot := get(t, "/")
		wIndex := get(t, "/index.html")

		rootBody, readErr := io.ReadAll(wRoot.Result().Body)
		require.NoError(t, readErr)
		indexBody, readErr := io.ReadAll(wIndex.Result().Body)
		require.NoError(t, readErr)

		assert.True(t, bytes.Equal(rootBody, indexBody),
			"/ and /index.html must return byte-identical bodies")
	})

	t.Run("GET /admin/ returns rewritten admin HTML", func(t *testing.T) {
		t.Parallel()
		w := get(t, "/admin/")
		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Contains(body, []byte(hashedWasmURL)))
	})

	t.Run("GET /admin/index.html returns same body as GET /admin/", func(t *testing.T) {
		t.Parallel()
		wAdmin := get(t, "/admin/")
		wIndex := get(t, "/admin/index.html")

		adminBody, readErr := io.ReadAll(wAdmin.Result().Body)
		require.NoError(t, readErr)
		indexBody, readErr := io.ReadAll(wIndex.Result().Body)
		require.NoError(t, readErr)

		assert.True(t, bytes.Equal(adminBody, indexBody))
	})

	t.Run("HTML entry points are served no-cache", func(t *testing.T) {
		t.Parallel()
		for _, url := range []string{"/", "/index.html", "/admin/", "/admin/index.html"} {
			w := get(t, url)
			assert.Equal(t, "no-cache", w.Result().Header.Get("Cache-Control"),
				"%s must be revalidated: it is the only document naming the immutable hashed assets", url)
		}
	})

	t.Run("HTML revalidation returns 304 and keeps the directive", func(t *testing.T) {
		t.Parallel()
		w := get(t, "/", "If-Modified-Since", bootTime.UTC().Format(http.TimeFormat))
		result := w.Result()
		assert.Equal(t, http.StatusNotModified, result.StatusCode,
			"no-cache must cost a revalidation round trip, not a body retransmit")
		assert.Equal(t, "no-cache", result.Header.Get("Cache-Control"))
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.Empty(t, body)
	})

	t.Run("hashed assets carry no origin Cache-Control", func(t *testing.T) {
		t.Parallel()
		for _, url := range []string{hashedWasmURL, hashedJsURL} {
			w := get(t, url)
			assert.Empty(t, w.Result().Header.Get("Cache-Control"),
				"%s: nginx is the sole source of truth for the immutable policy", url)
		}
	})

	t.Run("GET hashed wasm without gzip header returns plain bytes", func(t *testing.T) {
		t.Parallel()
		w := get(t, hashedWasmURL)
		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Empty(t, result.Header.Get("Content-Encoding"))
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubWasm))
	})

	t.Run("GET hashed wasm with Accept-Encoding: gzip returns gz bytes", func(t *testing.T) {
		t.Parallel()
		w := get(t, hashedWasmURL, "Accept-Encoding", "gzip")
		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Equal(t, "gzip", result.Header.Get("Content-Encoding"))
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubGz))
	})

	t.Run("GET hashed js URL returns js content type and raw bytes", func(t *testing.T) {
		t.Parallel()
		w := get(t, hashedJsURL)
		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		assert.Equal(t, "text/javascript; charset=utf-8", result.Header.Get("Content-Type"))
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubJS))
	})

	t.Run("GET /app.wasm (unhashed) falls through to FileServer", func(t *testing.T) {
		t.Parallel()
		// The FileServer will serve the raw file from the FS — stale-HTML recovery path.
		w := get(t, "/app.wasm")
		result := w.Result()
		// 200 with file bytes; not intercepted by the handler.
		assert.Equal(t, http.StatusOK, result.StatusCode)
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubWasm), "unhashed /app.wasm must return raw wasm bytes")
		// The body must NOT be the rewritten HTML.
		assert.False(t, bytes.Contains(body, []byte("<!DOCTYPE")))
	})

	t.Run("GET /wasm_exec.js (unhashed) falls through to FileServer", func(t *testing.T) {
		t.Parallel()
		w := get(t, "/wasm_exec.js")
		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.True(t, bytes.Equal(body, stubJS))
	})

	t.Run("POST / is not served from HTML cache", func(t *testing.T) {
		t.Parallel()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		result := w.Result()
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		// The rewritten HTML must not appear in the response — method guard is working.
		assert.False(t, bytes.Contains(body, []byte(hashedWasmURL)),
			"POST / must not return rewritten HTML")
	})

	t.Run("cached body pointer does not change across requests", func(t *testing.T) {
		// The cache is built once at boot; serving the same endpoint twice must
		// reuse the same byte slice without copying.
		snapshot := indexCache.body
		_ = get(t, "/")
		_ = get(t, "/")
		assert.True(t, &snapshot[0] == &indexCache.body[0],
			"body slice must be the same allocation across requests")
	})

	t.Run("unknown hashed-style URL does not match registry and falls through", func(t *testing.T) {
		// "/app.deadbeef.wasm.map" must not match — exact key lookup, not prefix.
		t.Parallel()
		w := get(t, "/app.deadbeef.wasm.map")
		result := w.Result()
		// FileServer returns 404 since this path does not exist in the FS.
		assert.Equal(t, http.StatusNotFound, result.StatusCode)
	})

	t.Run("unknown path is delegated to FileServer and returns 404", func(t *testing.T) {
		t.Parallel()
		w := get(t, "/nonexistent.txt")
		result := w.Result()
		assert.Equal(t, http.StatusNotFound, result.StatusCode)
	})
}

// TestInsertHash guards the hash-URL construction helper in isolation.
func TestInsertHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sourcePath string
		hash       string
		want       string
	}{
		{"app.wasm", "deadbeef", "/app.deadbeef.wasm"},
		{"wasm_exec.js", "cafebabe", "/wasm_exec.cafebabe.js"},
	}
	for _, tc := range tests {
		t.Run(tc.sourcePath, func(t *testing.T) {
			t.Parallel()
			got := insertHash(tc.sourcePath, tc.hash)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestNewHTMLCache_FSOverswitching ensures the cache reads from whichever fs.FS it
// was given, not the registry's — mirroring the --static-dir vs embedded FS
// branching in main.go.
func TestNewHTMLCache_FSOverswitching(t *testing.T) {
	t.Parallel()

	fsA := fstest.MapFS{
		"app.wasm":     {Data: stubWasm},
		"wasm_exec.js": {Data: stubJS},
		"index.html":   {Data: []byte(`<script>fetch('/app.wasm')</script>`)},
	}
	reg, err := NewRegistry(fsA, []Spec{
		{SourcePath: "app.wasm", ContentType: "application/wasm"},
		{SourcePath: "wasm_exec.js", ContentType: "text/javascript; charset=utf-8"},
	})
	require.NoError(t, err)

	// fsB has a different index.html; NewHTMLCache must read from fsB, not fsA.
	fsB := fstest.MapFS{
		"index.html": {Data: []byte(`FSONLY_MARKER fetch('/app.wasm')`)},
	}
	cache, err := NewHTMLCache(fsB, "index.html", reg, time.Now())
	require.NoError(t, err)

	assert.True(t, bytes.Contains(cache.body, []byte("FSONLY_MARKER")),
		"HTML cache must read from the FS it was given, not a cached copy")
}

// TestHTMLCache_Serve exercises the method guard inside HTMLCache.serve.
func TestHTMLCache_Serve(t *testing.T) {
	t.Parallel()

	c := &HTMLCache{
		body:     []byte("<html>hello</html>"),
		modTime:  time.Now(),
		filename: "index.html",
	}

	t.Run("GET returns true and writes body", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		handled := c.serve(w, r)
		assert.True(t, handled)
		result := w.Result()
		assert.Equal(t, "no-cache", result.Header.Get("Cache-Control"))
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.Equal(t, c.body, body)
	})

	t.Run("HEAD returns true", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodHead, "/", nil)
		handled := c.serve(w, r)
		assert.True(t, handled)
		assert.Equal(t, "no-cache", w.Result().Header.Get("Cache-Control"))
	})

	t.Run("POST returns false", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		handled := c.serve(w, r)
		assert.False(t, handled)
		assert.Equal(t, http.StatusOK, w.Code, "serve must not write to w when returning false")
		// Pins the header set below the method guard: hoisting it above would keep the
		// two assertions above green while leaking the directive onto every response
		// that falls through to the FileServer.
		assert.Empty(t, w.Header().Get("Cache-Control"))
	})

	t.Run("DELETE returns false", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/", nil)
		handled := c.serve(w, r)
		assert.False(t, handled)
	})
}

// TestHTMLCacheETagSurvivesRestart is the regression guard for #83. Cache-Control:
// no-cache means every page open costs a conditional request, so the validator's quality
// decides whether that request is a 304 or a full 36 KB transfer. The old validator was
// the process start time, which moves on every restart regardless of content — so a
// server-only release re-sent the document to every client for nothing.
func TestHTMLCacheETagSurvivesRestart(t *testing.T) {
	t.Parallel()

	mapFS := minimalMapFS()
	reg, err := NewRegistry(mapFS, defaultSpecs())
	require.NoError(t, err)

	firstBoot := time.Now().Add(-time.Hour)
	before, err := NewHTMLCache(mapFS, "index.html", reg, firstBoot)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	before.serve(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	etag := w.Result().Header.Get("Etag")
	require.NotEmpty(t, etag, "the 200 must carry a validator to revalidate against")

	// The restart: same bytes, a modTime an hour later. Under Last-Modified alone this is
	// exactly the case that produced a 200.
	after, err := NewHTMLCache(mapFS, "index.html", reg, time.Now())
	require.NoError(t, err)

	afterRec := httptest.NewRecorder()
	after.serve(afterRec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	require.Equal(t, etag, afterRec.Result().Header.Get("Etag"),
		"the validator must be the content, so a restart alone cannot change it")

	t.Run("unchanged content revalidates to 304 across the restart", func(t *testing.T) {
		t.Parallel()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		r.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		require.True(t, after.serve(rec, r))

		result := rec.Result()
		assert.Equal(t, http.StatusNotModified, result.StatusCode,
			"a restart that did not change the HTML must not re-send it")
		assert.Equal(t, "no-cache", result.Header.Get("Cache-Control"),
			"the directive must accompany the 304, or the next open skips revalidation")
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.Empty(t, body)
	})

	t.Run("the stale Last-Modified alone would have re-sent it", func(t *testing.T) {
		t.Parallel()
		// Same request without the ETag, carrying the pre-restart timestamp: 200, because
		// modTime moved. This is the behaviour the ETag replaces, pinned so the reason
		// for the ETag cannot quietly stop being true.
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		r.Header.Set("If-Modified-Since", firstBoot.UTC().Format(http.TimeFormat))
		rec := httptest.NewRecorder()
		require.True(t, after.serve(rec, r))
		assert.Equal(t, http.StatusOK, rec.Result().StatusCode)
	})

	t.Run("changed content gets a different validator and a 200", func(t *testing.T) {
		t.Parallel()
		changed := minimalMapFS()
		changed["index.html"] = &fstest.MapFile{Data: []byte(`<html><script src="/wasm_exec.js"></script>CHANGED</html>`)}
		changedReg, regErr := NewRegistry(changed, defaultSpecs())
		require.NoError(t, regErr)
		changedCache, cacheErr := NewHTMLCache(changed, "index.html", changedReg, firstBoot)
		require.NoError(t, cacheErr)

		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		r.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		require.True(t, changedCache.serve(rec, r))

		result := rec.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode,
			"a client holding the old validator must receive the new document")
		assert.NotEqual(t, etag, result.Header.Get("Etag"))
		body, readErr := io.ReadAll(result.Body)
		require.NoError(t, readErr)
		assert.Contains(t, string(body), "CHANGED")
	})
}

// Compile-time check: fstest.MapFS satisfies fs.FS.
var _ fs.FS = fstest.MapFS{}
