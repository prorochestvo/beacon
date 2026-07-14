package apiclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

var _ apiclient.Fetcher = (*fakeFetcher)(nil)

// fakeFetcher is an in-memory Fetcher for unit tests. Callers set jsonResponse
// (for FetchJSON) or noContentErr (for FetchNoContent) before each call; the
// last request's method, url, body, and headers are recorded for assertions.
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

func TestClient_MeRatesChart(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes chart response", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"window":"7 days","pairs":[{"pair":"USD/KZT","category":"fiat","spread_pct":0.29,"series":[{"kind":"BID","color":"#1D9E75","latest":449.5,"delta_pct":0.1,"sparse":false,"points":[{"timestamp":"2026-01-01T00:00:00Z","value":449.5}]},{"kind":"ASK","color":"#378ADD","latest":450.0,"delta_pct":0.0,"sparse":false}]}]}`),
		}
		c := apiclient.New(f)
		got, err := c.MeRatesChart(t.Context(), "test-init-data", 7)
		require.NoError(t, err)
		assert.Equal(t, "7 days", got.Window)
		require.Len(t, got.Pairs, 1)
		assert.Equal(t, "USD/KZT", got.Pairs[0].Pair)
		assert.Equal(t, "fiat", got.Pairs[0].Category)
		require.NotNil(t, got.Pairs[0].SpreadPct)
		assert.InDelta(t, 0.29, *got.Pairs[0].SpreadPct, 0.001)
		require.Len(t, got.Pairs[0].Series, 2)
		assert.Equal(t, "BID", got.Pairs[0].Series[0].Kind)
		assert.Equal(t, "#1D9E75", got.Pairs[0].Series[0].Color)
		assert.Equal(t, 449.5, got.Pairs[0].Series[0].Latest)
		require.Len(t, got.Pairs[0].Series[0].Points, 1)
		assert.Equal(t, "ASK", got.Pairs[0].Series[1].Kind)
	})

	t.Run("decode error on invalid json", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.MeRatesChart(t.Context(), "tok", 7)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode me rates chart")
	})

	t.Run("fetcher error propagates verbatim", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("connection refused")}
		c := apiclient.New(f)
		_, err := c.MeRatesChart(t.Context(), "tok", 7)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection refused")
	})

	t.Run("header is set from initData parameter", func(t *testing.T) {
		t.Parallel()
		const tok = "query_id=AAH&user=%7B%22id%22%3A123%7D"
		f := &fakeFetcher{
			jsonResponse: []byte(`{"window":"168h0m0s","pairs":[]}`),
		}
		c := apiclient.New(f)
		_, err := c.MeRatesChart(t.Context(), tok, 7)
		require.NoError(t, err)
		assert.Equal(t, tok, f.lastHeaders["X-Telegram-Init-Data"])
	})

	t.Run("period=30 is forwarded in URL query string", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`{"window":"30 days","pairs":[]}`)}
		c := apiclient.New(f)
		got, err := c.MeRatesChart(t.Context(), "tok", 30)
		require.NoError(t, err)
		assert.Equal(t, "30 days", got.Window)
		assert.Contains(t, f.lastURL, "period=30", "period must appear in the request URL")
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

func TestClient_MeRatesHistory(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes history response", func(t *testing.T) {
		t.Parallel()
		body := `{"pair":"USD/KZT","page":1,"limit":20,"total":1,"items":[{"source_title":"Test","timestamp":"2026-05-30T12:00:00Z","bid":490.0}]}`
		f := &fakeFetcher{jsonResponse: []byte(body)}
		c := apiclient.New(f)
		resp, err := c.MeRatesHistory(t.Context(), "init", "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		assert.Equal(t, "USD/KZT", resp.Pair)
		assert.EqualValues(t, 1, resp.Total)
		require.Len(t, resp.Items, 1)
		require.NotNil(t, resp.Items[0].Bid)
		assert.Equal(t, 490.0, *resp.Items[0].Bid)
	})

	t.Run("X-Telegram-Init-Data header is forwarded", func(t *testing.T) {
		t.Parallel()
		const initData = "query_id=AAH&user=%7B%22id%22%3A123%7D"
		f := &fakeFetcher{jsonResponse: []byte(`{"pair":"USD/KZT","page":1,"limit":20,"total":0,"items":[]}`)}
		c := apiclient.New(f)
		_, err := c.MeRatesHistory(t.Context(), initData, "USD/KZT", "", 1, 20)
		require.NoError(t, err)
		assert.Equal(t, initData, f.lastHeaders["X-Telegram-Init-Data"])
	})

	t.Run("server 401 propagates as error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 401")}
		c := apiclient.New(f)
		_, err := c.MeRatesHistory(t.Context(), "bad", "USD/KZT", "", 1, 20)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 401")
	})

	t.Run("malformed json returns decode error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`not-json`)}
		c := apiclient.New(f)
		_, err := c.MeRatesHistory(t.Context(), "tok", "USD/KZT", "", 1, 20)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode me rates history")
	})

	t.Run("server 500 propagates as non-nil error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.MeRatesHistory(t.Context(), "tok", "USD/KZT", "", 1, 20)
		require.Error(t, err)
	})

	t.Run("forwards source_title to URL", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`{"pair":"USD/KZT","page":1,"limit":20,"total":0,"items":[]}`)}
		c := apiclient.New(f)
		_, err := c.MeRatesHistory(t.Context(), "tok", "USD/KZT", "Kaspi", 1, 20)
		require.NoError(t, err)
		assert.Contains(t, f.lastURL, "source_title=Kaspi", "source_title must be present in the request URL")
	})
}

func TestClient_MeSubscriptionsRaw(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes raw subscription list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"items":[{"id":"sub-1","source_name":"src_a","source_title":"Alpha","base_currency":"USD","quote_currency":"KZT","condition_type":"delta","condition_value":"5","updated_at":"2024-01-01T00:00:00Z"}]}`),
		}
		c := apiclient.New(f)
		got, err := c.MeSubscriptionsRaw(t.Context(), "initdata-token")
		require.NoError(t, err)
		require.Len(t, got.Items, 1)
		assert.Equal(t, "sub-1", got.Items[0].ID)
		assert.Equal(t, "src_a", got.Items[0].SourceName)
		assert.Equal(t, "initdata-token", f.lastHeaders["X-Telegram-Init-Data"])
		assert.Equal(t, "/api/me/subscriptions/raw", f.lastURL)
	})

	t.Run("401 propagates as error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 401")}
		c := apiclient.New(f)
		_, err := c.MeSubscriptionsRaw(t.Context(), "bad-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("empty items returns non-nil slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: []byte(`{"items":[]}`)}
		c := apiclient.New(f)
		got, err := c.MeSubscriptionsRaw(t.Context(), "tok")
		require.NoError(t, err)
		assert.NotNil(t, got.Items)
		assert.Empty(t, got.Items)
	})
}

func TestClient_MeSubscriptionCreate(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns generated ID", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"id":"generated-123"}`),
		}
		c := apiclient.New(f)
		req := dto.MeSubscriptionCreateRequest{
			SourceName:     "src_a",
			ConditionType:  "delta",
			ConditionValue: "5",
		}
		got, err := c.MeSubscriptionCreate(t.Context(), "tok", req)
		require.NoError(t, err)
		assert.Equal(t, "generated-123", got.ID)
		assert.Equal(t, "tok", f.lastHeaders["X-Telegram-Init-Data"])
		assert.Equal(t, "/api/me/subscriptions", f.lastURL)
		assert.Equal(t, "POST", f.lastMethod)
	})

	t.Run("401 propagates as error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 401")}
		c := apiclient.New(f)
		_, err := c.MeSubscriptionCreate(t.Context(), "bad", dto.MeSubscriptionCreateRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})
}

func TestClient_MeSubscriptionUpdate(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns nil on 204", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{}
		c := apiclient.New(f)
		req := dto.MeSubscriptionUpdateRequest{ConditionType: "interval", ConditionValue: "1h"}
		err := c.MeSubscriptionUpdate(t.Context(), "tok", "sub-abc", req)
		require.NoError(t, err)
		assert.Equal(t, "tok", f.lastHeaders["X-Telegram-Init-Data"])
		assert.Contains(t, f.lastURL, "sub-abc")
		assert.Equal(t, "PATCH", f.lastMethod)
	})

	t.Run("401 propagates as error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{noContentErr: errors.New("http 401")}
		c := apiclient.New(f)
		err := c.MeSubscriptionUpdate(t.Context(), "bad", "sub-abc", dto.MeSubscriptionUpdateRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("id with slash is percent-escaped in URL", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{}
		c := apiclient.New(f)
		err := c.MeSubscriptionUpdate(t.Context(), "tok", "a/b", dto.MeSubscriptionUpdateRequest{ConditionType: "delta", ConditionValue: "1"})
		require.NoError(t, err)
		assert.Contains(t, f.lastURL, "a%2Fb", "slash must be percent-escaped in path segment")
	})
}

func TestClient_MeWeatherCitiesSearch(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes search response", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"items":[{"location_id":"1526384","display_name":"Алматы","latitude":43.25,"longitude":76.91,"timezone":"Asia/Almaty","country":"Казахстан","admin1":"Алматы"}]}`),
		}
		c := apiclient.New(f)
		got, err := c.MeWeatherCitiesSearch(t.Context(), "tok", "Almaty")
		require.NoError(t, err)
		require.Len(t, got.Items, 1)
		assert.Equal(t, "1526384", got.Items[0].LocationID)
		assert.Equal(t, "Алматы", got.Items[0].DisplayName)
		assert.Equal(t, "tok", f.lastHeaders["X-Telegram-Init-Data"])
	})

	t.Run("fetcher error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.MeWeatherCitiesSearch(t.Context(), "tok", "Almaty")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 500")
	})
}

func TestClient_MeWeatherCities(t *testing.T) {
	t.Parallel()

	t.Run("happy path decodes cities list", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"items":[{"id":"c1","location_id":"1526384","display_name":"Алматы","latitude":43.25,"longitude":76.91,"timezone":"Asia/Almaty","country":"Казахстан","admin1":"Алматы","notify_hour":7}]}`),
		}
		c := apiclient.New(f)
		got, err := c.MeWeatherCities(t.Context(), "tok")
		require.NoError(t, err)
		require.Len(t, got.Items, 1)
		assert.Equal(t, "c1", got.Items[0].ID)
		assert.Equal(t, 7, got.Items[0].NotifyHour)
		assert.Equal(t, "tok", f.lastHeaders["X-Telegram-Init-Data"])
	})

	t.Run("fetcher error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 401")}
		c := apiclient.New(f)
		_, err := c.MeWeatherCities(t.Context(), "bad")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 401")
	})
}

func TestClient_MeWeatherCityCreate(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns generated ID", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{
			jsonResponse: []byte(`{"id":"generated-city-id"}`),
		}
		c := apiclient.New(f)
		body := dto.WeatherCityCreateRequest{
			LocationID:  "1526384",
			DisplayName: "Алматы",
			Latitude:    43.25,
			Longitude:   76.91,
			Timezone:    "Asia/Almaty",
			Country:     "Казахстан",
			Admin1:      "Алматы",
		}
		got, err := c.MeWeatherCityCreate(t.Context(), "tok", body)
		require.NoError(t, err)
		assert.Equal(t, "generated-city-id", got.ID)
		assert.Equal(t, "tok", f.lastHeaders["X-Telegram-Init-Data"])
		assert.Equal(t, "POST", f.lastMethod)
	})

	t.Run("fetcher error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("http 500")}
		c := apiclient.New(f)
		_, err := c.MeWeatherCityCreate(t.Context(), "tok", dto.WeatherCityCreateRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 500")
	})
}

func TestClient_MeWeatherCityDelete(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns nil on 204", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{}
		c := apiclient.New(f)
		err := c.MeWeatherCityDelete(t.Context(), "tok", "city-abc")
		require.NoError(t, err)
		assert.Equal(t, "tok", f.lastHeaders["X-Telegram-Init-Data"])
		assert.Contains(t, f.lastURL, "city-abc")
		assert.Equal(t, "DELETE", f.lastMethod)
	})

	t.Run("fetcher error propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{noContentErr: errors.New("http 404")}
		c := apiclient.New(f)
		err := c.MeWeatherCityDelete(t.Context(), "tok", "nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 404")
	})
}

func TestClient_MeSubscriptionDelete(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns nil on 204", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{}
		c := apiclient.New(f)
		err := c.MeSubscriptionDelete(t.Context(), "tok", "sub-xyz")
		require.NoError(t, err)
		assert.Equal(t, "tok", f.lastHeaders["X-Telegram-Init-Data"])
		assert.Contains(t, f.lastURL, "sub-xyz")
		assert.Equal(t, "DELETE", f.lastMethod)
	})

	t.Run("401 propagates as error", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{noContentErr: errors.New("http 401")}
		c := apiclient.New(f)
		err := c.MeSubscriptionDelete(t.Context(), "bad", "sub-xyz")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("id with slash is percent-escaped in URL", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{}
		c := apiclient.New(f)
		err := c.MeSubscriptionDelete(t.Context(), "tok", "a/b")
		require.NoError(t, err)
		assert.Contains(t, f.lastURL, "a%2Fb", "slash must be percent-escaped in path segment")
	})
}
