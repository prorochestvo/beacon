package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// publicFetcher is an in-memory Fetcher for public-subscriptions tests.
// It routes FetchJSON calls by URL prefix, falling back to jsonResponse/jsonErr.
type publicFetcher struct {
	jsonResponse []byte
	jsonErr      error
	lastURL      string
	// urlResponses maps a URL prefix to the raw JSON body.
	urlResponses map[string][]byte
	// urlErr maps a URL prefix to an error.
	urlErr map[string]error
}

var _ apiclient.Fetcher = (*publicFetcher)(nil)

func (f *publicFetcher) FetchJSON(_ context.Context, _, rawURL string, _ any, _ map[string]string) ([]byte, error) {
	f.lastURL = rawURL
	for prefix, body := range f.urlResponses {
		if strings.HasPrefix(rawURL, prefix) {
			return body, nil
		}
	}
	for prefix, err := range f.urlErr {
		if strings.HasPrefix(rawURL, prefix) {
			return nil, err
		}
	}
	if f.jsonErr != nil {
		return nil, f.jsonErr
	}
	return f.jsonResponse, nil
}

func (f *publicFetcher) FetchNoContent(_ context.Context, _, _ string, _ any, _ map[string]string) error {
	return nil
}

// publicChartBody encodes a PublicChartResponse to JSON.
func publicChartBody(window string, page, limit int, total int64, pairs []dto.MeChartPairRow) []byte {
	resp := dto.PublicChartResponse{
		Window: window,
		Page:   page,
		Limit:  limit,
		Total:  total,
		Pairs:  pairs,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

// samplePublicPairs returns two distinct chart rows for reuse across tests.
func samplePublicPairs() []dto.MeChartPairRow {
	return []dto.MeChartPairRow{
		{
			Pair:   "USD/KZT",
			Series: []dto.MeChartSeries{{Kind: "BID", Color: "#1D9E75", Latest: 449.5}},
		},
		{
			Pair:   "EUR/KZT",
			Series: []dto.MeChartSeries{{Kind: "BID", Color: "#378ADD", Latest: 530.0}},
		},
	}
}

func newPublicPage(f *publicFetcher) *application.PublicSubscriptionsPage {
	return application.NewPublicSubscriptionsPage(apiclient.New(f))
}

func TestPublicSubscriptionsPage_LoadPage(t *testing.T) {
	t.Parallel()

	t.Run("happy path stores chart page and total", func(t *testing.T) {
		t.Parallel()
		pairs := samplePublicPairs()
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 1, 20, int64(len(pairs)), pairs),
		}
		page := newPublicPage(f)
		err := page.LoadPage(t.Context(), 1)
		require.NoError(t, err)

		st := page.State()
		require.NotNil(t, st.Chart)
		assert.Equal(t, 1, st.Page)
		assert.Equal(t, int64(len(pairs)), st.Total)
		assert.Len(t, st.Chart.Pairs, len(pairs))
		assert.NoError(t, st.ChartError)
		assert.False(t, st.ChartLoading)
	})

	t.Run("error sets ChartError and returns error", func(t *testing.T) {
		t.Parallel()
		f := &publicFetcher{jsonErr: errors.New("http 503")}
		page := newPublicPage(f)
		err := page.LoadPage(t.Context(), 1)
		require.Error(t, err)

		st := page.State()
		require.Error(t, st.ChartError)
		assert.ErrorContains(t, st.ChartError, "http 503")
		assert.False(t, st.ChartLoading, "ChartLoading must be false after error")
		assert.Nil(t, st.Chart, "Chart must remain nil on error")
	})

	t.Run("second-page load updates Page field", func(t *testing.T) {
		t.Parallel()
		pairs := samplePublicPairs()
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 2, 20, 40, pairs),
		}
		page := newPublicPage(f)
		// Load page 1 first.
		f.jsonResponse = publicChartBody("7 days", 1, 20, 40, pairs)
		require.NoError(t, page.LoadPage(t.Context(), 1))
		assert.Equal(t, 1, page.State().Page)

		// Now load page 2.
		f.jsonResponse = publicChartBody("7 days", 2, 20, 40, pairs)
		require.NoError(t, page.LoadPage(t.Context(), 2))
		assert.Equal(t, 2, page.State().Page)
	})

	t.Run("page less than 1 is normalised to 1", func(t *testing.T) {
		t.Parallel()
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 1, 20, 0, nil),
		}
		page := newPublicPage(f)
		require.NoError(t, page.LoadPage(t.Context(), -3))
		assert.Equal(t, 1, page.State().Page)
	})

	t.Run("ChartLoading is false both before and after successful load", func(t *testing.T) {
		t.Parallel()
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 1, 20, 0, nil),
		}
		page := newPublicPage(f)
		assert.False(t, page.State().ChartLoading)
		require.NoError(t, page.LoadPage(t.Context(), 1))
		assert.False(t, page.State().ChartLoading)
	})

	t.Run("auto-closes modal when refreshed chart no longer contains OpenPair", func(t *testing.T) {
		t.Parallel()
		// Initial load — USD/KZT is present.
		initialPairs := []dto.MeChartPairRow{
			{Pair: "USD/KZT", Series: []dto.MeChartSeries{{Kind: "BID", Color: "#1D9E75", Latest: 449.5}}},
		}
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 1, 20, 1, initialPairs),
		}
		page := newPublicPage(f)
		require.NoError(t, page.LoadPage(t.Context(), 1))
		page.OpenPairModal("USD/KZT")
		require.NotNil(t, page.State().OpenPair)

		// Reload — USD/KZT is gone.
		newPairs := []dto.MeChartPairRow{
			{Pair: "EUR/KZT", Series: []dto.MeChartSeries{{Kind: "BID", Color: "#378ADD", Latest: 530.0}}},
		}
		f.jsonResponse = publicChartBody("7 days", 1, 20, 1, newPairs)
		require.NoError(t, page.LoadPage(t.Context(), 1))
		assert.Nil(t, page.State().OpenPair, "OpenPair must be cleared when pair is no longer in the chart")
	})

	t.Run("modal stays open when reloaded chart still contains OpenPair", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			{Pair: "USD/KZT", Series: []dto.MeChartSeries{{Kind: "BID", Color: "#1D9E75", Latest: 449.5}}},
		}
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 1, 20, 1, pairs),
		}
		page := newPublicPage(f)
		require.NoError(t, page.LoadPage(t.Context(), 1))
		page.OpenPairModal("USD/KZT")
		require.NoError(t, page.LoadPage(t.Context(), 1))
		require.NotNil(t, page.State().OpenPair)
		assert.Equal(t, "USD/KZT", *page.State().OpenPair)
	})

	t.Run("context.Canceled propagates as error", func(t *testing.T) {
		t.Parallel()
		f := &publicFetcher{jsonErr: context.Canceled}
		page := newPublicPage(f)
		err := page.LoadPage(t.Context(), 1)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestPublicSubscriptionsPage_OpenPairModal(t *testing.T) {
	t.Parallel()

	t.Run("stores pair label in OpenPair", func(t *testing.T) {
		t.Parallel()
		page := newPublicPage(&publicFetcher{})
		page.OpenPairModal("USD/KZT")
		st := page.State()
		require.NotNil(t, st.OpenPair)
		assert.Equal(t, "USD/KZT", *st.OpenPair)
	})

	t.Run("subsequent call overwrites previous pair", func(t *testing.T) {
		t.Parallel()
		page := newPublicPage(&publicFetcher{})
		page.OpenPairModal("USD/KZT")
		page.OpenPairModal("EUR/KZT")
		st := page.State()
		require.NotNil(t, st.OpenPair)
		assert.Equal(t, "EUR/KZT", *st.OpenPair)
	})
}

func TestPublicSubscriptionsPage_ClosePairModal(t *testing.T) {
	t.Parallel()

	t.Run("clears OpenPair when previously set", func(t *testing.T) {
		t.Parallel()
		page := newPublicPage(&publicFetcher{})
		page.OpenPairModal("USD/KZT")
		page.ClosePairModal()
		assert.Nil(t, page.State().OpenPair)
	})

	t.Run("no-op when OpenPair is already nil", func(t *testing.T) {
		t.Parallel()
		page := newPublicPage(&publicFetcher{})
		page.ClosePairModal()
		assert.Nil(t, page.State().OpenPair)
	})
}

func TestPublicSubscriptionsPage_SetPeriod(t *testing.T) {
	t.Parallel()

	t.Run("valid period is stored and chart is reloaded", func(t *testing.T) {
		t.Parallel()
		pairs := samplePublicPairs()
		f := &publicFetcher{
			jsonResponse: publicChartBody("30 days", 1, 20, int64(len(pairs)), pairs),
		}
		page := newPublicPage(f)
		err := page.SetPeriod(t.Context(), 30)
		require.NoError(t, err)
		assert.Equal(t, 30, page.State().Period)
		assert.Contains(t, f.lastURL, "period=30", "period must appear in the request URL")
	})

	t.Run("default period is 7 in a freshly constructed page", func(t *testing.T) {
		t.Parallel()
		page := newPublicPage(&publicFetcher{})
		assert.Equal(t, 7, page.State().Period)
	})

	t.Run("invalid period is clamped to default and chart is reloaded", func(t *testing.T) {
		t.Parallel()
		pairs := samplePublicPairs()
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 1, 20, int64(len(pairs)), pairs),
		}
		page := newPublicPage(f)
		err := page.SetPeriod(t.Context(), 999)
		require.NoError(t, err)
		assert.Equal(t, 7, page.State().Period, "invalid period must be clamped to PublicChartDefaultPeriod")
		assert.Contains(t, f.lastURL, "period=7")
	})

	t.Run("each AllowedChartPeriods value is accepted", func(t *testing.T) {
		t.Parallel()
		for _, period := range application.AllowedChartPeriods {
			period := period
			t.Run("", func(t *testing.T) {
				t.Parallel()
				pairs := samplePublicPairs()
				f := &publicFetcher{
					jsonResponse: publicChartBody("n days", 1, 20, int64(len(pairs)), pairs),
				}
				page := newPublicPage(f)
				require.NoError(t, page.SetPeriod(t.Context(), period))
				assert.Equal(t, period, page.State().Period)
			})
		}
	})

	t.Run("fetch error is propagated and period is still updated", func(t *testing.T) {
		t.Parallel()
		f := &publicFetcher{jsonErr: errors.New("http 503")}
		page := newPublicPage(f)
		err := page.SetPeriod(t.Context(), 90)
		require.Error(t, err)
		assert.Equal(t, 90, page.State().Period, "period must be set even when the fetch fails")
	})

	t.Run("SetPeriod resets to page 1", func(t *testing.T) {
		t.Parallel()
		pairs := samplePublicPairs()
		f := &publicFetcher{
			jsonResponse: publicChartBody("7 days", 2, 20, 40, pairs),
		}
		page := newPublicPage(f)
		// Seed state so Page appears to be 2.
		require.NoError(t, page.LoadPage(t.Context(), 2))
		assert.Equal(t, 2, page.State().Page)

		f.jsonResponse = publicChartBody("30 days", 1, 20, 40, pairs)
		require.NoError(t, page.SetPeriod(t.Context(), 30))
		assert.Equal(t, 1, page.State().Page, "SetPeriod must reload from page 1")
	})

	t.Run("stale chart fetch is dropped when period changes mid-flight", func(t *testing.T) {
		// The gatedFetcher blocks the first FetchJSON call (the period=30 chart
		// fetch) until the test releases it. While it is blocked, the test calls
		// SetPeriod(7) — which completes immediately using the fallback response.
		// When the blocked (stale) call is released it finds p.state.Period == 7
		// != 30 and drops the result, so Chart remains as set by the period=7 fetch.
		t.Parallel()

		pairs7 := []dto.MeChartPairRow{
			{Pair: "USD/KZT", Series: []dto.MeChartSeries{{Kind: "BID", Color: "#1D9E75", Latest: 449.5}}},
		}
		pairs30 := []dto.MeChartPairRow{
			{Pair: "EUR/KZT", Series: []dto.MeChartSeries{{Kind: "BID", Color: "#378ADD", Latest: 530.0}}},
		}
		// First call (period=30) → stale; second call (period=7) → current.
		bf := newGatedFetcher(
			publicChartBody("30 days", 1, 20, 1, pairs30),
			publicChartBody("7 days", 1, 20, 1, pairs7),
		)
		page := application.NewPublicSubscriptionsPage(apiclient.New(bf))

		// Launch the period=30 fetch; it blocks inside gatedFetcher.
		done := make(chan error, 1)
		go func() {
			done <- page.SetPeriod(t.Context(), 30)
		}()

		// Wait until the fetch is blocked, then switch to period=7 while the
		// period=30 request is still in flight.
		<-bf.started
		require.NoError(t, page.SetPeriod(t.Context(), 7))

		// period=7 fetch already completed; Chart reflects pairs7.
		st := page.State()
		require.NotNil(t, st.Chart, "chart from period=7 fetch must be stored")
		assert.Equal(t, 7, st.Period)
		require.Len(t, st.Chart.Pairs, 1)
		assert.Equal(t, "USD/KZT", st.Chart.Pairs[0].Pair, "chart must reflect the period=7 result")

		// Release the stale period=30 fetch; it must NOT overwrite the state.
		bf.release <- struct{}{}
		require.NoError(t, <-done)

		st = page.State()
		assert.Equal(t, 7, st.Period, "period must remain 7 after stale fetch is dropped")
		require.NotNil(t, st.Chart)
		require.Len(t, st.Chart.Pairs, 1)
		assert.Equal(t, "USD/KZT", st.Chart.Pairs[0].Pair, "stale period=30 result must not overwrite Chart")
	})
}

func TestFindPairInPublicChart(t *testing.T) {
	t.Parallel()

	chart := &dto.PublicChartResponse{
		Pairs: []dto.MeChartPairRow{
			{Pair: "USD/KZT"},
			{Pair: "EUR/KZT"},
		},
	}

	t.Run("nil chart returns false", func(t *testing.T) {
		t.Parallel()
		assert.False(t, application.FindPairInPublicChart(nil, "USD/KZT"))
	})

	t.Run("empty pairs returns false", func(t *testing.T) {
		t.Parallel()
		assert.False(t, application.FindPairInPublicChart(&dto.PublicChartResponse{}, "USD/KZT"))
	})

	t.Run("pair present returns true", func(t *testing.T) {
		t.Parallel()
		assert.True(t, application.FindPairInPublicChart(chart, "USD/KZT"))
	})

	t.Run("pair absent returns false", func(t *testing.T) {
		t.Parallel()
		assert.False(t, application.FindPairInPublicChart(chart, "GBP/KZT"))
	})
}
