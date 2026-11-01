package ui_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/ui"
	"github.com/seilbekskindirov/beacon/internal/domain/ratepair"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// publicChartWith returns a PublicChartResponse populated with the given pairs.
func publicChartWith(pairs []dto.MeChartPairRow) *dto.PublicChartResponse {
	return &dto.PublicChartResponse{
		Window: "7 days",
		Page:   1,
		Limit:  20,
		Total:  int64(len(pairs)),
		Pairs:  pairs,
	}
}

func TestRenderPublicSubscriptions(t *testing.T) {
	t.Parallel()

	t.Run("skeleton when ChartLoading and Chart is nil", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{
			ChartLoading: true,
			Chart:        nil,
		}
		html := ui.RenderPublicSubscriptions(state)
		assert.Contains(t, html, `class="sparkline-skeleton"`)
		assert.Contains(t, html, `id="public-sparkline-chart"`)
		assert.NotContains(t, html, "sparkline-row")
	})

	t.Run("empty state when Chart is nil and not loading", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{
			ChartLoading: false,
			Chart:        nil,
		}
		html := ui.RenderPublicSubscriptions(state)
		assert.Contains(t, html, "sparkline-empty")
		assert.Contains(t, html, `id="public-sparkline-chart"`)
		assert.NotContains(t, html, "sparkline-row")
	})

	t.Run("error state when ChartError is set", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{
			ChartError: errors.New("timeout"),
		}
		html := ui.RenderPublicSubscriptions(state)
		assert.Contains(t, html, "sparkline-error")
		assert.Contains(t, html, "Chart unavailable")
	})

	t.Run("list with pagination when Chart has pairs", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 1.0, false, []dto.MeChartPoint{
					mkPoint("2026-05-27T00:00:00Z", 449.5),
				}),
			}),
		}
		state := application.PublicSubscriptionsState{
			Chart: publicChartWith(pairs),
			Page:  1,
			Limit: 20,
			Total: 1,
		}
		html := ui.RenderPublicSubscriptions(state)
		assert.Contains(t, html, "sparkline-row")
		assert.Contains(t, html, "USD/KZT")
		assert.Contains(t, html, `id="public-sparkline-chart"`)
		assert.Contains(t, html, `id="public-pagination"`)
		assert.Contains(t, html, `id="public-pair-modal-slot"`)
	})

	t.Run("all three wrapper divs always present", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderPublicSubscriptions(application.PublicSubscriptionsState{})
		assert.Contains(t, html, `id="public-sparkline-chart"`)
		assert.Contains(t, html, `id="public-pagination"`)
		assert.Contains(t, html, `id="public-pair-modal-slot"`)
	})
}

func TestRenderPublicSparklineSlot(t *testing.T) {
	t.Parallel()

	t.Run("returns skeleton when ChartLoading and Chart is nil", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{ChartLoading: true}
		html := ui.RenderPublicSparklineSlot(state)
		assert.Contains(t, html, `class="sparkline-skeleton"`)
	})

	t.Run("returns empty div when Chart is nil and not loading", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{}
		html := ui.RenderPublicSparklineSlot(state)
		assert.Contains(t, html, "sparkline-empty")
	})

	t.Run("returns error p when ChartError is set", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{ChartError: errors.New("unavailable")}
		html := ui.RenderPublicSparklineSlot(state)
		assert.Contains(t, html, "sparkline-error")
		assert.Contains(t, html, "Chart unavailable")
	})

	t.Run("returns sparkline list when Chart is populated", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 1.0, false, []dto.MeChartPoint{
					mkPoint("2026-05-27T00:00:00Z", 449.5),
				}),
			}),
		}
		state := application.PublicSubscriptionsState{Chart: publicChartWith(pairs)}
		html := ui.RenderPublicSparklineSlot(state)
		assert.Contains(t, html, "sparkline-row")
		assert.Contains(t, html, "USD/KZT")
	})

	t.Run("ChartError takes priority over ChartLoading", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{
			ChartLoading: true,
			ChartError:   errors.New("x"),
		}
		html := ui.RenderPublicSparklineSlot(state)
		assert.Contains(t, html, "sparkline-error")
		assert.NotContains(t, html, "sparkline-skeleton")
	})
}

func TestRenderPublicPairModal(t *testing.T) {
	t.Parallel()

	t.Run("returns empty string when OpenPair is nil", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{}
		assert.Empty(t, ui.RenderPublicPairModal(state))
	})

	t.Run("returns empty string when Chart is nil", func(t *testing.T) {
		t.Parallel()
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{OpenPair: &p}
		assert.Empty(t, ui.RenderPublicPairModal(state))
	})

	t.Run("returns empty string when OpenPair not found in chart", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			mkRow("EUR/KZT", "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 0.0, false, nil),
			}),
		}
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{
			OpenPair: &p,
			Chart:    publicChartWith(pairs),
		}
		assert.Empty(t, ui.RenderPublicPairModal(state))
	})

	t.Run("valid pair renders role=dialog aria-modal and aria-labelledby", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 1.0, false, []dto.MeChartPoint{
					mkPoint("2026-05-27T00:00:00Z", 449.5),
				}),
			}),
		}
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{OpenPair: &p, Chart: publicChartWith(pairs)}
		html := ui.RenderPublicPairModal(state)

		assert.Contains(t, html, `role="dialog"`)
		assert.Contains(t, html, `aria-modal="true"`)
		assert.Contains(t, html, `aria-labelledby="public-pair-modal-title"`)
		assert.Contains(t, html, `id="public-pair-modal-close"`)
		assert.Contains(t, html, `id="public-pair-modal-backdrop"`)
	})

	t.Run("modal does NOT contain history button", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 1.0, false, []dto.MeChartPoint{
					mkPoint("2026-05-27T00:00:00Z", 449.5),
				}),
			}),
		}
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{OpenPair: &p, Chart: publicChartWith(pairs)}
		html := ui.RenderPublicPairModal(state)

		assert.NotContains(t, html, "me-pair-modal-history",
			"public modal must not have a history button — the history endpoint is auth-gated")
		assert.NotContains(t, html, "View history")
		assert.NotContains(t, html, "public-pair-modal-history")
	})

	t.Run("both series cards rendered for two-series row", func(t *testing.T) {
		t.Parallel()
		bidPts := []dto.MeChartPoint{mkPoint("2026-05-27T00:00:00Z", 449.5)}
		askPts := []dto.MeChartPoint{mkPoint("2026-05-27T00:00:00Z", 450.5)}
		bid := mkSeries("BID", ratepair.ColorBid, 1.0, false, bidPts)
		ask := mkSeries("ASK", ratepair.ColorAsk, 0.5, false, askPts)
		row := mkRow("USD/KZT", "fiat", float64Ptr(0.22), []dto.MeChartSeries{bid, ask})
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{
			OpenPair: &p,
			Chart:    publicChartWith([]dto.MeChartPairRow{row}),
		}
		html := ui.RenderPublicPairModal(state)

		assert.Equal(t, 2, strings.Count(html, `class="me-pair-modal-series"`))
		assert.Contains(t, html, "Spread 0.22%")
		assert.NotContains(t, html, "<svg", "public modal must be text-only")
	})

	t.Run("XSS in pair label escaped in data-pair and title", func(t *testing.T) {
		t.Parallel()
		hostile := `<script>alert(1)</script>`
		pairs := []dto.MeChartPairRow{
			mkRow(hostile, "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 0.0, true, nil),
			}),
		}
		state := application.PublicSubscriptionsState{
			OpenPair: &hostile,
			Chart:    publicChartWith(pairs),
		}
		html := ui.RenderPublicPairModal(state)

		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;script&gt;")
		assert.Contains(t, html, `data-pair="&lt;script&gt;`)
	})

	t.Run("no script tag in output", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 1.0, false, []dto.MeChartPoint{
					mkPoint("2026-05-27T00:00:00Z", 449.5),
				}),
			}),
		}
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{OpenPair: &p, Chart: publicChartWith(pairs)}
		html := ui.RenderPublicPairModal(state)
		assert.NotContains(t, strings.ToLower(html), "<script")
	})

	t.Run("last-grab line present when series has points", func(t *testing.T) {
		t.Parallel()
		pts := []dto.MeChartPoint{
			mkPoint("2026-05-20T00:00:00Z", 445.0),
			mkPoint("2026-05-27T00:00:00Z", 449.5),
		}
		bid := mkSeries("BID", ratepair.ColorBid, 1.0, false, pts)
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{
			OpenPair: &p,
			Chart:    publicChartWith([]dto.MeChartPairRow{row}),
		}
		html := ui.RenderPublicPairModal(state)

		assert.Contains(t, html, "me-pair-modal-time")
		assert.Contains(t, html, "Last grab:")
		assert.Contains(t, html, "5/27/2026")
	})

	t.Run("last-grab line absent when series has no points", func(t *testing.T) {
		t.Parallel()
		bid := dto.MeChartSeries{Kind: "BID", Color: ratepair.ColorBid, Latest: 449.5, Sparse: true, Points: nil}
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{
			OpenPair: &p,
			Chart:    publicChartWith([]dto.MeChartPairRow{row}),
		}
		html := ui.RenderPublicPairModal(state)

		assert.NotContains(t, html, "Last grab:")
		assert.NotContains(t, html, "me-pair-modal-time")
	})

	t.Run("modal uses public-specific IDs not me-specific ones", func(t *testing.T) {
		t.Parallel()
		pairs := []dto.MeChartPairRow{
			mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{
				mkSeries("BID", ratepair.ColorBid, 1.0, false, []dto.MeChartPoint{
					mkPoint("2026-05-27T00:00:00Z", 449.5),
				}),
			}),
		}
		p := "USD/KZT"
		state := application.PublicSubscriptionsState{OpenPair: &p, Chart: publicChartWith(pairs)}
		html := ui.RenderPublicPairModal(state)

		assert.Contains(t, html, `id="public-pair-modal"`)
		assert.Contains(t, html, `id="public-pair-modal-backdrop"`)
		assert.Contains(t, html, `id="public-pair-modal-close"`)
		assert.Contains(t, html, `id="public-pair-modal-title"`)
		// Must NOT carry me-specific IDs.
		assert.NotContains(t, html, `id="me-pair-modal"`)
		assert.NotContains(t, html, `id="me-pair-modal-close"`)
	})
}

func TestRenderPublicPagination(t *testing.T) {
	t.Parallel()

	t.Run("returns empty string when Chart is nil", func(t *testing.T) {
		t.Parallel()
		state := application.PublicSubscriptionsState{}
		assert.Empty(t, ui.RenderPublicPagination(state))
	})

	t.Run("returns empty string when single page fits all pairs", func(t *testing.T) {
		t.Parallel()
		pairs := make([]dto.MeChartPairRow, 3)
		state := application.PublicSubscriptionsState{
			Chart: publicChartWith(pairs),
			Page:  1,
			Limit: 20,
		}
		// 3 pairs with limit 20 — all fit on one page, no pagination needed.
		html := ui.RenderPublicPagination(state)
		assert.Empty(t, html)
	})

	t.Run("renders pagination buttons when pairs exceed limit", func(t *testing.T) {
		t.Parallel()
		// 25 pairs in the current page slice, limit 20 → pagination is needed.
		pairs := make([]dto.MeChartPairRow, 25)
		state := application.PublicSubscriptionsState{
			Chart: publicChartWith(pairs),
			Page:  1,
			Limit: 20,
		}
		html := ui.RenderPublicPagination(state)
		require.NotEmpty(t, html, "pagination must render when pairs > limit")
		assert.Contains(t, html, "pagination")
	})

	t.Run("last page shows Prev but suppresses Next", func(t *testing.T) {
		t.Parallel()
		// 5 items on page 2 (total dataset is 25, limit 20). Page>1 must
		// still render the control (Prev enabled), and Next must be
		// suppressed because the current page slice is shorter than Limit.
		pairs := make([]dto.MeChartPairRow, 5)
		state := application.PublicSubscriptionsState{
			Chart: publicChartWith(pairs),
			Page:  2,
			Limit: 20,
			Total: 25,
		}
		html := ui.RenderPublicPagination(state)
		require.NotEmpty(t, html, "pagination must render on a non-first page")
		assert.Contains(t, html, `data-page="1"`, "Prev button must target page 1")
		assert.NotContains(t, html, `data-page="3"`, "Next must be suppressed when Count < Limit")
	})
}
