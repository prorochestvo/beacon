package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// meSubsFakeFetcher is a Fetcher that records every FetchJSON call and lets
// tests configure the response per call or globally.
type meSubsFakeFetcher struct {
	jsonResponse []byte
	jsonErr      error
	callCount    int
	lastHeaders  map[string]string
}

var _ apiclient.Fetcher = (*meSubsFakeFetcher)(nil)

func (f *meSubsFakeFetcher) FetchJSON(_ context.Context, _, _ string, _ any, headers map[string]string) ([]byte, error) {
	f.callCount++
	f.lastHeaders = headers
	if f.jsonErr != nil {
		return nil, f.jsonErr
	}
	return f.jsonResponse, nil
}

func (f *meSubsFakeFetcher) FetchNoContent(_ context.Context, _, _ string, _ any, _ map[string]string) error {
	return nil
}

func meSubsResponse(items []dto.MeSubscriptionRow, total int64, page, pageSize int) []byte {
	resp := dto.MeSubscriptionsResponse{
		Items:    items,
		Total:    total,
		Page:     int64(page),
		PageSize: int64(pageSize),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

func sampleItems() []dto.MeSubscriptionRow {
	return []dto.MeSubscriptionRow{
		{
			SourceName:    "usd-eur",
			SourceTitle:   "USD/EUR",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
			Conditions:    []string{">1.05"},
			LatestPrice:   1.0812,
			LatestAt:      "2026-01-01T12:00:00Z",
		},
	}
}

func newMePage(f *meSubsFakeFetcher, initData string) *application.MeSubscriptionsPage {
	c := apiclient.New(f)
	return application.NewMeSubscriptionsPage(c, initData, 10)
}

func TestMeSubscriptionsPage_LoadInitial(t *testing.T) {
	t.Parallel()

	t.Run("happy path stores items and total", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 1, 1, 10),
		}
		page := newMePage(f, "valid-init-data")
		err := page.LoadInitial(t.Context())
		require.NoError(t, err)
		st := page.State()
		assert.Len(t, st.Items, 1)
		assert.Equal(t, int64(1), st.Total)
		assert.Equal(t, 1, st.Page)
		assert.False(t, st.AuthFailure)
		assert.NoError(t, st.LastError)
	})

	t.Run("401 sets AuthFailure and clears items", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{jsonErr: errors.New("http 401")}
		page := newMePage(f, "bad-token")
		err := page.LoadInitial(t.Context())
		require.Error(t, err)
		st := page.State()
		assert.True(t, st.AuthFailure, "AuthFailure must be true on 401")
		assert.Empty(t, st.Items)
		assert.Equal(t, int64(0), st.Total)
		assert.ErrorContains(t, st.LastError, "http 401")
	})

	t.Run("generic server error sets LastError without AuthFailure", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{jsonErr: errors.New("http 500")}
		page := newMePage(f, "tok")
		err := page.LoadInitial(t.Context())
		require.Error(t, err)
		st := page.State()
		assert.False(t, st.AuthFailure, "AuthFailure must be false for non-401 errors")
		assert.ErrorContains(t, st.LastError, "http 500")
	})

	t.Run("resets page to 1 regardless of prior state", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 5, 1, 10),
		}
		page := newMePage(f, "tok")
		// Simulate user having navigated to page 3 before calling LoadInitial.
		err := page.NextPage(t.Context())
		require.NoError(t, err)
		err = page.NextPage(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 3, page.State().Page)

		err = page.LoadInitial(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 1, page.State().Page)
	})
}

func TestMeSubscriptionsPage_NextPage(t *testing.T) {
	t.Parallel()

	t.Run("increments page and fetches", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 30, 2, 10),
		}
		page := newMePage(f, "tok")
		err := page.NextPage(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 2, page.State().Page)
	})

	t.Run("fetch error propagates and stores LastError", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{jsonErr: errors.New("http 500")}
		page := newMePage(f, "tok")
		err := page.NextPage(t.Context())
		require.Error(t, err)
		assert.ErrorContains(t, page.State().LastError, "http 500")
		// Page is still incremented even on error — matches JS behaviour where
		// currentPage++ happens before the fetch attempt.
		assert.Equal(t, 2, page.State().Page)
	})
}

func TestMeSubscriptionsPage_PrevPage(t *testing.T) {
	t.Parallel()

	t.Run("decrements page and fetches when page > 1", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 30, 1, 10),
		}
		page := newMePage(f, "tok")
		// Advance to page 2 first.
		f.jsonResponse = meSubsResponse(sampleItems(), 30, 2, 10)
		err := page.NextPage(t.Context())
		require.NoError(t, err)
		require.Equal(t, 2, page.State().Page)

		f.jsonResponse = meSubsResponse(sampleItems(), 30, 1, 10)
		err = page.PrevPage(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 1, page.State().Page)
	})

	t.Run("no-op when already on page 1", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 10, 1, 10),
		}
		page := newMePage(f, "tok")
		err := page.LoadInitial(t.Context())
		require.NoError(t, err)
		callsBefore := f.callCount

		err = page.PrevPage(t.Context())
		require.NoError(t, err)
		// No additional fetch should have been issued.
		assert.Equal(t, callsBefore, f.callCount, "PrevPage at page 1 must not issue a fetch")
		assert.Equal(t, 1, page.State().Page)
	})
}

func TestMeSubscriptionsPage_OnSearch(t *testing.T) {
	t.Parallel()

	t.Run("debounce fires exactly once for rapid keystrokes", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 1, 1, 10),
		}
		page := newMePage(f, "tok")

		// Call OnSearch twice in rapid succession within 100 ms.
		// Only the second search ("us") should result in a network call.
		// The first call's timer is cancelled by the second call (100 ms < 250 ms
		// debounce), so the first channel never sends; we capture but do not read it.
		firstDone := page.OnSearch("usd")
		time.Sleep(100 * time.Millisecond)
		done := page.OnSearch("us")
		// firstDone is intentionally not read: the debounce timer for the first
		// call was cancelled before it fired, so the channel will never receive.
		_ = firstDone

		var searchErr error
		select {
		case searchErr = <-done:
		case <-time.After(600 * time.Millisecond):
			t.Fatal("OnSearch did not fire within 600ms")
		}

		require.NoError(t, searchErr)
		// The fakeFetcher records every FetchJSON call. Only 1 is expected.
		assert.Equal(t, 1, f.callCount, "debounce must fire exactly one fetch for rapid keystrokes")
		assert.Equal(t, "us", page.State().Query)
	})

	t.Run("single search settles after 250ms", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 1, 1, 10),
		}
		page := newMePage(f, "tok")

		done := page.OnSearch("eur")
		var searchErr error
		select {
		case searchErr = <-done:
		case <-time.After(600 * time.Millisecond):
			t.Fatal("OnSearch did not fire within 600ms")
		}

		require.NoError(t, searchErr)
		st := page.State()
		assert.Equal(t, "eur", st.Query)
		assert.Equal(t, 1, st.Page, "OnSearch must reset page to 1")
		assert.Len(t, st.Items, 1)
	})

	t.Run("OnSearch 401 sets AuthFailure via debounce and returns error", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{jsonErr: errors.New("http 401")}
		page := newMePage(f, "bad-tok")

		done := page.OnSearch("usd")
		var searchErr error
		select {
		case searchErr = <-done:
		case <-time.After(600 * time.Millisecond):
			t.Fatal("OnSearch did not fire within 600ms")
		}

		require.Error(t, searchErr)
		assert.ErrorContains(t, searchErr, "http 401")
		st := page.State()
		assert.True(t, st.AuthFailure)
	})
}

func TestMeSubscriptionsPage_HeaderPropagation(t *testing.T) {
	t.Parallel()

	t.Run("X-Telegram-Init-Data header matches constructor parameter", func(t *testing.T) {
		t.Parallel()
		const initData = "query_id=AAH&user=%7B%22id%22%3A123%7D&auth_date=1000&hash=abc"
		f := &meSubsFakeFetcher{
			jsonResponse: meSubsResponse(nil, 0, 1, 10),
		}
		page := newMePage(f, initData)
		err := page.LoadInitial(t.Context())
		require.NoError(t, err)
		assert.Equal(t, initData, f.lastHeaders["X-Telegram-Init-Data"],
			"X-Telegram-Init-Data header must be forwarded from the constructor initData parameter")
	})
}

// chartFakeFetcher routes FetchJSON calls: chart-endpoint calls use chartResponse/chartErr,
// subscriptions-endpoint calls use jsonResponse.
type chartFakeFetcher struct {
	jsonResponse  []byte
	chartResponse []byte
	chartErr      error
	chartCallURL  string
}

var _ apiclient.Fetcher = (*chartFakeFetcher)(nil)

func (f *chartFakeFetcher) FetchJSON(_ context.Context, _, rawURL string, _ any, _ map[string]string) ([]byte, error) {
	if strings.Contains(rawURL, "/rates/chart") {
		f.chartCallURL = rawURL
		if f.chartErr != nil {
			return nil, f.chartErr
		}
		return f.chartResponse, nil
	}
	return f.jsonResponse, nil
}

func (f *chartFakeFetcher) FetchNoContent(_ context.Context, _, _ string, _ any, _ map[string]string) error {
	return nil
}

// captureFetcher is like chartFakeFetcher but calls onChartCall from inside the
// chart FetchJSON so callers can observe intermediate state during the fetch.
type captureFetcher struct {
	jsonResponse  []byte
	chartResponse []byte
	chartErr      error
	onChartCall   func()
}

var _ apiclient.Fetcher = (*captureFetcher)(nil)

func (f *captureFetcher) FetchJSON(_ context.Context, _, rawURL string, _ any, _ map[string]string) ([]byte, error) {
	if strings.Contains(rawURL, "/rates/chart") {
		if f.onChartCall != nil {
			f.onChartCall()
		}
		if f.chartErr != nil {
			return nil, f.chartErr
		}
		return f.chartResponse, nil
	}
	return f.jsonResponse, nil
}

func (f *captureFetcher) FetchNoContent(_ context.Context, _, _ string, _ any, _ map[string]string) error {
	return nil
}

func chartPoints(prices ...float64) []dto.ChartPointResponse {
	out := make([]dto.ChartPointResponse, len(prices))
	for i, p := range prices {
		out[i] = dto.ChartPointResponse{Label: "2026-01-01", Price: p}
	}
	return out
}

func chartResponse(points []dto.ChartPointResponse) []byte {
	b, err := json.Marshal(points)
	if err != nil {
		panic(err)
	}
	return b
}

func TestMeSubscriptionsPage_SetPeriod(t *testing.T) {
	t.Parallel()

	t.Run("valid period updates state and clears charts", func(t *testing.T) {
		t.Parallel()
		f := &chartFakeFetcher{
			jsonResponse:  meSubsResponse(sampleItems(), 1, 1, 10),
			chartResponse: chartResponse(chartPoints(1.0, 2.0)),
		}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		require.NoError(t, page.LoadInitial(t.Context()))
		gen := page.SnapshotGeneration()
		require.NoError(t, page.LoadChart(t.Context(), "usd-eur", gen))
		assert.NotEmpty(t, page.State().Charts)

		require.NoError(t, page.SetPeriod(t.Context(), application.MeSubscriptionsPeriodMonth))
		assert.Equal(t, application.MeSubscriptionsPeriodMonth, page.State().Period)
		assert.Empty(t, page.State().Charts, "SetPeriod must clear charts on period change")
		assert.Empty(t, page.State().Expanded, "SetPeriod must clear expanded on period change")
	})

	t.Run("unchanged period leaves charts intact", func(t *testing.T) {
		t.Parallel()
		f := &chartFakeFetcher{
			jsonResponse:  meSubsResponse(sampleItems(), 1, 1, 10),
			chartResponse: chartResponse(chartPoints(1.0)),
		}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		require.NoError(t, page.LoadInitial(t.Context()))
		gen := page.SnapshotGeneration()
		require.NoError(t, page.LoadChart(t.Context(), "usd-eur", gen))
		before := page.State().Charts["usd-eur"]

		// SetPeriod with same period must be a no-op.
		require.NoError(t, page.SetPeriod(t.Context(), application.MeSubscriptionsPeriodWeek))
		after := page.State().Charts["usd-eur"]
		assert.Equal(t, before, after, "unchanged period must not clear charts")
	})

	t.Run("invalid period returns PublicError and does not change state", func(t *testing.T) {
		t.Parallel()
		f := &chartFakeFetcher{jsonResponse: meSubsResponse(sampleItems(), 1, 1, 10)}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		require.NoError(t, page.LoadInitial(t.Context()))

		err := page.SetPeriod(t.Context(), "decade")
		require.Error(t, err)

		var pubErr *internal.PublicError
		require.ErrorAs(t, err, &pubErr)
		assert.Equal(t, "Invalid period.", pubErr.Details())
		assert.Equal(t, application.MeSubscriptionsPeriodWeek, page.State().Period)
	})
}

func TestMeSubscriptionsPage_LoadChart(t *testing.T) {
	t.Parallel()

	t.Run("happy path stores points and Loaded=true", func(t *testing.T) {
		f := &chartFakeFetcher{
			jsonResponse:  meSubsResponse(sampleItems(), 1, 1, 10),
			chartResponse: chartResponse(chartPoints(1.08, 1.09)),
		}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		require.NoError(t, page.LoadInitial(t.Context()))
		gen := page.SnapshotGeneration()
		require.NoError(t, page.LoadChart(t.Context(), "usd-eur", gen))
		st := page.State().Charts["usd-eur"]
		assert.True(t, st.Loaded)
		assert.False(t, st.Loading)
		assert.NoError(t, st.Error)
		require.Len(t, st.Points, 2)
		assert.InDelta(t, 1.08, st.Points[0].Price, 0.001)
	})

	t.Run("fetch error stores Error and returns it", func(t *testing.T) {
		f := &chartFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 1, 1, 10),
			chartErr:     errors.New("chart http 503"),
		}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		require.NoError(t, page.LoadInitial(t.Context()))
		gen := page.SnapshotGeneration()
		err := page.LoadChart(t.Context(), "usd-eur", gen)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chart http 503")
		st := page.State().Charts["usd-eur"]
		assert.NotNil(t, st.Error)
		assert.False(t, st.Loaded)
	})

	t.Run("loading flag is set before fetch completes", func(t *testing.T) {
		// LoadChart sets Charts[name].Loading=true synchronously before calling
		// the fetcher. We verify this by using captureFetcher.onChartCall, which
		// fires from inside FetchJSON before returning. Because the whole test is
		// single-goroutine, no mutex is needed.
		var capturedLoading bool
		var pagePtr *application.MeSubscriptionsPage

		capFetch := &captureFetcher{
			jsonResponse:  meSubsResponse(sampleItems(), 1, 1, 10),
			chartResponse: chartResponse(chartPoints(1.0)),
			onChartCall: func() {
				st := pagePtr.State().Charts["usd-eur"]
				capturedLoading = st.Loading
			},
		}
		pagePtr = application.NewMeSubscriptionsPage(apiclient.New(capFetch), "tok", 10)
		require.NoError(t, pagePtr.LoadInitial(t.Context()))
		gen := pagePtr.SnapshotGeneration()
		require.NoError(t, pagePtr.LoadChart(t.Context(), "usd-eur", gen))

		assert.True(t, capturedLoading, "Loading must be true during the fetch")
		assert.True(t, pagePtr.State().Charts["usd-eur"].Loaded, "Loaded must be true after fetch")
	})

	t.Run("second call replaces prior state", func(t *testing.T) {
		f := &chartFakeFetcher{
			jsonResponse: meSubsResponse(sampleItems(), 1, 1, 10),
			chartErr:     errors.New("first error"),
		}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		require.NoError(t, page.LoadInitial(t.Context()))
		gen := page.SnapshotGeneration()
		err := page.LoadChart(t.Context(), "usd-eur", gen)
		require.Error(t, err)
		assert.NotNil(t, page.State().Charts["usd-eur"].Error)

		// Second call with success — same gen (no page/period change).
		f.chartErr = nil
		f.chartResponse = chartResponse(chartPoints(1.0))
		require.NoError(t, page.LoadChart(t.Context(), "usd-eur", gen))
		st := page.State().Charts["usd-eur"]
		assert.True(t, st.Loaded)
		assert.NoError(t, st.Error)
	})

	t.Run("stale generation does not mutate state and returns nil", func(t *testing.T) {
		f := &chartFakeFetcher{
			jsonResponse:  meSubsResponse(sampleItems(), 1, 1, 10),
			chartResponse: chartResponse(chartPoints(1.0)),
		}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		require.NoError(t, page.LoadInitial(t.Context()))
		staleGen := page.SnapshotGeneration() - 1
		// Passing a stale gen must be a no-op.
		err := page.LoadChart(t.Context(), "usd-eur", staleGen)
		require.NoError(t, err)
		// Charts should remain untouched (empty after LoadInitial clears them).
		_, found := page.State().Charts["usd-eur"]
		assert.False(t, found, "stale gen must not write into Charts")
	})
}

func TestMeSubscriptionsPage_ToggleExpand(t *testing.T) {
	t.Parallel()

	t.Run("absent key sets to true", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{jsonResponse: meSubsResponse(nil, 0, 1, 10)}
		page := newMePage(f, "tok")
		page.ToggleExpand("usd-eur")
		assert.True(t, page.State().Expanded["usd-eur"])
	})

	t.Run("true flips to false", func(t *testing.T) {
		t.Parallel()
		f := &meSubsFakeFetcher{jsonResponse: meSubsResponse(nil, 0, 1, 10)}
		page := newMePage(f, "tok")
		page.ToggleExpand("usd-eur")
		page.ToggleExpand("usd-eur")
		assert.False(t, page.State().Expanded["usd-eur"])
	})
}

func TestMeSubscriptionsPage_NextPage_ClearsCharts(t *testing.T) {
	t.Parallel()

	t.Run("NextPage clears Charts and Expanded", func(t *testing.T) {
		t.Parallel()
		f := &chartFakeFetcher{
			jsonResponse:  meSubsResponse(sampleItems(), 30, 2, 10),
			chartResponse: chartResponse(chartPoints(1.0)),
		}
		page := application.NewMeSubscriptionsPage(apiclient.New(f), "tok", 10)
		gen := page.SnapshotGeneration()
		// Seed a chart entry (without LoadInitial so we can set gen directly).
		// Use LoadInitial first to get items, then LoadChart.
		f.jsonResponse = meSubsResponse(sampleItems(), 30, 1, 10)
		require.NoError(t, page.LoadInitial(t.Context()))
		gen = page.SnapshotGeneration()
		require.NoError(t, page.LoadChart(t.Context(), "usd-eur", gen))
		page.ToggleExpand("usd-eur")
		assert.NotEmpty(t, page.State().Charts)
		assert.NotEmpty(t, page.State().Expanded)

		f.jsonResponse = meSubsResponse(sampleItems(), 30, 2, 10)
		require.NoError(t, page.NextPage(t.Context()))
		assert.Empty(t, page.State().Charts, "NextPage must clear Charts")
		assert.Empty(t, page.State().Expanded, "NextPage must clear Expanded")
	})
}
