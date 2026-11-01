package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogger(t *testing.T) {
	t.Parallel()

	t.Run("logs default 200 when inner handler writes no status", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		h := Logger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}), &buf)
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
		h.ServeHTTP(rec, req)
		got := buf.String()
		require.Contains(t, got, "middleware [200] GET /healthz",
			"expected access log line, got %q", got)
	})

	t.Run("captures explicit status code", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		h := Logger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}), &buf)
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/missing", nil)
		h.ServeHTTP(rec, req)
		require.Contains(t, buf.String(), "middleware [404] POST /api/missing")
	})

	t.Run("ignores second WriteHeader call", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		h := Logger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			w.WriteHeader(http.StatusInternalServerError)
		}), &buf)
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/echo", nil)
		h.ServeHTTP(rec, req)
		require.Contains(t, buf.String(), "middleware [202] PUT /api/echo")
		require.NotContains(t, buf.String(), "[500]",
			"second WriteHeader must be ignored")
	})
}
