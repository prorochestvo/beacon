package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/internal/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/pkg/api"
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

func meSubsResponse(items []api.MeSubscriptionRow, total int64, page, pageSize int) []byte {
	resp := api.MeSubscriptionsResponse{
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

func sampleItems() []api.MeSubscriptionRow {
	return []api.MeSubscriptionRow{
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
