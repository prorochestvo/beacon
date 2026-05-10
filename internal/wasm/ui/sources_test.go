package ui_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/wasm/ui"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

func makeState(sources []api.SourceResponse) application.SourcesState {
	return application.SourcesState{
		All:          sources,
		FilterStatus: "all",
		FilterActive: "all",
		SortDesc:     true,
	}
}

func TestRenderSources(t *testing.T) {
	t.Parallel()

	t.Run("empty state renders no-sources-match message", func(t *testing.T) {
		t.Parallel()
		state := makeState(nil)
		html := ui.RenderSources(state)
		assert.Contains(t, html, "No sources match the current filters.")
	})

	t.Run("one row OK status active true", func(t *testing.T) {
		t.Parallel()
		sources := []api.SourceResponse{
			{
				Name:          "usd-eur",
				Title:         "USD/EUR",
				BaseCurrency:  "USD",
				QuoteCurrency: "EUR",
				Interval:      "1h",
				Active:        true,
				LastSuccess:   true,
				LastRunAt:     "2026-01-01T00:00:00Z",
			},
		}
		state := makeState(sources)
		html := ui.RenderSources(state)
		assert.Contains(t, html, "USD/EUR")
		assert.Contains(t, html, "USD")
		assert.Contains(t, html, "EUR")
		assert.Contains(t, html, "1h")
		assert.Contains(t, html, `class="ok"`)
		assert.Contains(t, html, "active-on")
		assert.NotContains(t, html, "active-off")
	})

	t.Run("one row error status with last_error", func(t *testing.T) {
		t.Parallel()
		sources := []api.SourceResponse{
			{
				Name:          "gbp-usd",
				Title:         "GBP/USD",
				BaseCurrency:  "GBP",
				QuoteCurrency: "USD",
				Interval:      "30m",
				Active:        false,
				LastSuccess:   false,
				LastError:     "connection refused",
			},
		}
		state := makeState(sources)
		html := ui.RenderSources(state)
		assert.Contains(t, html, "GBP/USD")
		assert.Contains(t, html, `class="err"`)
		assert.Contains(t, html, "connection refused")
		assert.Contains(t, html, "active-off")
		assert.NotContains(t, html, "active-on")
	})

	t.Run("XSS payload in name title and last_error is escaped", func(t *testing.T) {
		t.Parallel()
		sources := []api.SourceResponse{
			{
				Name:          `<script>alert(1)</script>`,
				Title:         `Foo & Bar "baz"`,
				BaseCurrency:  "USD",
				QuoteCurrency: "EUR",
				Interval:      "1h",
				Active:        false,
				LastSuccess:   false,
				LastError:     `<>&"`,
			},
		}
		state := makeState(sources)
		html := ui.RenderSources(state)

		assert.NotContains(t, html, "<script>", "raw <script> tag must not appear in output")
		assert.NotContains(t, html, "alert(1)</script>", "unescaped script content must not appear")

		assert.Contains(t, html, "&lt;script&gt;")
		assert.Contains(t, html, "&amp;")
		assert.Contains(t, html, "&lt;")
		assert.Contains(t, html, "&gt;")
		assert.Contains(t, html, "&quot;")

		assert.Contains(t, html, "Foo &amp; Bar &quot;baz&quot;")

		assert.Contains(t, html, "&lt;&gt;&amp;&quot;")
	})

	t.Run("sort arrow shows down when SortDesc true", func(t *testing.T) {
		t.Parallel()
		state := makeState([]api.SourceResponse{
			{Name: "a", BaseCurrency: "USD", QuoteCurrency: "EUR"},
		})
		state.SortDesc = true
		html := ui.RenderSourcesTable(state)
		assert.Contains(t, html, "↓")
		assert.NotContains(t, html, "↑")
	})

	t.Run("sort arrow shows up when SortDesc false", func(t *testing.T) {
		t.Parallel()
		state := makeState([]api.SourceResponse{
			{Name: "a", BaseCurrency: "USD", QuoteCurrency: "EUR"},
		})
		state.SortDesc = false
		html := ui.RenderSourcesTable(state)
		assert.Contains(t, html, "↑")
		assert.NotContains(t, html, "↓")
	})

	t.Run("stats line shows active count and total", func(t *testing.T) {
		t.Parallel()
		state := makeState(nil)
		state.Stats = api.StatsResponse{SourcesTotal: 10, SourcesActive: 7, ErrorsTotal: 0}
		html := ui.RenderSources(state)
		assert.Contains(t, html, "7/10 source(s) active")
		assert.NotContains(t, html, "error(s)")
	})

	t.Run("stats line includes error link when errors_total > 0", func(t *testing.T) {
		t.Parallel()
		state := makeState(nil)
		state.Stats = api.StatsResponse{SourcesTotal: 5, SourcesActive: 4, ErrorsTotal: 3}
		html := ui.RenderSources(state)
		assert.Contains(t, html, `href="#/errors"`)
		assert.Contains(t, html, "3 error(s)")
	})

	t.Run("active toggle button uses data-name and data-active attributes", func(t *testing.T) {
		t.Parallel()
		sources := []api.SourceResponse{
			{Name: "usd-eur", Title: "USD/EUR", BaseCurrency: "USD", QuoteCurrency: "EUR", Active: true, LastSuccess: true},
		}
		html := ui.RenderSources(makeState(sources))
		assert.Contains(t, html, `data-name="usd-eur"`)
		assert.Contains(t, html, `data-active="false"`)
	})
}

func TestRenderSourcesTable(t *testing.T) {
	t.Parallel()

	t.Run("column headers are present", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSourcesTable(makeState(nil))
		for _, hdr := range []string{"Title", "Pair", "Interval", "Last Run", "Status", "Active"} {
			assert.True(t, strings.Contains(html, hdr), "expected header %q in table", hdr)
		}
	})

	t.Run("source name with percent and slash is path-escaped in href", func(t *testing.T) {
		t.Parallel()
		sources := []api.SourceResponse{
			{
				Name:          "a%b/c",
				Title:         "A percent B slash C",
				BaseCurrency:  "USD",
				QuoteCurrency: "EUR",
				Interval:      "1h",
				Active:        true,
				LastSuccess:   true,
			},
		}
		html := ui.RenderSourcesTable(makeState(sources))
		// The href must use percent-encoded segments so decodeURIComponent in the
		// JS hash router round-trips correctly: % → %25, / → %2F.
		assert.Contains(t, html, `href="#/sources/a%25b%2Fc"`, "percent and slash must be path-escaped in href")
		// The visible link text must still be HTML-escaped (not path-escaped).
		assert.Contains(t, html, "A percent B slash C", "display text must be the title, not the escaped name")
	})
}
