package apiclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

var _ apiclient.Fetcher = (*fakeFetcher)(nil)

// fakeFetcher is an in-memory Fetcher for unit tests.
// Callers set jsonResponse (for FetchJSON) or noContentErr (for FetchNoContent)
// before each call. The last request's method, url, body, and headers are
// recorded so tests can assert them.
type fakeFetcher struct {
	jsonResponse []byte
	jsonErr      error

	noContentErr error

	lastMethod  string
	lastURL     string
	lastBody    any
	lastHeaders map[string]string
}

func (f *fakeFetcher) FetchJSON(_ context.Context, method, url string, body any, headers map[string]string) ([]byte, error) {
	f.lastMethod = method
	f.lastURL = url
	f.lastBody = body
	f.lastHeaders = headers
	if f.jsonErr != nil {
		return nil, f.jsonErr
	}
	return f.jsonResponse, nil
}

func (f *fakeFetcher) FetchNoContent(_ context.Context, method, url string, body any, headers map[string]string) error {
	f.lastMethod = method
	f.lastURL = url
	f.lastBody = body
	f.lastHeaders = headers
	return f.noContentErr
}

func TestClient_ListSources(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes source list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`[{"name":"usd-eur","title":"USD/EUR","base_currency":"USD","quote_currency":"EUR","interval":"1h","active":true,"last_success":true}]`),
		}
		c := apiclient.New(f)
		got, err := c.ListSources(t.Context(), 10)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "usd-eur", got[0].Name)
		assert.Equal(t, "USD/EUR", got[0].Title)
	})

	t.Run("empty list returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`[]`)}
		c := apiclient.New(f)
		got, err := c.ListSources(t.Context(), 10)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.ListSources(t.Context(), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 500")
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.ListSources(t.Context(), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode sources")
	})
}

func TestClient_ListRates(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes rate list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`[{"id":"1","base_currency":"USD","quote_currency":"EUR","price":1.08,"timestamp":"2026-01-01T00:00:00Z"}]`),
		}
		c := apiclient.New(f)
		got, err := c.ListRates(t.Context(), "usd-eur", 50)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "USD", got[0].BaseCurrency)
		assert.InDelta(t, 1.08, got[0].Price, 0.001)
	})

	t.Run("empty list returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`[]`)}
		c := apiclient.New(f)
		got, err := c.ListRates(t.Context(), "usd-eur", 50)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.ListRates(t.Context(), "usd-eur", 50)
		require.Error(t, err)
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.ListRates(t.Context(), "usd-eur", 50)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode rates")
	})
}

func TestClient_ListSubscriptions(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes subscription list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`[{"id":"s1","user_type":"telegram","source_name":"usd-eur","condition":">1.05"}]`),
		}
		c := apiclient.New(f)
		got, err := c.ListSubscriptions(t.Context(), "usd-eur", 1)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "s1", got[0].ID)
	})

	t.Run("empty list returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`[]`)}
		c := apiclient.New(f)
		got, err := c.ListSubscriptions(t.Context(), "usd-eur", 1)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.ListSubscriptions(t.Context(), "usd-eur", 1)
		require.Error(t, err)
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.ListSubscriptions(t.Context(), "usd-eur", 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode subscriptions")
	})
}

func TestClient_ListDailyEvents(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes daily event list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`[{"type":"fetch","date":"2026-01-01","success_count":5,"failed_count":1}]`),
		}
		c := apiclient.New(f)
		got, err := c.ListDailyEvents(t.Context(), "usd-eur", 1)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, int64(5), got[0].SuccessCount)
	})

	t.Run("empty list returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`[]`)}
		c := apiclient.New(f)
		got, err := c.ListDailyEvents(t.Context(), "usd-eur", 1)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.ListDailyEvents(t.Context(), "usd-eur", 1)
		require.Error(t, err)
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.ListDailyEvents(t.Context(), "usd-eur", 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode daily events")
	})
}

func TestClient_ListExecutionErrors(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes execution error list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`[{"id":"e1","source_name":"usd-eur","error":"timeout","timestamp":"2026-01-01T00:00:00Z"}]`),
		}
		c := apiclient.New(f)
		got, err := c.ListExecutionErrors(t.Context(), 1)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "e1", got[0].ID)
	})

	t.Run("empty list returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`[]`)}
		c := apiclient.New(f)
		got, err := c.ListExecutionErrors(t.Context(), 1)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.ListExecutionErrors(t.Context(), 1)
		require.Error(t, err)
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.ListExecutionErrors(t.Context(), 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode execution errors")
	})
}

func TestClient_ListFailedNotifications(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes notification list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`[{"id":"n1","user_type":"telegram","status":"failed","created_at":"2026-01-01T00:00:00Z","sent_at":"2026-01-01T00:00:00Z"}]`),
		}
		c := apiclient.New(f)
		got, err := c.ListFailedNotifications(t.Context(), 0, 50)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "n1", got[0].ID)
	})

	t.Run("empty list returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`[]`)}
		c := apiclient.New(f)
		got, err := c.ListFailedNotifications(t.Context(), 0, 50)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.ListFailedNotifications(t.Context(), 0, 50)
		require.Error(t, err)
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.ListFailedNotifications(t.Context(), 0, 50)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode failed notifications")
	})
}

func TestClient_Stats(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes stats", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"sources_total":10,"sources_active":8,"errors_total":3}`),
		}
		c := apiclient.New(f)
		got, err := c.Stats(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(10), got.SourcesTotal)
		assert.Equal(t, int64(8), got.SourcesActive)
		assert.Equal(t, int64(3), got.ErrorsTotal)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.Stats(t.Context())
		require.Error(t, err)
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.Stats(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode stats")
	})
}

func TestClient_SetSourceActive(t *testing.T) {
	t.Parallel()

	t.Run("happy path calls FetchNoContent with correct body", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{}
		c := apiclient.New(f)
		err := c.SetSourceActive(t.Context(), "usd-eur", true)
		require.NoError(t, err)
		assert.Equal(t, "PATCH", f.lastMethod)
		assert.Equal(t, dto.SourceActiveRequest{Active: true}, f.lastBody)
	})

	t.Run("sets active false sends correct body", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{}
		c := apiclient.New(f)
		err := c.SetSourceActive(t.Context(), "usd-eur", false)
		require.NoError(t, err)
		assert.Equal(t, dto.SourceActiveRequest{Active: false}, f.lastBody)
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{noContentErr: errors.New("http 500")}
		c := apiclient.New(f)
		err := c.SetSourceActive(t.Context(), "usd-eur", true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 500")
	})
}

func TestClient_RatesChart(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes two chart points", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`[{"label":"2026-01-01","price":1.08},{"label":"2026-01-02","price":1.09}]`),
		}
		c := apiclient.New(f)
		got, err := c.RatesChart(t.Context(), "usd-eur", "week")
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "2026-01-01", got[0].Label)
		assert.InDelta(t, 1.08, got[0].Price, 0.001)
		assert.Equal(t, "2026-01-02", got[1].Label)
		assert.InDelta(t, 1.09, got[1].Price, 0.001)
	})

	t.Run("empty chart returns non-nil empty slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`[]`)}
		c := apiclient.New(f)
		got, err := c.RatesChart(t.Context(), "usd-eur", "week")
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("decode error on invalid JSON", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.RatesChart(t.Context(), "usd-eur", "week")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode chart points")
	})

	t.Run("fetcher error propagates verbatim", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 503")}
		c := apiclient.New(f)
		_, err := c.RatesChart(t.Context(), "usd-eur", "week")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 503")
	})
}

func TestClient_MeSubscriptions(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes response", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"items":[{"source_name":"usd-eur","source_title":"USD/EUR","base_currency":"USD","quote_currency":"EUR","conditions":[">1.05"]}],"page":1,"page_size":10,"total":1}`),
		}
		c := apiclient.New(f)
		got, err := c.MeSubscriptions(t.Context(), "tok", 1, 10, "")
		require.NoError(t, err)
		assert.Equal(t, int64(1), got.Total)
		require.Len(t, got.Items, 1)
		assert.Equal(t, "usd-eur", got.Items[0].SourceName)
	})

	t.Run("header propagation sets X-Telegram-Init-Data", func(t *testing.T) {
		t.Parallel()
		const initData = "query_id=AAH&user=%7B%22id%22%3A123%7D"
		f := &fakeFetcher{
			jsonResponse: []byte(`{"items":[],"page":1,"page_size":10,"total":0}`),
		}
		c := apiclient.New(f)
		_, err := c.MeSubscriptions(t.Context(), initData, 1, 10, "")
		require.NoError(t, err)
		assert.Equal(t, initData, f.lastHeaders["X-Telegram-Init-Data"])
	})

	t.Run("server error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 401")}
		c := apiclient.New(f)
		_, err := c.MeSubscriptions(t.Context(), "bad-token", 1, 10, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 401")
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.MeSubscriptions(t.Context(), "tok", 1, 10, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode me subscriptions")
	})

	t.Run("empty items list is non-nil", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"items":[],"page":1,"page_size":10,"total":0}`),
		}
		c := apiclient.New(f)
		got, err := c.MeSubscriptions(t.Context(), "tok", 1, 10, "")
		require.NoError(t, err)
		assert.NotNil(t, got.Items)
		assert.Empty(t, got.Items)
	})
}
