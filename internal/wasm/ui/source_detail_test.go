package ui_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/wasm/ui"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

func makeDetailState(name, title string) application.SourceDetailState {
	return application.SourceDetailState{
		Name:            name,
		Title:           title,
		RateSortDesc:    true,
		SubsPage:        1,
		DailyEventsPage: 1,
	}
}

func TestRenderSourceDetail(t *testing.T) {
	t.Parallel()

	t.Run("renders breadcrumb back link", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "My Source")
		html := ui.RenderSourceDetail(state)
		assert.Contains(t, html, `href="#/"`)
		assert.Contains(t, html, "← All Sources")
	})

	t.Run("renders escaped title in h1", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", `<script>alert(1)</script>`)
		html := ui.RenderSourceDetail(state)
		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;script&gt;")
	})

	t.Run("contains stable div ids for async section updates", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		html := ui.RenderSourceDetail(state)
		assert.Contains(t, html, `id="rates-table"`)
		assert.Contains(t, html, `id="subs-section"`)
		assert.Contains(t, html, `id="daily-events-section"`)
	})

	t.Run("contains rate filter input", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		html := ui.RenderSourceDetail(state)
		assert.Contains(t, html, `id="rate-filter"`)
	})
}

func TestRenderRatesTable(t *testing.T) {
	t.Parallel()

	t.Run("empty rates shows no-rates message", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		html := ui.RenderRatesTable(state)
		assert.Contains(t, html, "No rates.")
	})

	t.Run("rate row renders escaped base and quote currency", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Rates = []api.RateResponse{
			{ID: "1", BaseCurrency: `US"D`, QuoteCurrency: "EUR", Price: 1.1, Timestamp: "2026-01-01T00:00:00Z"},
		}
		html := ui.RenderRatesTable(state)
		assert.Contains(t, html, "US&quot;D")
		assert.Contains(t, html, "EUR")
		assert.NotContains(t, html, `US"D`)
	})

	t.Run("XSS payload in currencies is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Rates = []api.RateResponse{
			{ID: "1", BaseCurrency: "<script>", QuoteCurrency: ">EUR", Price: 1.0},
		}
		html := ui.RenderRatesTable(state)
		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;script&gt;")
		assert.Contains(t, html, "&gt;EUR")
	})

	t.Run("sort arrow down when RateSortDesc true", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.RateSortDesc = true
		html := ui.RenderRatesTable(state)
		assert.Contains(t, html, "↓")
		assert.NotContains(t, html, "↑")
	})

	t.Run("sort arrow up when RateSortDesc false", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.RateSortDesc = false
		html := ui.RenderRatesTable(state)
		assert.Contains(t, html, "↑")
		assert.NotContains(t, html, "↓")
	})

	t.Run("rate sort header has stable id for delegated click", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		html := ui.RenderRatesTable(state)
		assert.Contains(t, html, `id="rate-sort-header"`)
	})

	t.Run("no inline onclick attributes", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Rates = []api.RateResponse{
			{ID: "1", BaseCurrency: "USD", QuoteCurrency: "EUR", Price: 1.1},
		}
		html := ui.RenderRatesTable(state)
		assert.NotContains(t, html, "onclick")
	})
}

func TestRenderSubsSection(t *testing.T) {
	t.Parallel()

	t.Run("empty subs shows no-subscriptions message", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		html := ui.RenderSubsSection(state)
		assert.Contains(t, html, "No subscriptions.")
	})

	t.Run("condition with < and > is escaped to prevent XSS", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Subs = []api.SubscriptionDetailResponse{
			{ID: "1", UserType: "telegram", SourceName: "src", Condition: "price > 100 & price < 200"},
		}
		html := ui.RenderSubsSection(state)
		assert.NotContains(t, html, "price > 100")
		assert.NotContains(t, html, "price < 200")
		assert.Contains(t, html, "price &gt; 100")
		assert.Contains(t, html, "price &lt; 200")
		assert.Contains(t, html, "&amp;")
	})

	t.Run("latest_notified_at empty renders em dash", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Subs = []api.SubscriptionDetailResponse{
			{ID: "1", UserType: "telegram", SourceName: "src", Condition: "cond", LatestNotifiedAt: ""},
		}
		html := ui.RenderSubsSection(state)
		assert.Contains(t, html, "—")
	})

	t.Run("latest_notified_at non-empty renders formatted date", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Subs = []api.SubscriptionDetailResponse{
			{ID: "1", UserType: "telegram", SourceName: "src", Condition: "cond", LatestNotifiedAt: "2026-01-15T12:00:00Z"},
		}
		html := ui.RenderSubsSection(state)
		assert.NotContains(t, html, "2026-01-15T12:00:00Z", "raw ISO should not appear; formatted date should")
		assert.Contains(t, html, "2026")
	})

	t.Run("XSS payload in sub id and user_type is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Subs = []api.SubscriptionDetailResponse{
			{ID: `<b>id</b>`, UserType: `<script>`, SourceName: "src", Condition: "cond"},
		}
		html := ui.RenderSubsSection(state)
		assert.NotContains(t, html, "<b>id</b>")
		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;b&gt;id&lt;/b&gt;")
		assert.Contains(t, html, "&lt;script&gt;")
	})

	t.Run("no inline onclick attributes", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.Subs = []api.SubscriptionDetailResponse{
			{ID: "1", Condition: "x"},
		}
		html := ui.RenderSubsSection(state)
		assert.NotContains(t, html, "onclick")
	})

	t.Run("pagination uses data attributes not onclick", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.SubsPage = 2
		state.Subs = make([]api.SubscriptionDetailResponse, application.SubsLimit)
		html := ui.RenderSubsSection(state)
		assert.Contains(t, html, `data-section="subs"`)
		assert.NotContains(t, html, "onclick")
	})
}

func TestRenderDailyEventsSection(t *testing.T) {
	t.Parallel()

	t.Run("empty events shows no-data message", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		html := ui.RenderDailyEventsSection(state)
		assert.Contains(t, html, "No daily event data.")
	})

	t.Run("daily event row renders type and date escaped", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.DailyEvents = []api.DailyEventResponse{
			{Type: "<rate>", Date: "2026-01-01", SuccessCount: 5, FailedCount: 2},
		}
		html := ui.RenderDailyEventsSection(state)
		assert.NotContains(t, html, "<rate>")
		assert.Contains(t, html, "&lt;rate&gt;")
		assert.Contains(t, html, "2026-01-01")
		assert.Contains(t, html, "5/2")
	})

	t.Run("no inline onclick attributes", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.DailyEvents = []api.DailyEventResponse{
			{Type: "rate", Date: "2026-01-01"},
		}
		html := ui.RenderDailyEventsSection(state)
		assert.NotContains(t, html, "onclick")
	})

	t.Run("pagination uses data attributes not onclick", func(t *testing.T) {
		t.Parallel()
		state := makeDetailState("src", "Source")
		state.DailyEventsPage = 2
		state.DailyEvents = make([]api.DailyEventResponse, application.DailyEventsLimit)
		html := ui.RenderDailyEventsSection(state)
		assert.Contains(t, html, `data-section="daily-events"`)
		assert.NotContains(t, html, "onclick")
	})
}
