package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/cmd/wasm/dom"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// RenderPublicSubscriptions returns the full HTML for the unauthenticated guest
// landing page. Layout:
//  1. #public-sparkline-chart — sparkline-list (skeleton / empty / rendered).
//  2. #public-pagination — pagination control (empty when not needed).
//  3. #public-pair-modal-slot — pair detail overlay (empty unless OpenPair is set).
//
// Every user-influenced field is passed through dom.Escape before interpolation.
func RenderPublicSubscriptions(state application.PublicSubscriptionsState) string {
	var b strings.Builder
	b.WriteString(`<div id="public-sparkline-chart">`)
	b.WriteString(RenderPublicSparklineSlot(state))
	b.WriteString(`</div>`)
	b.WriteString(`<div id="public-pagination">`)
	b.WriteString(RenderPublicPagination(state))
	b.WriteString(`</div>`)
	b.WriteString(`<div id="public-pair-modal-slot">`)
	b.WriteString(RenderPublicPairModal(state))
	b.WriteString(`</div>`)
	return b.String()
}

// RenderPublicSparklineSlot returns the HTML content for the
// #public-sparkline-chart div. Exported so main.go can update the chart slot
// in-place after the async fetch completes without re-rendering the page.
func RenderPublicSparklineSlot(state application.PublicSubscriptionsState) string {
	if state.ChartError != nil {
		return `<p class="sparkline-error">Chart unavailable</p>`
	}
	if state.ChartLoading && state.Chart == nil {
		return `<div class="sparkline-skeleton"></div>`
	}
	if state.Chart == nil {
		return `<div class="sparkline-empty">No chart data yet.</div>`
	}
	return renderPublicSparklineList(*state.Chart, state.Period)
}

// RenderPublicPagination returns the pagination control HTML for the public
// sparkline list. Uses the generic RenderPagination helper. Returns an empty
// string when no pagination is needed.
func RenderPublicPagination(state application.PublicSubscriptionsState) string {
	if state.Chart == nil {
		return ""
	}
	limit := state.Limit
	if limit < 1 {
		limit = application.PublicChartDefaultLimit
	}
	return RenderPagination(PaginationState{
		Page:    state.Page,
		Count:   len(state.Chart.Pairs),
		Limit:   limit,
		Section: "public",
	})
}

// RenderPublicPairModal returns the HTML for the open-pair detail overlay, or
// an empty string when state.OpenPair is nil or the chart data is missing. The
// public modal is a slimmed-down variant of RenderPairModal: it shows the
// per-series value cards and the close button, but intentionally omits the
// "View history" button because the history endpoint is auth-gated and there
// is no public equivalent.
//
// The output is a self-contained HTML fragment safe for innerHTML assignment
// into #public-pair-modal-slot.
func RenderPublicPairModal(state application.PublicSubscriptionsState) string {
	if state.OpenPair == nil {
		return ""
	}
	if state.Chart == nil {
		return ""
	}
	var row dto.MeChartPairRow
	found := false
	for _, r := range state.Chart.Pairs {
		if r.Pair == *state.OpenPair {
			row = r
			found = true
			break
		}
	}
	if !found {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b,
		`<div class="me-pair-modal" id="public-pair-modal" role="dialog" aria-modal="true" aria-labelledby="public-pair-modal-title" data-pair="%s">`,
		dom.Escape(row.Pair),
	)
	b.WriteString(`<div class="me-pair-modal-backdrop" id="public-pair-modal-backdrop"></div>`)
	b.WriteString(`<div class="me-pair-modal-card">`)
	b.WriteString(`<div class="me-pair-modal-header">`)
	fmt.Fprintf(&b, `<h2 class="me-pair-modal-title" id="public-pair-modal-title">%s</h2>`, dom.Escape(row.Pair))
	b.WriteString(`<button class="me-pair-modal-close" id="public-pair-modal-close" type="button" aria-label="Close">&#10005;</button>`)
	b.WriteString(`</div>`)

	b.WriteString(`<div class="me-pair-modal-body">`)

	// One text block per direction (no SVG — the SVG lives on the list row).
	for _, sr := range row.Series {
		b.WriteString(`<div class="me-pair-modal-series">`)
		b.WriteString(renderValueLine([]dto.MeChartSeries{sr}))
		b.WriteString(`</div>`)
	}

	// Spread line: present when SpreadPct is available for a two-series row.
	if row.SpreadPct != nil && len(row.Series) >= 2 {
		fmt.Fprintf(&b,
			`<div class="me-pair-modal-spread">Spread %s</div>`,
			formatSpreadPct(*row.SpreadPct),
		)
	}

	// Last-grab line: max timestamp across all series.
	pt, hasGrab := latestPointAcrossSeries(row.Series)
	if hasGrab {
		var grabTime time.Time
		grabTime = pt.Timestamp
		fmt.Fprintf(&b,
			`<div class="me-pair-modal-time">Last grab: %s</div>`,
			dom.Escape(fmtDate(grabTime.Format(time.RFC3339))),
		)
	}

	// Note: no "View history" button — the history endpoint is auth-gated and
	// has no public equivalent. Adding it would require initData, which the
	// guest path does not have.

	b.WriteString(`</div>`) // me-pair-modal-body
	b.WriteString(`</div>`) // me-pair-modal-card
	b.WriteString(`</div>`) // me-pair-modal
	return b.String()
}

// renderPublicSparklineList returns the full HTML for the public sparkline-list
// view. Mirrors RenderSparklineListForPeriod but accepts a PublicChartResponse
// and the active period value instead of MeChartResponse.
func renderPublicSparklineList(chart dto.PublicChartResponse, period int) string {
	// Reuse RenderSparklineListForPeriod by converting to MeChartResponse.
	// Both types share the same Pairs type ([]MeChartPairRow), Window string,
	// and rendering logic. The conversion is zero-allocation at the call site
	// because MeChartResponse.Pairs points to the same backing array.
	return RenderSparklineListForPeriod(dto.MeChartResponse{
		Window: chart.Window,
		Pairs:  chart.Pairs,
	}, period)
}
