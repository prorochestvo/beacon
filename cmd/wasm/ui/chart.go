package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/cmd/wasm/dom"
	"github.com/seilbekskindirov/monitor/internal/domain/ratepair"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// chart.go renders the sparkline-list view. Each pair row emits a flex row
// containing a text block on the left (pair label + spread/delta) and the SVG
// sparkline on the right. The renderChartArea helper is also called by the
// list row; the pair-detail modal is text-only (no SVG) as of plan 011.

// svgW and svgH are the viewBox dimensions used for every sparkline SVG.
// The polyline runs from x=5 to x=295 so the end halo never clips the edge.
const svgW, svgH = 300, 60

// svgXFirst and svgXLast are the x-coordinates of the first and last polyline points.
const svgXFirst = 5.0
const svgXLast = 295.0

// svgYMin and svgYMax are the inner y bounds of the sparkline drawing area.
// The polyline maps [minValue, maxValue] onto [svgYMax, svgYMin] (SVG y is top-down).
const svgYMin = 8.0
const svgYMax = 52.0

// allowedUIPeriods is the ordered list of period values (days) shown in the
// chip row. Matches application.AllowedChartPeriods; the list is inlined here
// to keep the ui package free of an import cycle with application.
var allowedUIPeriods = []int{7, 30, 90, 180, 360}

// RenderSparklineList returns the full HTML for the sparkline-list chart view.
// One row is rendered per pair in chart.Pairs. An empty Pairs slice renders
// the "no chart data" empty-state. The period selector row defaults to 7 days;
// use RenderSparklineListForPeriod to supply the active selection explicitly.
func RenderSparklineList(chart dto.MeChartResponse) string {
	return renderSparklineListInternal(chart, 7)
}

// RenderSparklineListForPeriod is like RenderSparklineList but marks the chip
// for period as active. period must be one of {7, 30, 90, 180, 360}; an
// unrecognised value is silently treated as 7.
func RenderSparklineListForPeriod(chart dto.MeChartResponse, period int) string {
	return renderSparklineListInternal(chart, period)
}

// renderSparklineListInternal renders the full chart list with the given active
// period chip highlighted.
func renderSparklineListInternal(chart dto.MeChartResponse, period int) string {
	if len(chart.Pairs) == 0 {
		return `<div class="sparkline-empty"><p>No chart data yet.</p></div>`
	}

	now := time.Now().UTC()
	dateLabel := fmt.Sprintf("%s · last %s", now.Format("Mon 02 Jan 2006"), chart.Window)

	var b strings.Builder
	b.WriteString(`<div class="sparkline-list">`)
	b.WriteString(`<div class="sparkline-header">`)
	b.WriteString(`<div class="sparkline-title">FX rates · % change</div>`)
	fmt.Fprintf(&b, `<div class="sparkline-subtitle">%s</div>`, dateLabel)
	b.WriteString(`</div>`)

	b.WriteString(renderPeriodChips(period))

	for _, row := range chart.Pairs {
		b.WriteString(renderSparklineRow(row))
	}

	b.WriteString(`<div class="sparkline-footer">tap a row to see details →</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// renderPeriodChips returns the HTML for the 5-chip period selector row.
// The chip for activePeriod carries the period-chip--active class; all chips
// carry a data-period attribute so the delegated click handler in main.go can
// read it without traversing child nodes.
//
// Period labels use a compact suffix (e.g. "7d", "30d") to fit on narrow
// screens. The active chip falls back to 7 when activePeriod is unrecognised.
func renderPeriodChips(activePeriod int) string {
	// Validate: if activePeriod is not in the whitelist, treat as 7.
	valid := false
	for _, p := range allowedUIPeriods {
		if activePeriod == p {
			valid = true
			break
		}
	}
	if !valid {
		activePeriod = 7
	}

	var b strings.Builder
	b.WriteString(`<div class="period-chip-row">`)
	for _, p := range allowedUIPeriods {
		activeClass := ""
		if p == activePeriod {
			activeClass = ` period-chip--active`
		}
		fmt.Fprintf(&b,
			`<button class="period-chip%s" type="button" data-period="%d">%dd</button>`,
			activeClass, p, p,
		)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// renderSparklineRow returns the HTML for one pair row in the sparkline list.
// Layout: a left text block (pair label + spread/delta) and an SVG sparkline
// on the right, side-by-side via flexbox. Zero-series rows omit the chart div
// and render only the "no data" text block so the two empty states do not
// stack on top of each other.
func renderSparklineRow(row dto.MeChartPairRow) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		`<div class="sparkline-row" data-pair="%s" role="button" tabindex="0" aria-label="Open details for %s">`,
		dom.Escape(row.Pair), dom.Escape(row.Pair),
	)
	b.WriteString(`<div class="sparkline-row-text">`)
	// Line 1: pair label.
	fmt.Fprintf(&b, `<div class="sparkline-pair-label">%s</div>`, dom.Escape(row.Pair))
	// Line 2: Spread X.XX% (two-series) or Δ X.XX% (single series).
	b.WriteString(renderCollapsedDelta(row))
	b.WriteString(`</div>`)
	if len(row.Series) > 0 {
		b.WriteString(`<div class="sparkline-row-chart">`)
		b.WriteString(renderChartArea(row.Series))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// renderCollapsedDelta returns the second line of a collapsed sparkline row:
// "↔ X.XX%" for two-series rows (the spread between BID and ASK), "Δ X.XX%"
// for single-series rows (period change), or "no data" when no series are
// present. Both indicators are single-glyph prefixes so the row keeps a
// consistent visual rhythm — a font-style icon next to the value, never a
// long word label.
func renderCollapsedDelta(row dto.MeChartPairRow) string {
	if len(row.Series) == 0 {
		return `<div class="sparkline-row-delta sparkline-row-delta-empty">no data</div>`
	}
	if row.SpreadPct != nil && len(row.Series) >= 2 {
		return fmt.Sprintf(
			`<div class="sparkline-row-delta">%s %s</div>`,
			spreadGlyph,
			formatSpreadPct(*row.SpreadPct),
		)
	}
	// Single-series, or two-series without a SpreadPct: use the first
	// non-sparse series for the delta, falling back to the first series.
	sr := row.Series[0]
	for _, s := range row.Series {
		if !s.Sparse {
			sr = s
			break
		}
	}
	deltaStr := formatSparklineDelta(sr.DeltaPct, sr.Sparse)
	deltaColor := ratepair.ColorDeltaUp
	if sr.DeltaPct < 0 {
		deltaColor = ratepair.ColorDeltaDown
	}
	return fmt.Sprintf(
		`<div class="sparkline-row-delta" style="color:%s">Δ %s</div>`,
		dom.Escape(deltaColor), deltaStr,
	)
}

// renderValueLine builds the value + delta text line for a pair row.
// Two-series rows use compact single-char prefixes B/A colored in role colors.
// Single-series rows use full BID/ASK prefix.
func renderValueLine(series []dto.MeChartSeries) string {
	if len(series) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="sparkline-value-line">`)
	for i, sr := range series {
		if i > 0 {
			b.WriteString(`<span class="sparkline-value-sep"> </span>`)
		}
		prefix, color := seriesPrefixAndColor(sr.Kind, len(series) > 1)
		deltaStr := formatSparklineDelta(sr.DeltaPct, sr.Sparse)
		deltaColor := ratepair.ColorDeltaUp
		if sr.DeltaPct < 0 {
			deltaColor = ratepair.ColorDeltaDown
		}
		fmt.Fprintf(&b,
			`<span class="sparkline-series-value"><span class="sparkline-series-prefix" style="color:%s">%s</span> %.2f <span class="sparkline-value-delta" style="color:%s">%s</span></span>`,
			dom.Escape(color), dom.Escape(prefix),
			sr.Latest,
			dom.Escape(deltaColor), dom.Escape(deltaStr),
		)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// seriesPrefixAndColor returns the display prefix ("B"/"A" for two-series rows,
// "BID"/"ASK" for single-series rows) and the role color for the prefix glyph.
func seriesPrefixAndColor(kind string, compact bool) (prefix, color string) {
	if kind == "BID" {
		if compact {
			return "B", ratepair.ColorBid
		}
		return "BID", ratepair.ColorBid
	}
	if compact {
		return "A", ratepair.ColorAsk
	}
	return "ASK", ratepair.ColorAsk
}

// renderChartArea returns the SVG (or no-data badge) for the pair's chart area.
// It builds a single Y-frame fitting all series values, draws one polyline per
// series, and adds an end-of-line halo on each.
//
// Both-sparse / no-data: if every series has Latest==0 and no Points, render a
// "no data" badge. Mixed-sparse: draw the sparse direction as a flat horizontal
// line at its Latest in its role color.
func renderChartArea(series []dto.MeChartSeries) string {
	if len(series) == 0 {
		return ""
	}

	// Check no-data: all series are no-data (zero Latest and no Points).
	allNoData := true
	for _, sr := range series {
		if sr.Latest != 0 || len(sr.Points) > 0 {
			allNoData = false
			break
		}
	}
	if allNoData {
		return `<span class="sparkline-no-data">no data</span>`
	}

	// Compute the union min/max across all series (excluding flat-zero no-data series).
	minV, maxV := 0.0, 0.0
	first := true
	for _, sr := range series {
		if sr.Latest == 0 && len(sr.Points) == 0 {
			continue
		}
		if sr.Sparse {
			// Sparse series: single effective value is Latest.
			if first {
				minV = sr.Latest
				maxV = sr.Latest
				first = false
			} else {
				if sr.Latest < minV {
					minV = sr.Latest
				}
				if sr.Latest > maxV {
					maxV = sr.Latest
				}
			}
			continue
		}
		for _, pt := range sr.Points {
			if first {
				minV = pt.Value
				maxV = pt.Value
				first = false
			} else {
				if pt.Value < minV {
					minV = pt.Value
				}
				if pt.Value > maxV {
					maxV = pt.Value
				}
			}
		}
	}

	var svgBody strings.Builder
	svgBody.WriteString(renderSparklineBaseline())

	for _, sr := range series {
		if sr.Latest == 0 && len(sr.Points) == 0 {
			// No-data series within a mixed row: skip entirely.
			continue
		}
		if sr.Sparse {
			svgBody.WriteString(renderFlatLine(sr.Color, sr.Latest, minV, maxV))
		} else {
			svgBody.WriteString(renderPolyline(sr, minV, maxV))
		}
	}

	return fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" style="flex:1;min-width:0;height:auto;">%s</svg>`,
		svgW, svgH,
		svgBody.String(),
	)
}

// renderFlatLine draws a horizontal line at the given value's Y position in the
// given color. Used for sparse series in a mixed row.
func renderFlatLine(color string, value, minV, maxV float64) string {
	y := svgYForValue(value, minV, maxV)
	return fmt.Sprintf(
		`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="2" stroke-linecap="round"/>`,
		svgXFirst, y, svgXLast, y, dom.Escape(color),
	)
}

// renderPolyline draws a multi-point polyline plus end-of-line halo for a
// non-sparse series. Falls back to a flat line when fewer than two points exist
// (belt-and-suspenders for the min/max equality case).
func renderPolyline(sr dto.MeChartSeries, minV, maxV float64) string {
	if len(sr.Points) < 2 {
		return renderFlatLine(sr.Color, sr.Latest, minV, maxV)
	}

	n := len(sr.Points)
	var coordsBuilder strings.Builder
	for i, pt := range sr.Points {
		if i > 0 {
			coordsBuilder.WriteByte(' ')
		}
		x := svgXFirst + float64(i)/float64(n-1)*(svgXLast-svgXFirst)
		y := svgYForValue(pt.Value, minV, maxV)
		fmt.Fprintf(&coordsBuilder, "%.1f,%.1f", x, y)
	}

	lastPt := sr.Points[len(sr.Points)-1]
	ex := svgXLast
	ey := svgYForValue(lastPt.Value, minV, maxV)

	polyline := fmt.Sprintf(
		`<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" points="%s"/>`,
		dom.Escape(sr.Color),
		coordsBuilder.String(),
	)
	haloOuter := fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="6" fill="%s" opacity="0.2"/>`, ex, ey, dom.Escape(sr.Color))
	haloInner := fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="2.8" fill="%s"/>`, ex, ey, dom.Escape(sr.Color))

	return polyline + haloOuter + haloInner
}

// renderSparklineBaseline returns the dashed baseline SVG element drawn at
// svgYMax (the bottom of the drawing area) as a visual anchor.
func renderSparklineBaseline() string {
	return fmt.Sprintf(
		`<line x1="0" y1="%.1f" x2="%d" y2="%.1f" stroke="var(--tg-theme-hint-color,#888)" stroke-width="0.5" stroke-dasharray="3,3"/>`,
		svgYMax, svgW, svgYMax,
	)
}

// svgYForValue maps a price value onto the SVG y-axis (top-down, so higher
// values map to smaller y). When maxV == minV the series is flat; all points
// are placed at mid-height to avoid a division by zero.
func svgYForValue(v, minV, maxV float64) float64 {
	if maxV == minV {
		return (svgYMin + svgYMax) / 2
	}
	return svgYMax - (v-minV)/(maxV-minV)*(svgYMax-svgYMin)
}

// formatSparklineDelta formats a percent-change value for display.
// When sparse is true, the value is forced to "+0.0%" per the plan's spec.
func formatSparklineDelta(v float64, sparse bool) string {
	if sparse {
		return "+0.0%"
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if v >= 0 {
		return "+" + s + "%"
	}
	return s + "%"
}

// formatSpreadPct formats the relative spread as "X.XX%".
func formatSpreadPct(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64) + "%"
}

// spreadGlyph is the single-character prefix used in both the list row and the
// modal to denote bid/ask spread. The double-headed arrow ↔ (U+2194) renders
// as a font glyph in every Telegram client we target and visually pairs with
// the Δ used for single-series rows.
const spreadGlyph = "↔"
