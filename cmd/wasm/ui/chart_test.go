package ui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/cmd/wasm/ui"
	"github.com/seilbekskindirov/monitor/internal/domain/ratepair"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// mkPoint builds a MeChartPoint at the given RFC3339 time and value.
func mkPoint(ts string, v float64) dto.MeChartPoint {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return dto.MeChartPoint{Timestamp: t, Value: v}
}

// mkSeries constructs a MeChartSeries for test use.
func mkSeries(kind, color string, delta float64, sparse bool, points []dto.MeChartPoint) dto.MeChartSeries {
	var latest float64
	if len(points) > 0 {
		latest = points[len(points)-1].Value
	}
	return dto.MeChartSeries{
		Kind:     kind,
		Color:    color,
		Latest:   latest,
		DeltaPct: delta,
		Sparse:   sparse,
		Points:   points,
	}
}

// mkRow constructs a MeChartPairRow for test use.
func mkRow(pair, category string, spreadPct *float64, series []dto.MeChartSeries) dto.MeChartPairRow {
	return dto.MeChartPairRow{
		Pair:      pair,
		Category:  category,
		SpreadPct: spreadPct,
		Series:    series,
	}
}

func float64Ptr(v float64) *float64 { return &v }

func TestRenderSparklineList(t *testing.T) {
	t.Parallel()

	t.Run("empty pairs produces empty-state HTML", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: nil})
		assert.Contains(t, html, "sparkline-empty")
		assert.NotContains(t, html, "sparkline-row")
	})

	t.Run("two-series row renders pair label, spread glyph, and SVG with two polylines", func(t *testing.T) {
		t.Parallel()
		bidPts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-24T00:00:00Z", 487.55),
		}
		askPts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 481.0),
			mkPoint("2026-05-24T00:00:00Z", 488.95),
		}
		bid := mkSeries("BID", ratepair.ColorBid, 3.6, false, bidPts)
		ask := mkSeries("ASK", ratepair.ColorAsk, 3.4, false, askPts)
		row := mkRow("USD/KZT", "fiat", float64Ptr(0.29), []dto.MeChartSeries{bid, ask})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})

		assert.Contains(t, html, "sparkline-row")
		assert.Contains(t, html, `data-pair="USD/KZT"`)
		assert.Contains(t, html, `role="button"`)
		assert.Contains(t, html, `tabindex="0"`)
		assert.Contains(t, html, "USD/KZT")
		// Text block: pair label + spread glyph line.
		assert.Contains(t, html, "sparkline-row-text")
		assert.Contains(t, html, "↔ 0.29%")
		assert.NotContains(t, html, "Spread 0.29%", "list row uses the ↔ glyph, not the word")
		// No per-series value text or sparkline-value-line in the list.
		assert.NotContains(t, html, ">B<")
		assert.NotContains(t, html, ">A<")
		assert.NotContains(t, html, "sparkline-value-line")
		// SVG sparkline with two polylines is present.
		assert.Contains(t, html, "sparkline-row-chart")
		assert.Contains(t, html, "<svg")
		assert.Equal(t, 2, strings.Count(html, "<polyline "), "two-series row must emit two polylines")
	})

	t.Run("single-series row renders pair label, Δ delta, and SVG with one polyline", func(t *testing.T) {
		t.Parallel()
		bidPts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 492.0),
		}
		bid := mkSeries("BID", ratepair.ColorBid, 2.5, false, bidPts)
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})

		assert.Contains(t, html, "USD/KZT")
		assert.Contains(t, html, `data-pair="USD/KZT"`)
		assert.Contains(t, html, "sparkline-row-text")
		assert.Contains(t, html, "sparkline-row-delta")
		assert.Contains(t, html, "Δ")
		assert.Contains(t, html, "+2.50%")
		// No per-series prefix text in the list.
		assert.NotContains(t, html, ">BID<")
		// SVG is present with one polyline.
		assert.Contains(t, html, "sparkline-row-chart")
		assert.Contains(t, html, "<svg")
		assert.Equal(t, 1, strings.Count(html, "<polyline "), "single-series row must emit one polyline")
	})

	t.Run("no-data row (sparse series with zero Latest) renders Δ +0.0% delta and flat SVG line", func(t *testing.T) {
		t.Parallel()
		// A sparse series with Latest==0 is still a series; the collapsed row
		// shows Δ +0.0% (the sparse formatSparklineDelta value) and a flat SVG
		// line (renderChartArea handles the all-sparse all-zero as "no data" badge).
		sr := dto.MeChartSeries{
			Kind:   "BID",
			Color:  ratepair.ColorBid,
			Latest: 0,
			Sparse: true,
			Points: nil,
		}
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{sr})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})

		assert.Contains(t, html, "Δ")
		assert.Contains(t, html, "+0.0%")
		assert.Contains(t, html, "sparkline-row-delta")
		// renderChartArea returns a "no data" span when all series are all-zero,
		// so no <svg> element — but the chart div wrapper is present.
		assert.Contains(t, html, "sparkline-row-chart")
	})

	t.Run("zero-series row renders no-data badge and no SVG", func(t *testing.T) {
		t.Parallel()
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})

		assert.Contains(t, html, "no data")
		assert.Contains(t, html, "sparkline-row-delta-empty")
		// Zero-series rows skip the chart div entirely.
		assert.NotContains(t, html, "sparkline-row-chart")
		assert.NotContains(t, html, "<svg")
	})

	t.Run("XSS in pair label is escaped in data-pair and aria-label and label div", func(t *testing.T) {
		t.Parallel()
		sr := dto.MeChartSeries{Kind: "BID", Color: ratepair.ColorBid, Latest: 0, Sparse: true}
		row := mkRow(`<script>alert(1)</script>`, "fiat", nil, []dto.MeChartSeries{sr})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;script&gt;")
		// Must be escaped in data-pair attribute and aria-label.
		assert.Contains(t, html, `data-pair="&lt;script&gt;`)
		assert.Contains(t, html, `aria-label="Open details for &lt;script&gt;`)
	})

	t.Run("list row does not emit sparkline-value-line", func(t *testing.T) {
		t.Parallel()
		bidPts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 490.0),
		}
		askPts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 481.0),
			mkPoint("2026-05-27T00:00:00Z", 491.0),
		}
		bid := mkSeries("BID", ratepair.ColorBid, 2.08, false, bidPts)
		ask := mkSeries("ASK", ratepair.ColorAsk, 2.08, false, askPts)
		row := mkRow("USD/KZT", "fiat", float64Ptr(0.21), []dto.MeChartSeries{bid, ask})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.NotContains(t, html, "sparkline-value-line",
			"per-series value text lives in the modal only; it must not appear in the list row")
	})

	t.Run("output contains no script tag", func(t *testing.T) {
		t.Parallel()
		pts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 490.0),
		}
		bid := mkSeries("BID", ratepair.ColorBid, 2.08, false, pts)
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.NotContains(t, strings.ToLower(html), "<script")
	})

	t.Run("spread glyph not present when SpreadPct is nil", func(t *testing.T) {
		t.Parallel()
		bid := mkSeries("BID", ratepair.ColorBid, 1.0, false, []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 485.0),
		})
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.NotContains(t, html, "↔")
	})

	t.Run("positive delta renders with + prefix", func(t *testing.T) {
		t.Parallel()
		pts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 500.0),
		}
		bid := mkSeries("BID", ratepair.ColorBid, 4.17, false, pts)
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.Contains(t, html, "+4.17%")
	})

	t.Run("negative delta renders with minus sign", func(t *testing.T) {
		t.Parallel()
		pts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 500.0),
			mkPoint("2026-05-27T00:00:00Z", 480.0),
		}
		bid := mkSeries("BID", ratepair.ColorBid, -4.0, false, pts)
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.Contains(t, html, "-4.00%")
	})

	t.Run("multiple rows are all rendered", func(t *testing.T) {
		t.Parallel()
		pts1 := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 490.0),
		}
		pts2 := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 100.0),
			mkPoint("2026-05-27T00:00:00Z", 98.0),
		}
		ch := dto.MeChartResponse{
			Window: "7d",
			Pairs: []dto.MeChartPairRow{
				mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{mkSeries("BID", ratepair.ColorBid, 2.08, false, pts1)}),
				mkRow("GOLD/KZT", "metal", nil, []dto.MeChartSeries{mkSeries("BID", "#BA7517", -2.0, false, pts2)}),
			},
		}
		html := ui.RenderSparklineList(ch)
		assert.Equal(t, 2, strings.Count(html, `class="sparkline-row"`))
		assert.Contains(t, html, "USD/KZT")
		assert.Contains(t, html, "GOLD/KZT")
	})

	t.Run("dashed baseline appears in list row SVG (renderChartArea works)", func(t *testing.T) {
		t.Parallel()
		pts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", -1.0),
			mkPoint("2026-05-27T00:00:00Z", 2.0),
		}
		bid := mkSeries("BID", ratepair.ColorBid, 0.0, false, pts)
		row := mkRow("EUR/USD", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		require.NotEmpty(t, html)
		// SVG is present and contains the dashed baseline.
		assert.Contains(t, html, "<svg")
		assert.Contains(t, html, "stroke-dasharray")
	})

	t.Run("color values appear in collapsed row delta color attribute", func(t *testing.T) {
		t.Parallel()
		pts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 100.0),
			mkPoint("2026-05-27T00:00:00Z", 110.0),
		}
		bid := mkSeries("BID", "#BA7517", 10.0, false, pts)
		row := mkRow("GOLD/KZT", "metal", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		// Delta uses ratepair.ColorDeltaUp for positive delta — not the series color.
		assert.Contains(t, html, ratepair.ColorDeltaUp)
	})

	t.Run("quote in Color is still escaped when used in delta style attribute", func(t *testing.T) {
		t.Parallel()
		// Hostile color attempts to break out of style="color:..." in the delta div.
		hostileColor := `#1D9E75" onload="x`
		pts := []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 490.0),
		}
		bid := mkSeries("BID", hostileColor, 2.0, false, pts)
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		// The series color is not used in the collapsed row (delta color comes from
		// ratepair.ColorDeltaUp/Down). The hostile color must not appear verbatim.
		assert.NotContains(t, html, `" onload="`, "raw injection payload must not appear in output")
	})

	t.Run("footer copy reads tap a row to see details", func(t *testing.T) {
		t.Parallel()
		bid := mkSeries("BID", ratepair.ColorBid, 0.0, false, []dto.MeChartPoint{
			mkPoint("2026-05-23T00:00:00Z", 480.0),
			mkPoint("2026-05-27T00:00:00Z", 485.0),
		})
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.Contains(t, html, "tap a row to see details")
		assert.NotContains(t, html, "compare sources")
	})

	t.Run("period chip row is present with 7d active by default", func(t *testing.T) {
		t.Parallel()
		bid := mkSeries("BID", ratepair.ColorBid, 0.5, false, []dto.MeChartPoint{
			{Value: 449.5}, {Value: 450.0},
		})
		row := mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})
		html := ui.RenderSparklineList(dto.MeChartResponse{Window: "7d", Pairs: []dto.MeChartPairRow{row}})
		assert.Contains(t, html, `period-chip-row`)
		assert.Contains(t, html, `data-period="7"`)
		assert.Contains(t, html, `period-chip--active`)
		// All 5 period chips are present.
		for _, p := range []string{"7", "30", "90", "180", "360"} {
			assert.Contains(t, html, `data-period="`+p+`"`, "chip for period %s must be present", p)
		}
	})
}

func TestRenderSparklineListForPeriod(t *testing.T) {
	t.Parallel()

	singleRow := func() []dto.MeChartPairRow {
		bid := mkSeries("BID", ratepair.ColorBid, 0.5, false, []dto.MeChartPoint{{Value: 449.5}, {Value: 450.0}})
		return []dto.MeChartPairRow{mkRow("USD/KZT", "fiat", nil, []dto.MeChartSeries{bid})}
	}

	t.Run("active chip matches the supplied period", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSparklineListForPeriod(dto.MeChartResponse{Window: "30 days", Pairs: singleRow()}, 30)
		// The 30d chip must have the active class.
		require.Contains(t, html, `period-chip--active`)
		// Find the active chip span and verify it carries data-period="30".
		idx := strings.Index(html, `period-chip--active`)
		require.Greater(t, idx, 0)
		// The active chip HTML contains data-period="30".
		assert.Contains(t, html[max(0, idx-100):idx+100], `data-period="30"`)
	})

	t.Run("invalid period falls back to 7d active", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSparklineListForPeriod(dto.MeChartResponse{Window: "7d", Pairs: singleRow()}, 999)
		require.Contains(t, html, `period-chip--active`)
		idx := strings.Index(html, `period-chip--active`)
		assert.Contains(t, html[max(0, idx-100):idx+100], `data-period="7"`)
	})

	t.Run("empty pairs still returns empty-state (chips are not rendered)", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSparklineListForPeriod(dto.MeChartResponse{Window: "7d", Pairs: nil}, 30)
		assert.Contains(t, html, `sparkline-empty`)
		assert.NotContains(t, html, `period-chip`)
	})
}

// max returns the larger of a and b. Provided as a local helper because the
// standard library builtin max is not available before Go 1.21 in all build targets.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
