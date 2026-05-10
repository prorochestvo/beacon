package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/internal/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

var _ apiclient.Fetcher = (*fakeFetcher)(nil)

type fakeFetcher struct {
	jsonResponse    []byte
	jsonErr         error
	noContentErr    error
	noContentCalled int
}

func (f *fakeFetcher) FetchJSON(_ context.Context, _, _ string, _ any, _ map[string]string) ([]byte, error) {
	if f.jsonErr != nil {
		return nil, f.jsonErr
	}
	return f.jsonResponse, nil
}

func (f *fakeFetcher) FetchNoContent(_ context.Context, _, _ string, _ any, _ map[string]string) error {
	f.noContentCalled++
	return f.noContentErr
}

func sourcesFixture() []api.SourceResponse {
	return []api.SourceResponse{
		{Name: "usd-eur", Title: "USD/EUR", BaseCurrency: "USD", QuoteCurrency: "EUR", Active: true, LastSuccess: true, LastRunAt: "2026-01-03T00:00:00Z"},
		{Name: "gbp-usd", Title: "GBP/USD", BaseCurrency: "GBP", QuoteCurrency: "USD", Active: false, LastSuccess: false, LastRunAt: "2026-01-01T00:00:00Z"},
		{Name: "jpy-eur", Title: "Yen Euro", BaseCurrency: "JPY", QuoteCurrency: "EUR", Active: true, LastSuccess: true, LastRunAt: "2026-01-02T00:00:00Z"},
	}
}

func newPage(sources []api.SourceResponse) *application.SourcesPage {
	f := &fakeFetcher{}
	c := apiclient.New(f)
	return application.NewSourcesPage(sources, api.StatsResponse{}, c)
}

func TestSourcesPage_OnFilterTitle(t *testing.T) {
	t.Parallel()

	t.Run("matches on title substring case-insensitive", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterTitle("usd")
		visible := state.Visible()
		require.Len(t, visible, 2)
		names := []string{visible[0].Name, visible[1].Name}
		assert.Contains(t, names, "usd-eur")
		assert.Contains(t, names, "gbp-usd")
	})

	t.Run("matches on name substring case-insensitive", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterTitle("jpy")
		visible := state.Visible()
		require.Len(t, visible, 1)
		assert.Equal(t, "jpy-eur", visible[0].Name)
	})

	t.Run("case insensitive title match", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterTitle("YEN")
		visible := state.Visible()
		require.Len(t, visible, 1)
		assert.Equal(t, "jpy-eur", visible[0].Name)
	})

	t.Run("empty filter returns all sources", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterTitle("")
		assert.Len(t, state.Visible(), 3)
	})

	t.Run("no match returns empty slice", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterTitle("XYZ")
		assert.Empty(t, state.Visible())
	})
}

func TestSourcesPage_OnFilterPair(t *testing.T) {
	t.Parallel()

	t.Run("exact pair match", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterPair("USD/EUR")
		visible := state.Visible()
		require.Len(t, visible, 1)
		assert.Equal(t, "usd-eur", visible[0].Name)
	})

	t.Run("partial pair match", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterPair("eur")
		visible := state.Visible()
		require.Len(t, visible, 2)
	})

	t.Run("case insensitive pair filter", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterPair("gbp")
		visible := state.Visible()
		require.Len(t, visible, 1)
		assert.Equal(t, "gbp-usd", visible[0].Name)
	})

	t.Run("empty filter returns all", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterPair("")
		assert.Len(t, state.Visible(), 3)
	})
}

func TestSourcesPage_OnFilterStatus(t *testing.T) {
	t.Parallel()

	t.Run("ok keeps only last_success=true rows", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterStatus("ok")
		visible := state.Visible()
		for _, src := range visible {
			assert.True(t, src.LastSuccess, "expected all visible sources to have last_success=true")
		}
		assert.Len(t, visible, 2)
	})

	t.Run("error keeps only last_success=false rows", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterStatus("error")
		visible := state.Visible()
		for _, src := range visible {
			assert.False(t, src.LastSuccess, "expected all visible sources to have last_success=false")
		}
		assert.Len(t, visible, 1)
	})

	t.Run("all keeps everything", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterStatus("all")
		assert.Len(t, state.Visible(), 3)
	})
}

func TestSourcesPage_OnFilterActive(t *testing.T) {
	t.Parallel()

	t.Run("yes keeps only active sources", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterActive("yes")
		visible := state.Visible()
		for _, src := range visible {
			assert.True(t, src.Active)
		}
		assert.Len(t, visible, 2)
	})

	t.Run("no keeps only inactive sources", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterActive("no")
		visible := state.Visible()
		for _, src := range visible {
			assert.False(t, src.Active)
		}
		assert.Len(t, visible, 1)
	})

	t.Run("all keeps everything", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.OnFilterActive("all")
		assert.Len(t, state.Visible(), 3)
	})
}

func TestSourcesPage_ToggleSort(t *testing.T) {
	t.Parallel()

	t.Run("default state is descending", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		assert.True(t, p.State().SortDesc)
	})

	t.Run("ToggleSort flips descending to ascending", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		state := p.ToggleSort()
		assert.False(t, state.SortDesc)
	})

	t.Run("ToggleSort flips ascending back to descending", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		p.ToggleSort()
		state := p.ToggleSort()
		assert.True(t, state.SortDesc)
	})

	t.Run("desc sorts newest first", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		visible := p.State().Visible()
		require.Len(t, visible, 3)
		assert.Equal(t, "usd-eur", visible[0].Name)
		assert.Equal(t, "jpy-eur", visible[1].Name)
		assert.Equal(t, "gbp-usd", visible[2].Name)
	})

	t.Run("asc sorts oldest first", func(t *testing.T) {
		t.Parallel()
		p := newPage(sourcesFixture())
		p.ToggleSort()
		visible := p.State().Visible()
		require.Len(t, visible, 3)
		assert.Equal(t, "gbp-usd", visible[0].Name)
	})
}

func TestSourcesPage_ToggleActive(t *testing.T) {
	t.Parallel()

	updatedSources := []api.SourceResponse{
		{Name: "usd-eur", Title: "USD/EUR", BaseCurrency: "USD", QuoteCurrency: "EUR", Active: false, LastSuccess: true},
	}
	updatedJSON, err := json.Marshal(updatedSources)
	require.NoError(t, err)

	t.Run("calls SetSourceActive and refreshes list on success", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: updatedJSON}
		c := apiclient.New(f)
		sources := sourcesFixture()
		p := application.NewSourcesPage(sources, api.StatsResponse{}, c)
		state, err := p.ToggleActive(t.Context(), "usd-eur", false)
		require.NoError(t, err)
		assert.Equal(t, 1, f.noContentCalled, "SetSourceActive must issue exactly one PATCH")
		require.Len(t, state.All, 1)
		assert.Equal(t, "usd-eur", state.All[0].Name)
		assert.False(t, state.All[0].Active)
	})

	t.Run("SetSourceActive error propagates without changing state", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{noContentErr: errors.New("http 500")}
		c := apiclient.New(f)
		sources := sourcesFixture()
		p := application.NewSourcesPage(sources, api.StatsResponse{}, c)
		_, err := p.ToggleActive(t.Context(), "usd-eur", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "http 500")
		assert.Equal(t, 1, f.noContentCalled, "PATCH must be attempted exactly once before returning the error")
		assert.Len(t, p.State().All, 3, "source list must be unchanged after a PATCH failure")
	})

	t.Run("ListSources error after patch propagates", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("fetch failed")}
		c := apiclient.New(f)
		sources := sourcesFixture()
		p := application.NewSourcesPage(sources, api.StatsResponse{}, c)
		_, err := p.ToggleActive(t.Context(), "usd-eur", false)
		require.Error(t, err)
		assert.Equal(t, 1, f.noContentCalled, "PATCH must be attempted even when the subsequent list fetch fails")
		assert.Len(t, p.State().All, 3, "source list must be unchanged when the re-fetch fails")
	})
}
