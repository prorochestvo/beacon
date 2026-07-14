package ui_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/ui"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

func meSubsState(items []dto.MeSubscriptionRow) application.MeSubscriptionsState {
	return application.MeSubscriptionsState{
		Items: items,
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

	t.Run("chart slot renders skeleton when ChartLoading and no Chart", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()})
		state.ChartLoading = true
		state.Chart = nil
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `class="sparkline-skeleton"`)
		assert.NotContains(t, html, "<polyline")
	})

	t.Run("chart slot renders empty state when Chart is nil and not loading", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil)
		state.Chart = nil
		state.ChartLoading = false
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, "sparkline-empty")
		// Empty state must use <div> so padding/background CSS applies correctly.
		assert.Contains(t, html, `<div class="sparkline-empty">`)
		assert.NotContains(t, html, `<p class="sparkline-empty">`)
	})

	t.Run("chart slot renders error state when ChartError is set", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()})
		state.ChartError = errors.New("timeout")
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, "sparkline-error")
		assert.Contains(t, html, "Chart unavailable")
	})

	t.Run("chart slot renders sparkline list when Chart is populated", func(t *testing.T) {
		t.Parallel()
		state := meSubsState([]dto.MeSubscriptionRow{singleItem()})
		state.Chart = &dto.MeChartResponse{
			Window: "7d",
			Pairs: []dto.MeChartPairRow{
				{
					Pair:     "USD/KZT",
					Category: "fiat",
					Series: []dto.MeChartSeries{
						{
							Kind:   "BID",
							Color:  "#1D9E75",
							Latest: 487.0,
							Points: []dto.MeChartPoint{
								mkPoint("2026-05-23T00:00:00Z", 480.0),
								mkPoint("2026-05-27T00:00:00Z", 487.0),
							},
						},
					},
				},
			},
		}
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, "sparkline-row")
		assert.Contains(t, html, "USD/KZT")
	})

	t.Run("pair modal slot is always present unless auth failure", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil)
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `id="me-pair-modal-slot"`)
	})

	t.Run("gear button is present on authenticated screen", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil)
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `id="me-manage"`)
		assert.Contains(t, html, `class="me-manage-gear"`)
	})

	t.Run("weather button is present on authenticated screen", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil)
		html := ui.RenderMeSubscriptions(state)
		assert.Contains(t, html, `id="me-weather"`)
		assert.Contains(t, html, `class="me-weather-cloud"`)
	})

	t.Run("401 auth failure shows error message and hides chart, modal slot, and buttons", func(t *testing.T) {
		t.Parallel()
		state := meSubsState(nil)
		state.AuthFailure = true
		html := ui.RenderMeSubscriptions(state)

		assert.Contains(t, html, "must be opened from the bot")
		assert.Contains(t, html, `class="error-msg"`)
		// Auth failure must not render chart, modal slot, or the floating buttons.
		assert.NotContains(t, html, `id="me-sparkline-chart"`)
		assert.NotContains(t, html, `id="me-pair-modal-slot"`)
		assert.NotContains(t, html, `id="me-manage"`)
		assert.NotContains(t, html, `id="me-weather"`)
	})
}
