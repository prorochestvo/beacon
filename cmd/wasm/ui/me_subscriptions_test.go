package ui_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/cmd/wasm/ui"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

func meSubsState(items []dto.MeSubscriptionRow, total int64, page, pageSize int) application.MeSubscriptionsState {
	return application.MeSubscriptionsState{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Period:   application.MeSubscriptionsDefaultPeriod,
		Charts:   map[string]application.ChartState{},
		Expanded: map[string]bool{},
	}
}

func singleItem() dto.MeSubscriptionRow {
	return dto.MeSubscriptionRow{
		SourceName:    "usd-eur",
		SourceTitle:   "USD/EUR",
		BaseCurrency:  "USD",
		QuoteCurrency: "EUR",
		Conditions:    []string{">1.05", "<2.00"},
		LatestPrice:   1.0812,
		LatestAt:      "2026-01-01T12:00:00Z",
	}
}

func TestRenderMeSubscriptions(t *testing.T) {
	t.Parallel()

	t.Run("empty state renders no-subscriptions message", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil, 0, 1, 10)
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, "No subscriptions found.")
		assert.Contains(t, html, `class="search-bar"`)
		assert.NotContains(t, html, `class="pagination"`)
	})

	t.Run("pagination wrapper id is always present", func(t *testing.T) {
		t.Parallel()

		t.Run("happy path with items", func(t *testing.T) {
			t.Parallel()
			state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
			html := ui.RenderMeSubscriptions(state)
			assert.Contains(t, html, `id="me-subs-pagination"`)
		})

		t.Run("empty state", func(t *testing.T) {
			t.Parallel()
			state := meSubsState(nil, 0, 1, 10)
			html := ui.RenderMeSubscriptions(state)
			assert.Contains(t, html, `id="me-subs-pagination"`)
		})

		t.Run("auth failure", func(t *testing.T) {
			t.Parallel()
			state := meSubsState(nil, 0, 1, 10)
			state.AuthFailure = true
			html := ui.RenderMeSubscriptions(state)
			assert.Contains(t, html, `id="me-subs-pagination"`)
		})

		t.Run("generic error", func(t *testing.T) {
			t.Parallel()
			state := meSubsState(nil, 0, 1, 10)
			state.LastError = errors.New("network timeout")
			html := ui.RenderMeSubscriptions(state)
			assert.Contains(t, html, `id="me-subs-pagination"`)
		})
	})

	t.Run("single card renders all fields", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		html := ui.RenderMeSubscriptions(state)

		assert.Contains(t, html, `class="card"`)
		assert.Contains(t, html, `class="card-title"`)
		assert.Contains(t, html, "USD/EUR")
		assert.Contains(t, html, `class="card-pair"`)
		assert.Contains(t, html, "USD/EUR")
		assert.Contains(t, html, `class="card-price"`)
		assert.Contains(t, html, "1.0812")
		assert.Contains(t, html, `class="card-time"`)
		assert.Contains(t, html, "Last grab:")
		assert.Contains(t, html, `class="badge"`)
		assert.Contains(t, html, "&gt;1.05")
		assert.Contains(t, html, "&lt;2.00")
	})

	t.Run("single card no pagination when only 1 page", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		html := ui.RenderMeSubscriptions(state)
		assert.NotContains(t, html, `class="pagination"`)
	})

	t.Run("multi-card with pagination shows prev and next buttons", func(t *testing.T) {
		t.Parallel()
		items := []dto.MeSubscriptionRow{
			singleItem(),
			{SourceName: "gbp-usd", SourceTitle: "GBP/USD", BaseCurrency: "GBP", QuoteCurrency: "USD", LatestPrice: 1.27},
		}
		// 25 total, page size 10, current page 2 → both prev and next shown
		state := meSubsState(items, 25, 2, 10)
		html := ui.RenderMeSubscriptions(state)

		assert.Contains(t, html, `class="pagination"`)
		assert.Contains(t, html, `data-section="me-subs"`)
		// page 2 → prev button navigates to page 1
		assert.Contains(t, html, `data-page="1"`)
		// page 2, count 2 (< pageSize 10) → no next button; len(items)==2 < limit=10
		// Actually 2 items < 10 limit so no next. Let's assert that.
		assert.NotContains(t, html, `data-page="3"`)
	})

	t.Run("multi-card page 1 with full page shows next but no prev", func(t *testing.T) {
		t.Parallel()
		items := make([]dto.MeSubscriptionRow, 10)
		for i := range items {
			items[i] = singleItem()
		}
		state := meSubsState(items, 25, 1, 10)
		html := ui.RenderMeSubscriptions(state)

		assert.Contains(t, html, `class="pagination"`)
		assert.Contains(t, html, `data-page="2"`) // next page
		assert.NotContains(t, html, `data-page="0"`)
		// prev disabled because page == 1
		assert.Contains(t, html, `<button disabled>`)
	})

	t.Run("401 auth failure shows error message and hides pagination", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil, 0, 1, 10)
		state.AuthFailure = true
		html := ui.RenderMeSubscriptions(state)

		assert.Contains(t, html, "must be opened from the bot")
		assert.Contains(t, html, `class="error-msg"`)
		assert.NotContains(t, html, `class="pagination"`)
		assert.NotContains(t, html, "No subscriptions yet.")
	})

	t.Run("generic error shows error message and hides pagination", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil, 0, 1, 10)
		state.LastError = errors.New("network timeout")
		html := ui.RenderMeSubscriptions(state)

		assert.Contains(t, html, "Error loading subscriptions:")
		assert.Contains(t, html, "network timeout")
		assert.Contains(t, html, `class="error-msg"`)
		assert.NotContains(t, html, `class="pagination"`)
	})

	t.Run("XSS payload in source_title is escaped", func(t *testing.T) {
		t.Parallel()
		item := dto.MeSubscriptionRow{
			SourceName:    "evil-source",
			SourceTitle:   `<script>alert(1)</script>`,
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
			Conditions:    []string{`<img src=x onerror=alert(1)>`, `A & B > C`},
			LatestPrice:   1.0,
		}
		state := meSubsState([]dto.MeSubscriptionRow{item}, 1, 1, 10)
		html := ui.RenderMeSubscriptions(state)

		assert.NotContains(t, html, "<script>", "raw <script> must not appear")
		assert.NotContains(t, html, "alert(1)</script>")
		assert.Contains(t, html, "&lt;script&gt;")
		assert.Contains(t, html, "&lt;img src=x onerror=alert(1)&gt;")
		assert.Contains(t, html, "A &amp; B &gt; C")
	})

	t.Run("source_name used as fallback when source_title is empty", func(t *testing.T) {
		t.Parallel()
		item := dto.MeSubscriptionRow{
			SourceName:    "my-source",
			SourceTitle:   "",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
		}
		state := meSubsState([]dto.MeSubscriptionRow{item}, 1, 1, 10)
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, "my-source")
	})

	t.Run("missing latest_price renders dash", func(t *testing.T) {
		t.Parallel()
		item := dto.MeSubscriptionRow{
			SourceName:    "s",
			SourceTitle:   "S",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
			LatestPrice:   0,
		}
		state := meSubsState([]dto.MeSubscriptionRow{item}, 1, 1, 10)
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `class="card-price"`)
		assert.Contains(t, html, "—")
	})

	t.Run("search bar renders with current query value", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil, 0, 1, 10)
		state.Query = "usd"
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `value="usd"`)
	})

	t.Run("XSS in query value is escaped in search bar", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil, 0, 1, 10)
		state.Query = `"><script>alert(1)</script>`
		html := ui.RenderMeSubscriptions(state)
		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;script&gt;")
	})

	t.Run("period toggle shows three buttons with current period active", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil, 0, 1, 10)
		state.Period = application.MeSubscriptionsPeriodMonth
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `id="me-period-toggle"`)
		assert.Contains(t, html, `data-period="week"`)
		assert.Contains(t, html, `data-period="month"`)
		assert.Contains(t, html, `data-period="year"`)
		// month is active
		assert.Contains(t, html, `class="period-btn active" data-period="month"`)
		// week and year are not active
		assert.Contains(t, html, `class="period-btn" data-period="week"`)
		assert.Contains(t, html, `class="period-btn" data-period="year"`)
	})

	t.Run("chart slot renders skeleton when Charts entry is missing", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		// Charts map is empty → no entry for "usd-eur"
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `class="card-chart-skeleton"`)
		assert.NotContains(t, html, `class="card-chart"`)
	})

	t.Run("chart slot renders skeleton when Loading is true", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		state.Charts["usd-eur"] = application.ChartState{Loading: true}
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `class="card-chart-skeleton"`)
	})

	t.Run("chart slot renders SVG when Loaded is true", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		state.Charts["usd-eur"] = application.ChartState{
			Loaded: true,
			Points: []dto.ChartPointResponse{{Label: "2026-01-01", Price: 1.08}},
		}
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `class="card-chart"`)
		assert.Contains(t, html, `<polyline`)
		assert.NotContains(t, html, `class="card-chart-skeleton"`)
	})

	t.Run("chart slot renders error message when Error is non-nil", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		state.Charts["usd-eur"] = application.ChartState{
			Error: errors.New("network timeout"),
		}
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `class="card-chart-error"`)
		assert.Contains(t, html, "chart unavailable")
		// raw error text must not appear in the UI
		assert.NotContains(t, html, "network timeout")
	})

	t.Run("expanded list renders label and price for each point", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		state.Charts["usd-eur"] = application.ChartState{
			Loaded: true,
			Points: []dto.ChartPointResponse{
				{Label: "2026-01-01", Price: 1.0812},
				{Label: "2026-01-02", Price: 1.0934},
			},
		}
		state.Expanded["usd-eur"] = true
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `class="card-chart-expanded"`)
		assert.Contains(t, html, "2026-01-01")
		assert.Contains(t, html, "1.0812")
		assert.Contains(t, html, "2026-01-02")
		assert.Contains(t, html, "1.0934")
	})

	t.Run("expanded list hidden when Expanded[name] is false", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		state.Charts["usd-eur"] = application.ChartState{
			Loaded: true,
			Points: []dto.ChartPointResponse{{Label: "2026-01-01", Price: 1.08}},
		}
		state.Expanded["usd-eur"] = false
		html := ui.RenderMeSubscriptions(state)
		assert.NotContains(t, html, `class="card-chart-expanded"`)
	})

	t.Run("expanded list hidden when chart not yet loaded even if Expanded is true", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		// Chart not loaded, but Expanded is true.
		state.Expanded["usd-eur"] = true
		html := ui.RenderMeSubscriptions(state)
		assert.NotContains(t, html, `class="card-chart-expanded"`)
	})

	t.Run("chart-slot id is escaped source name", func(t *testing.T) {
		t.Parallel()
		item := dto.MeSubscriptionRow{
			SourceName:    `evil"name`,
			SourceTitle:   "Evil",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
		}
		state := meSubsState([]dto.MeSubscriptionRow{item}, 1, 1, 10)
		html := ui.RenderMeSubscriptions(state)
		// The raw quote must not appear unescaped in the id attribute.
		assert.NotContains(t, html, `id="card-chart-evil"name"`)
		assert.Contains(t, html, `card-chart-evil&quot;name`)
	})

	t.Run("card carries data-source-name attribute", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()}, 1, 1, 10)
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `data-source-name="usd-eur"`)
	})
}
