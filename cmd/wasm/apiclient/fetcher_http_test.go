//go:build !js
// +build !js

// Package apiclient_test uses the external test package so that the tests
// exercise only the exported surface of httpFetcher (NewHTTPFetcher).
package apiclient_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/apiclient"
)

func TestNewHTTPFetcher_FetchJSON(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns response bytes", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"ok":true}`))
			require.NoError(t, err)
		}))
		defer srv.Close()

		// srv.Client() (not nil) gives each parallel test a transport private to
		// its own server; a nil client shares http.DefaultTransport, which
		// httptest.Server.Close() calls CloseIdleConnections on, racing siblings.
		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		got, err := f.FetchJSON(t.Context(), "GET", "/api/foo", nil, nil)
		require.NoError(t, err)
		assert.JSONEq(t, `{"ok":true}`, string(got))
	})

	t.Run("non-2xx returns http error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		_, err := f.FetchJSON(t.Context(), "GET", "/api/missing", nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 404")
	})

	t.Run("network error wraps with fetch prefix", func(t *testing.T) {
		t.Parallel()
		// Point at a server that is already closed so the TCP dial fails.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		_, err := f.FetchJSON(t.Context(), "GET", "/api/foo", nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetch:")
	})

	t.Run("request headers are propagated", func(t *testing.T) {
		t.Parallel()
		const wantHeader = "query_id=AAH&user=%7B%22id%22%3A123%7D"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, wantHeader, r.Header.Get("X-Telegram-Init-Data"))
			_, err := w.Write([]byte(`{}`))
			require.NoError(t, err)
		}))
		defer srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		_, err := f.FetchJSON(t.Context(), "GET", "/api/me/subscriptions", nil, map[string]string{
			"X-Telegram-Init-Data": wantHeader,
		})
		require.NoError(t, err)
	})

	t.Run("body is marshaled and Content-Type is set", func(t *testing.T) {
		t.Parallel()
		type payload struct {
			Value string `json:"value"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var p payload
			require.NoError(t, json.Unmarshal(raw, &p))
			assert.Equal(t, "hello", p.Value)
			_, err = w.Write([]byte(`{}`))
			require.NoError(t, err)
		}))
		defer srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		_, err := f.FetchJSON(t.Context(), "POST", "/api/foo", payload{Value: "hello"}, nil)
		require.NoError(t, err)
	})

	t.Run("baseURL trailing slash is trimmed", func(t *testing.T) {
		t.Parallel()
		var receivedPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedPath = r.URL.Path
			_, err := w.Write([]byte(`{}`))
			require.NoError(t, err)
		}))
		defer srv.Close()

		// URL with trailing slash; path starts with leading slash.
		f := apiclient.NewHTTPFetcher(srv.URL+"/", srv.Client())
		_, err := f.FetchJSON(t.Context(), "GET", "/api/foo", nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "/api/foo", receivedPath)
	})
}

func TestNewHTTPFetcher_FetchNoContent(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns nil error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		err := f.FetchNoContent(t.Context(), "PATCH", "/api/sources/x/active", nil, nil)
		require.NoError(t, err)
	})

	t.Run("non-2xx returns http error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		err := f.FetchNoContent(t.Context(), "PATCH", "/api/sources/x/active", nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 404")
	})

	t.Run("network error wraps with fetch prefix", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		err := f.FetchNoContent(t.Context(), "PATCH", "/api/sources/x/active", nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetch:")
	})

	t.Run("body is marshaled and Content-Type is set", func(t *testing.T) {
		t.Parallel()
		type payload struct {
			Active bool `json:"active"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var p payload
			require.NoError(t, json.Unmarshal(raw, &p))
			assert.True(t, p.Active)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		f := apiclient.NewHTTPFetcher(srv.URL, srv.Client())
		err := f.FetchNoContent(t.Context(), "PATCH", "/api/sources/x/active", payload{Active: true}, nil)
		require.NoError(t, err)
	})
}
