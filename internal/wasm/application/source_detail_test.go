package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/internal/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

func ratesFixture() []api.RateResponse {
	return []api.RateResponse{
		{ID: "1", BaseCurrency: "USD", QuoteCurrency: "EUR", Price: 1.1, Timestamp: "2026-01-03T00:00:00Z"},
		{ID: "2", BaseCurrency: "GBP", QuoteCurrency: "USD", Price: 1.25, Timestamp: "2026-01-01T00:00:00Z"},
		{ID: "3", BaseCurrency: "USD", QuoteCurrency: "JPY", Price: 150.0, Timestamp: "2026-01-02T00:00:00Z"},
		{ID: "4", BaseCurrency: "EUR", QuoteCurrency: "GBP", Price: 0.85, Timestamp: ""},
	}
}

func newDetailPage(name string, sources []api.SourceResponse, rates []api.RateResponse, f *fakeFetcher) *application.SourceDetailPage {
	c := apiclient.New(f)
	return application.NewSourceDetailPage(name, sources, rates, c)
}

func TestSourceDetailPage_OnRateFilter(t *testing.T) {
	t.Parallel()

	t.Run("filters case-insensitive substring on base/quote", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		state := p.OnRateFilter("usd")
		visible := state.VisibleRates()
		// USD/EUR, GBP/USD (USD in quote), USD/JPY all contain "usd"
		require.Len(t, visible, 3, "USD/EUR, GBP/USD, and USD/JPY should all match 'usd'")
		for _, r := range visible {
			pair := strings.ToLower(r.BaseCurrency + "/" + r.QuoteCurrency)
			assert.Contains(t, pair, "usd")
		}
	})

	t.Run("upper case filter matches lower case currencies", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		state := p.OnRateFilter("GBP")
		visible := state.VisibleRates()
		require.Len(t, visible, 2, "GBP/USD and EUR/GBP should match")
	})

	t.Run("empty filter returns all rates", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		state := p.OnRateFilter("")
		assert.Len(t, state.VisibleRates(), len(ratesFixture()))
	})

	t.Run("no match returns empty slice", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		state := p.OnRateFilter("XYZ")
		assert.Empty(t, state.VisibleRates())
	})
}

func TestSourceDetailPage_ToggleRateSort(t *testing.T) {
	t.Parallel()

	t.Run("default sort is descending", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		assert.True(t, p.State().RateSortDesc)
	})

	t.Run("desc places newest timestamp first", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		visible := p.State().VisibleRates()
		require.Len(t, visible, len(ratesFixture()))
		assert.Equal(t, "1", visible[0].ID, "2026-01-03 is newest")
	})

	t.Run("zero timestamp sorts last in desc", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		visible := p.State().VisibleRates()
		assert.Equal(t, "4", visible[len(visible)-1].ID, "empty timestamp should be last")
	})

	t.Run("toggle flips to ascending", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		state := p.ToggleRateSort()
		assert.False(t, state.RateSortDesc)
	})

	t.Run("asc places zero-timestamp first then oldest real timestamp", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		p.ToggleRateSort()
		visible := p.State().VisibleRates()
		require.Len(t, visible, len(ratesFixture()))
		// time.Time{} (zero) is before all real timestamps, so empty Timestamp sorts first in asc.
		assert.Equal(t, "4", visible[0].ID, "empty timestamp (zero time) sorts first in asc")
		assert.Equal(t, "1", visible[len(visible)-1].ID, "2026-01-03 is newest so sorts last in asc")
	})

	t.Run("toggle twice returns to desc", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("src", nil, ratesFixture(), &fakeFetcher{})
		p.ToggleRateSort()
		state := p.ToggleRateSort()
		assert.True(t, state.RateSortDesc)
	})
}

func TestSourceDetailPage_LoadSubsPage(t *testing.T) {
	t.Parallel()

	subsData := []api.SubscriptionDetailResponse{
		{ID: "a", UserType: "telegram", SourceName: "src", Condition: "price > 100"},
		{ID: "b", UserType: "telegram", SourceName: "src", Condition: "price < 50"},
	}
	subsJSON, err := json.Marshal(subsData)
	require.NoError(t, err)

	t.Run("happy path fetches correct page and replaces slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: subsJSON}
		p := newDetailPage("src", nil, nil, f)
		err := p.LoadSubsPage(context.Background(), 2)
		require.NoError(t, err)
		state := p.State()
		assert.Equal(t, 2, state.SubsPage)
		require.Len(t, state.Subs, 2)
		assert.Equal(t, "a", state.Subs[0].ID)
	})

	t.Run("fetch error propagates without changing state", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("network error")}
		p := newDetailPage("src", nil, nil, f)
		err := p.LoadSubsPage(context.Background(), 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network error")
		assert.Equal(t, 1, p.State().SubsPage, "page must remain at default on error")
		assert.Empty(t, p.State().Subs)
	})

	t.Run("issues exactly one FetchJSON call per LoadSubsPage", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcherCounted{jsonResponse: subsJSON}
		c := apiclient.New(f)
		p := application.NewSourceDetailPage("src", nil, nil, c)
		require.NoError(t, p.LoadSubsPage(context.Background(), 1))
		assert.Equal(t, 1, f.fetchJSONCalled)
	})

	t.Run("page 1 prev disabled - count equals limit", func(t *testing.T) {
		t.Parallel()
		items := make([]api.SubscriptionDetailResponse, application.SubsLimit)
		data, err := json.Marshal(items)
		require.NoError(t, err)
		f := &fakeFetcher{jsonResponse: data}
		p := newDetailPage("src", nil, nil, f)
		require.NoError(t, p.LoadSubsPage(context.Background(), 1))
		state := p.State()
		assert.Equal(t, 1, state.SubsPage)
		assert.Len(t, state.Subs, application.SubsLimit)
		assert.False(t, state.SubsPage > 1, "page 1: prev must be disabled")
	})

	t.Run("last page next disabled - count less than limit", func(t *testing.T) {
		t.Parallel()
		items := make([]api.SubscriptionDetailResponse, application.SubsLimit-1)
		data, err := json.Marshal(items)
		require.NoError(t, err)
		f := &fakeFetcher{jsonResponse: data}
		p := newDetailPage("src", nil, nil, f)
		require.NoError(t, p.LoadSubsPage(context.Background(), 3))
		state := p.State()
		assert.Less(t, len(state.Subs), application.SubsLimit, "count < limit means no next page")
	})
}

func TestSourceDetailPage_LoadDailyEventsPage(t *testing.T) {
	t.Parallel()

	eventsData := []api.DailyEventResponse{
		{Type: "rate", Date: "2026-01-01", SuccessCount: 10, FailedCount: 0},
	}
	eventsJSON, err := json.Marshal(eventsData)
	require.NoError(t, err)

	t.Run("happy path fetches correct page and replaces slice", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: eventsJSON}
		p := newDetailPage("src", nil, nil, f)
		err := p.LoadDailyEventsPage(context.Background(), 2)
		require.NoError(t, err)
		state := p.State()
		assert.Equal(t, 2, state.DailyEventsPage)
		require.Len(t, state.DailyEvents, 1)
		assert.Equal(t, "rate", state.DailyEvents[0].Type)
	})

	t.Run("fetch error propagates without changing state", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("timeout")}
		p := newDetailPage("src", nil, nil, f)
		err := p.LoadDailyEventsPage(context.Background(), 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout")
		assert.Equal(t, 1, p.State().DailyEventsPage)
		assert.Empty(t, p.State().DailyEvents)
	})

	t.Run("issues exactly one FetchJSON call per LoadDailyEventsPage", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcherCounted{jsonResponse: eventsJSON}
		c := apiclient.New(f)
		p := application.NewSourceDetailPage("src", nil, nil, c)
		require.NoError(t, p.LoadDailyEventsPage(context.Background(), 1))
		assert.Equal(t, 1, f.fetchJSONCalled)
	})

	t.Run("page 1 prev disabled - count equals limit", func(t *testing.T) {
		t.Parallel()
		items := make([]api.DailyEventResponse, application.DailyEventsLimit)
		data, err := json.Marshal(items)
		require.NoError(t, err)
		f := &fakeFetcher{jsonResponse: data}
		p := newDetailPage("src", nil, nil, f)
		require.NoError(t, p.LoadDailyEventsPage(context.Background(), 1))
		state := p.State()
		assert.False(t, state.DailyEventsPage > 1, "page 1: prev must be disabled")
		assert.Len(t, state.DailyEvents, application.DailyEventsLimit)
	})

	t.Run("last page next disabled - count less than limit", func(t *testing.T) {
		t.Parallel()
		items := make([]api.DailyEventResponse, application.DailyEventsLimit-5)
		data, err := json.Marshal(items)
		require.NoError(t, err)
		f := &fakeFetcher{jsonResponse: data}
		p := newDetailPage("src", nil, nil, f)
		require.NoError(t, p.LoadDailyEventsPage(context.Background(), 4))
		state := p.State()
		assert.Less(t, len(state.DailyEvents), application.DailyEventsLimit)
	})
}

func TestNewSourceDetailPage(t *testing.T) {
	t.Parallel()

	sources := []api.SourceResponse{
		{Name: "usd-eur", Title: "USD to EUR"},
		{Name: "gbp-usd", Title: ""},
	}

	t.Run("uses Title when present in sources list", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("usd-eur", sources, nil, &fakeFetcher{})
		assert.Equal(t, "USD to EUR", p.State().Title)
	})

	t.Run("falls back to Name when Title is empty", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("gbp-usd", sources, nil, &fakeFetcher{})
		assert.Equal(t, "gbp-usd", p.State().Title)
	})

	t.Run("falls back to Name when source not in list", func(t *testing.T) {
		t.Parallel()
		p := newDetailPage("jpy-eur", sources, nil, &fakeFetcher{})
		assert.Equal(t, "jpy-eur", p.State().Title)
	})
}

// fakeFetcherCounted is a separate fake that counts FetchJSON calls, used to
// assert exactly one network call is issued per Load* invocation.
type fakeFetcherCounted struct {
	jsonResponse    []byte
	fetchJSONCalled int
}

var _ apiclient.Fetcher = (*fakeFetcherCounted)(nil)

func (f *fakeFetcherCounted) FetchJSON(_ context.Context, _, _ string, _ any, _ map[string]string) ([]byte, error) {
	f.fetchJSONCalled++
	return f.jsonResponse, nil
}

func (f *fakeFetcherCounted) FetchNoContent(_ context.Context, _, _ string, _ any, _ map[string]string) error {
	return nil
}
