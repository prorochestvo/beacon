package rulegen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ Fetcher = (*ChromedpFetcher)(nil)

// findChromiumOrSkip looks for a Chromium/Chrome binary in the locations
// chromedp checks, plus CHROMIUM_PATH from the environment used by cmd/rulegen.
// The test is skipped cleanly if no binary is found — CI runners without
// Chromium must not fail, only skip.
func findChromiumOrSkip(t *testing.T) string {
	t.Helper()

	candidates := []string{
		"chromium",
		"chromium-browser",
		"google-chrome",
		"chrome",
	}

	for _, name := range candidates {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}

	t.Skip("no Chromium binary found on PATH; skipping chromedp integration test")
	return ""
}

func TestChromedpFetcher_Fetch(t *testing.T) {
	t.Parallel()

	t.Run("applies default networkIdleMillis when zero", func(t *testing.T) {
		t.Parallel()
		f := NewChromedpFetcher(ChromedpFetcherOptions{})
		if f.networkIdleMillisForTest() != 5000 {
			t.Fatalf("expected default networkIdleMs=5000, got %d", f.networkIdleMillisForTest())
		}
	})

	t.Run("returns rendered DOM of a static page", func(t *testing.T) {
		t.Parallel()
		chromiumPath := findChromiumOrSkip(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, err := w.Write([]byte(`<html><body><p id="sentinel">hello-static</p></body></html>`))
			if err != nil {
				t.Logf("test server write error: %v", err)
			}
		}))
		t.Cleanup(srv.Close)

		f := NewChromedpFetcher(ChromedpFetcherOptions{
			Timeout:           30 * time.Second,
			ChromiumPath:      chromiumPath,
			NetworkIdleMillis: 500,
		})

		body, err := f.Fetch(t.Context(), srv.URL)
		require.NoError(t, err)
		assert.Contains(t, string(body), "hello-static")
	})

	t.Run("returns post-hydration DOM when JS injects content", func(t *testing.T) {
		t.Parallel()
		chromiumPath := findChromiumOrSkip(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, err := w.Write([]byte(`<html><body></body><script>document.body.innerHTML = '<span id="x">hello</span>';</script></html>`))
			if err != nil {
				t.Logf("test server write error: %v", err)
			}
		}))
		t.Cleanup(srv.Close)

		f := NewChromedpFetcher(ChromedpFetcherOptions{
			Timeout:           30 * time.Second,
			ChromiumPath:      chromiumPath,
			NetworkIdleMillis: 800,
		})

		body, err := f.Fetch(t.Context(), srv.URL)
		require.NoError(t, err)
		// The JS-injected span must appear in the captured outer HTML.
		assert.Contains(t, string(body), `id="x"`)
		assert.Contains(t, string(body), "hello")
	})

	t.Run("respects timeout", func(t *testing.T) {
		t.Parallel()
		chromiumPath := findChromiumOrSkip(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Sleep much longer than the fetcher timeout so the deadline fires first.
			select {
			case <-time.After(60 * time.Second):
				_, _ = w.Write([]byte("too late"))
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(srv.Close)

		f := NewChromedpFetcher(ChromedpFetcherOptions{
			Timeout:           3 * time.Second,
			ChromiumPath:      chromiumPath,
			NetworkIdleMillis: 100,
		})

		_, err := f.Fetch(context.Background(), srv.URL)
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.DeadlineExceeded),
			"expected DeadlineExceeded wrapped in fetch error, got: %v", err)
	})

	t.Run("propagates fetch error on invalid url", func(t *testing.T) {
		t.Parallel()
		chromiumPath := findChromiumOrSkip(t)

		f := NewChromedpFetcher(ChromedpFetcherOptions{
			Timeout:           10 * time.Second,
			ChromiumPath:      chromiumPath,
			NetworkIdleMillis: 100,
		})

		// Port 1 on loopback is effectively always closed.
		_, err := f.Fetch(t.Context(), "http://127.0.0.1:1/")
		require.Error(t, err)
	})

	t.Run("applies WaitSelector when set", func(t *testing.T) {
		t.Parallel()
		chromiumPath := findChromiumOrSkip(t)

		// The page starts empty then injects #rate-loaded after 1 s via JS.
		// With NetworkIdleMillis=100, the default sleep path would finish
		// before the element appears. WaitSelector blocks until it is visible,
		// so the captured DOM must contain the element.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, err := w.Write([]byte(`<html><body>
<script>
setTimeout(function() {
  var el = document.createElement('div');
  el.id = 'rate-loaded';
  el.textContent = 'loaded';
  document.body.appendChild(el);
}, 1000);
</script>
</body></html>`))
			if err != nil {
				t.Logf("test server write error: %v", err)
			}
		}))
		t.Cleanup(srv.Close)

		f := NewChromedpFetcher(ChromedpFetcherOptions{
			Timeout:           30 * time.Second,
			ChromiumPath:      chromiumPath,
			NetworkIdleMillis: 100,
			WaitSelector:      "#rate-loaded",
		})

		body, err := f.Fetch(t.Context(), srv.URL)
		require.NoError(t, err)
		assert.Contains(t, string(body), `id="rate-loaded"`,
			"WaitSelector must block until the injected element is visible")
	})
}
