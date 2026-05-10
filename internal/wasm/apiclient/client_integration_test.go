//go:build !js
// +build !js

// Package apiclient_test exercises every Client method end-to-end against a
// real httptest.Server via the httpFetcher, verifying the full request/response
// round-trip without any WASM runtime.
package apiclient_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/internal/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

// newIntegrationClient creates a test server, wires a Client to it via
// NewHTTPFetcher, and returns both together with a cleanup function. Each
// integration test owns its own server so parallel execution is safe.
func newIntegrationClient(t *testing.T) (*apiclient.Client, *http.ServeMux, func()) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	fetcher := apiclient.NewHTTPFetcher(srv.URL, nil)
	client := apiclient.New(fetcher)
	return client, mux, srv.Close
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(v)
	require.NoError(t, err)
}

func TestClient_Integration_ListSources(t *testing.T) {
	t.Parallel()
	client, mux, cleanup := newIntegrationClient(t)
	defer cleanup()

	want := []api.SourceResponse{{
		Name:          "usd-eur",
		Title:         "USD/EUR",
		BaseCurrency:  "USD",
		QuoteCurrency: "EUR",
		Interval:      "1h",
		Active:        true,
		LastSuccess:   true,
	}}
	mux.HandleFunc("/api/sources", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "10", r.URL.Query().Get("limit"))
		writeJSON(t, w, want)
	})

	got, err := client.ListSources(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, want[0].Name, got[0].Name)
	assert.Equal(t, want[0].Title, got[0].Title)
	assert.Equal(t, want[0].Active, got[0].Active)
}

func TestClient_Integration_ListRates(t *testing.T) {
	t.Parallel()
	client, mux, cleanup := newIntegrationClient(t)
	defer cleanup()

	want := []api.RateResponse{{
		ID:            "r1",
		BaseCurrency:  "USD",
		QuoteCurrency: "EUR",
		Price:         1.08,
		Timestamp:     "2026-01-01T00:00:00Z",
	}}
	// /api/sources/{name}/rates uses a subtree match; the handler inspects the path.
	mux.HandleFunc("/api/sources/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/sources/usd-eur/rates", r.URL.Path)
		assert.Equal(t, "50", r.URL.Query().Get("limit"))
		writeJSON(t, w, want)
	})

	got, err := client.ListRates(context.Background(), "usd-eur", 50)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, want[0].ID, got[0].ID)
	assert.InDelta(t, want[0].Price, got[0].Price, 0.001)
}

func TestClient_Integration_ListSubscriptions(t *testing.T) {
	t.Parallel()
	client, mux, cleanup := newIntegrationClient(t)
	defer cleanup()

	want := []api.SubscriptionDetailResponse{{
		ID:         "s1",
		UserType:   "telegram",
		SourceName: "usd-eur",
		Condition:  ">1.05",
	}}
	mux.HandleFunc("/api/sources/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/sources/usd-eur/subscriptions/list", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("page"))
		writeJSON(t, w, want)
	})

	got, err := client.ListSubscriptions(context.Background(), "usd-eur", 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, want[0].ID, got[0].ID)
	assert.Equal(t, want[0].Condition, got[0].Condition)
}

func TestClient_Integration_ListDailyEvents(t *testing.T) {
	t.Parallel()
	client, mux, cleanup := newIntegrationClient(t)
	defer cleanup()

	want := []api.DailyEventResponse{{
		Type:         "fetch",
		Date:         "2026-01-01",
		SuccessCount: 5,
		FailedCount:  1,
	}}
	mux.HandleFunc("/api/sources/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/sources/usd-eur/events/daily", r.URL.Path)
		assert.Equal(t, "2", r.URL.Query().Get("page"))
		writeJSON(t, w, want)
	})

	got, err := client.ListDailyEvents(context.Background(), "usd-eur", 2)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, int64(5), got[0].SuccessCount)
	assert.Equal(t, int64(1), got[0].FailedCount)
}

func TestClient_Integration_ListExecutionErrors(t *testing.T) {
	t.Parallel()
	client, mux, cleanup := newIntegrationClient(t)
	defer cleanup()

	want := []api.ExecutionErrorResponse{{
		ID:         "e1",
		SourceName: "usd-eur",
		Error:      "timeout",
		Timestamp:  "2026-01-01T00:00:00Z",
	}}
	mux.HandleFunc("/api/errors/execution", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "1", r.URL.Query().Get("page"))
		writeJSON(t, w, want)
	})

	got, err := client.ListExecutionErrors(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, want[0].ID, got[0].ID)
	assert.Equal(t, want[0].Error, got[0].Error)
}

func TestClient_Integration_ListFailedNotifications(t *testing.T) {
	t.Parallel()
	client, mux, cleanup := newIntegrationClient(t)
	defer cleanup()

	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	want := []api.NotificationResponse{{
		ID:        "n1",
		UserType:  "telegram",
		Status:    "failed",
		CreatedAt: ts,
		SentAt:    ts,
	}}
	mux.HandleFunc("/api/notifications/failed", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "0", r.URL.Query().Get("offset"))
		assert.Equal(t, "50", r.URL.Query().Get("limit"))
		writeJSON(t, w, want)
	})

	got, err := client.ListFailedNotifications(context.Background(), 0, 50)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, want[0].ID, got[0].ID)
	assert.Equal(t, want[0].Status, got[0].Status)
}

func TestClient_Integration_Stats(t *testing.T) {
	t.Parallel()
	client, mux, cleanup := newIntegrationClient(t)
	defer cleanup()

	want := api.StatsResponse{
		SourcesTotal:  10,
		SourcesActive: 8,
		ErrorsTotal:   3,
	}
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		writeJSON(t, w, want)
	})

	got, err := client.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want.SourcesTotal, got.SourcesTotal)
	assert.Equal(t, want.SourcesActive, got.SourcesActive)
	assert.Equal(t, want.ErrorsTotal, got.ErrorsTotal)
}

func TestClient_Integration_SetSourceActive(t *testing.T) {
	t.Parallel()

	t.Run("active true sends PATCH with correct body and returns nil on 204", func(t *testing.T) {
		t.Parallel()
		client, mux, cleanup := newIntegrationClient(t)
		defer cleanup()

		mux.HandleFunc("/api/sources/", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "PATCH", r.Method)
			assert.Equal(t, "/api/sources/usd-eur/active", r.URL.Path)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var body api.SourceActiveRequest
			require.NoError(t, json.Unmarshal(raw, &body))
			assert.True(t, body.Active)

			w.WriteHeader(http.StatusNoContent)
		})

		err := client.SetSourceActive(context.Background(), "usd-eur", true)
		require.NoError(t, err)
	})

	t.Run("active false sends correct body", func(t *testing.T) {
		t.Parallel()
		client, mux, cleanup := newIntegrationClient(t)
		defer cleanup()

		mux.HandleFunc("/api/sources/", func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var body api.SourceActiveRequest
			require.NoError(t, json.Unmarshal(raw, &body))
			assert.False(t, body.Active)
			w.WriteHeader(http.StatusNoContent)
		})

		err := client.SetSourceActive(context.Background(), "usd-eur", false)
		require.NoError(t, err)
	})

	t.Run("non-2xx from server propagates as error", func(t *testing.T) {
		t.Parallel()
		client, mux, cleanup := newIntegrationClient(t)
		defer cleanup()

		mux.HandleFunc("/api/sources/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		err := client.SetSourceActive(context.Background(), "usd-eur", true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 500")
	})
}

func TestClient_Integration_MeSubscriptions(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes response and propagates initData header", func(t *testing.T) {
		t.Parallel()
		client, mux, cleanup := newIntegrationClient(t)
		defer cleanup()

		const initData = "query_id=AAH&user=%7B%22id%22%3A123%7D"
		want := api.MeSubscriptionsResponse{
			Items: []api.MeSubscriptionRow{{
				SourceName:    "usd-eur",
				SourceTitle:   "USD/EUR",
				BaseCurrency:  "USD",
				QuoteCurrency: "EUR",
				Conditions:    []string{">1.05"},
				LatestPrice:   1.08,
			}},
			Page:     1,
			PageSize: 10,
			Total:    1,
		}
		mux.HandleFunc("/api/me/subscriptions", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "1", r.URL.Query().Get("page"))
			assert.Equal(t, "10", r.URL.Query().Get("page_size"))
			assert.Equal(t, "eur", r.URL.Query().Get("q"))
			assert.Equal(t, initData, r.Header.Get("X-Telegram-Init-Data"))
			writeJSON(t, w, want)
		})

		got, err := client.MeSubscriptions(context.Background(), initData, 1, 10, "eur")
		require.NoError(t, err)
		assert.Equal(t, want.Total, got.Total)
		require.Len(t, got.Items, 1)
		assert.Equal(t, want.Items[0].SourceName, got.Items[0].SourceName)
		assert.InDelta(t, want.Items[0].LatestPrice, got.Items[0].LatestPrice, 0.001)
	})

	t.Run("401 from server propagates as error containing http 401", func(t *testing.T) {
		t.Parallel()
		client, mux, cleanup := newIntegrationClient(t)
		defer cleanup()

		mux.HandleFunc("/api/me/subscriptions", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})

		_, err := client.MeSubscriptions(context.Background(), "bad-token", 1, 10, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 401")
	})

	t.Run("empty q is omitted from query string", func(t *testing.T) {
		t.Parallel()
		client, mux, cleanup := newIntegrationClient(t)
		defer cleanup()

		mux.HandleFunc("/api/me/subscriptions", func(w http.ResponseWriter, r *http.Request) {
			// Use direct map presence check — Query().Get("q") returns "" for both
			// absent and q= cases, so it cannot distinguish them.
			_, present := r.URL.Query()["q"]
			assert.False(t, present, "q should be absent from the URL when empty string is passed")
			writeJSON(t, w, api.MeSubscriptionsResponse{})
		})

		_, err := client.MeSubscriptions(context.Background(), "tok", 1, 10, "")
		require.NoError(t, err)
	})
}

func TestClient_Integration_ListRates_PathEscape(t *testing.T) {
	t.Parallel()

	t.Run("name with slash is path-escaped in request URL", func(t *testing.T) {
		t.Parallel()
		client, mux, cleanup := newIntegrationClient(t)
		defer cleanup()

		mux.HandleFunc("/api/sources/", func(w http.ResponseWriter, r *http.Request) {
			// net/http decodes the path in URL.Path; EscapedPath preserves the
			// raw percent-encoded form as sent on the wire.
			assert.Equal(t, "/api/sources/a%2Fb/rates", r.URL.EscapedPath())
			writeJSON(t, w, []api.RateResponse{})
		})

		_, err := client.ListRates(context.Background(), "a/b", 10)
		require.NoError(t, err)
	})
}
