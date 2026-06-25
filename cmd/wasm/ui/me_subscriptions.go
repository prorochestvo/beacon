// Package ui provides HTML renderers for the WASM frontend. This file handles
// the Mini App subscriptions screen: a sparkline-list chart slot followed by a
// modal slot. Per-pair detail is surfaced through the modal overlay (the old
// list section, search bar, and toggle button are gone).
//
// The modal is text-only (no SVG). When state.HistoryOpen is true the modal
// body swaps to the per-pair history view rendered by me_pair_history.go.
//
// Layout when AuthFailure is false:
//  1. #me-sparkline-chart — sparkline-list chart (skeleton / empty / rendered).
//  2. #me-pair-modal-slot — pair detail overlay (empty unless OpenPair is set).
//
// AuthFailure short-circuits to just the error message.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/cmd/wasm/dom"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// authFailureMsg is the exact copy from subscriptions.html.
// The empty-initData path and the 401-response path both render this message.
const authFailureMsg = "This page must be opened from the bot&#39;s button. Please reopen via the bot."

// meManageGearSVG is the inline SVG gear icon for the manage-subscriptions
// button. Viewbox 24×24 px; rendered at 20×20 px via CSS.
const meManageGearSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" focusable="false">` +
	`<path d="M19.14 12.94c.04-.3.06-.61.06-.94s-.02-.64-.07-.94l2.03-1.58a.49.49 0 0 0 .12-.61l-1.92-3.32a.49.49 0 0 0-.59-.22l-2.39.96a7.01 7.01 0 0 0-1.62-.94l-.36-2.54A.484.484 0 0 0 14 2h-4c-.25 0-.46.18-.49.42l-.36 2.54a7.01 7.01 0 0 0-1.62.94l-2.39-.96a.48.48 0 0 0-.59.22L2.63 8.48a.48.48 0 0 0 .12.61l2.03 1.58A7.2 7.2 0 0 0 4.71 12c0 .32.03.63.07.94l-2.03 1.58a.49.49 0 0 0-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.36 1.04.67 1.62.94l.36 2.54c.05.24.26.42.49.42h4c.25 0 .46-.18.49-.42l.36-2.54a7.01 7.01 0 0 0 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32a.49.49 0 0 0-.12-.61l-2.01-1.58zM12 15.6A3.6 3.6 0 0 1 8.4 12 3.6 3.6 0 0 1 12 8.4a3.6 3.6 0 0 1 3.6 3.6 3.6 3.6 0 0 1-3.6 3.6z"/>` +
	`</svg>`

// RenderMeSubscriptions returns the full HTML for the Mini App subscriptions
// screen. Default top-to-bottom layout:
//  1. #me-sparkline-chart — sparkline-list chart (skeleton / empty / rendered).
//  2. #me-pair-modal-slot — pair detail overlay (empty unless OpenPair is set).
//
// The manage-subscriptions gear button is absolutely positioned top-right of
// #app via CSS, so it floats over the chart-card header and adds nothing to the
// vertical flow. It is NOT rendered on the guest screen or when AuthFailure is
// set; AuthFailure short-circuits the whole screen to the auth-failure message.
//
// Every user-influenced field is passed through dom.Escape before interpolation.
func RenderMeSubscriptions(state application.MeSubscriptionsState) string {
	if state.AuthFailure {
		return fmt.Sprintf(`<p class="error-msg">%s</p>`, authFailureMsg)
	}

	var b strings.Builder
	b.WriteString(`<button id="me-manage" class="me-manage-gear" type="button" aria-label="Manage subscriptions">`)
	b.WriteString(meManageGearSVG)
	b.WriteString(`</button>`)
	b.WriteString(`<div id="me-sparkline-chart">`)
	b.WriteString(renderSparklineSlot(state))
	b.WriteString(`</div>`)
	b.WriteString(`<div id="me-pair-modal-slot">`)
	b.WriteString(RenderPairModal(state))
	b.WriteString(`</div>`)
	return b.String()
}

// RenderSparklineSlot returns the HTML content for the #me-sparkline-chart div.
// Exported so main.go can update the chart slot in-place after the async fetch
// without re-rendering the whole page.
func RenderSparklineSlot(state application.MeSubscriptionsState) string {
	return renderSparklineSlot(state)
}

// RenderPairModal returns the HTML for the open-pair detail overlay, or the
// empty string when state.OpenPair is nil or the chart data is missing. The
// output is a self-contained HTML fragment safe for innerHTML assignment into
// #me-pair-modal-slot.
//
// When state.HistoryOpen is true the modal body holds the history view
// (RenderPairHistory) instead of the per-series detail sheet. The modal card
// chrome (backdrop, header, close button) is always present.
func RenderPairModal(state application.MeSubscriptionsState) string {
	if state.OpenPair == nil {
		return ""
	}
	row, ok := findChartRowByPair(state.Chart, *state.OpenPair)
	if !ok {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b,
		`<div class="me-pair-modal" id="me-pair-modal" role="dialog" aria-modal="true" aria-labelledby="me-pair-modal-title" data-pair="%s">`,
		dom.Escape(row.Pair),
	)
	b.WriteString(`<div class="me-pair-modal-backdrop" id="me-pair-modal-backdrop"></div>`)
	b.WriteString(`<div class="me-pair-modal-card">`)
	b.WriteString(`<div class="me-pair-modal-header">`)
	fmt.Fprintf(&b, `<h2 class="me-pair-modal-title" id="me-pair-modal-title">%s</h2>`, dom.Escape(row.Pair))
	b.WriteString(`<button class="me-pair-modal-close" id="me-pair-modal-close" type="button" aria-label="Close">&#10005;</button>`)
	b.WriteString(`</div>`)

	b.WriteString(`<div class="me-pair-modal-body">`)
	if state.HistoryOpen {
		b.WriteString(RenderPairHistory(state))
	} else {
		pt, hasGrab := latestPointAcrossSeries(row.Series)
		var grabTime time.Time
		if hasGrab {
			grabTime = pt.Timestamp
		}
		b.WriteString(renderModalDetailBody(row, grabTime, hasGrab))
	}
	b.WriteString(`</div>`) // me-pair-modal-body
	b.WriteString(`</div>`) // me-pair-modal-card
	b.WriteString(`</div>`) // me-pair-modal
	return b.String()
}

// renderModalDetailBody returns the text-only detail view rendered inside the
// modal card when HistoryOpen is false: per-series value lines, an optional
// spread line, an optional last-grab line, and the History action button.
// Subscription-condition badges live elsewhere — per-pair conditions are
// managed on a dedicated screen, not in the read-only detail modal.
func renderModalDetailBody(row dto.MeChartPairRow, lastGrab time.Time, hasGrab bool) string {
	var b strings.Builder

	// One text block per direction (no SVG — the SVG lives on the list row).
	for _, sr := range row.Series {
		b.WriteString(`<div class="me-pair-modal-series">`)
		b.WriteString(renderValueLine([]dto.MeChartSeries{sr}))
		b.WriteString(`</div>`)
	}

	// Spread line: present when the server gave SpreadPct for a two-series row.
	// The len(row.Series) >= 2 guard mirrors renderCollapsedDelta so a
	// single-series row with a stray SpreadPct never renders a lone spread line.
	if row.SpreadPct != nil && len(row.Series) >= 2 {
		fmt.Fprintf(&b,
			`<div class="me-pair-modal-spread">%s %s</div>`,
			spreadGlyph,
			formatSpreadPct(*row.SpreadPct),
		)
	}

	// Last-grab line: max timestamp across all series.
	if hasGrab {
		fmt.Fprintf(&b,
			`<div class="me-pair-modal-time">Last grab: %s</div>`,
			dom.Escape(fmtDate(lastGrab.Format(time.RFC3339))),
		)
	}

	// History action button at the bottom of the detail view.
	b.WriteString(`<div class="me-pair-modal-actions">`)
	b.WriteString(`<button class="me-pair-modal-history" id="me-pair-modal-history" type="button">History</button>`)
	b.WriteString(`</div>`)

	return b.String()
}

// renderSparklineSlot returns the content for the #me-sparkline-chart div.
func renderSparklineSlot(state application.MeSubscriptionsState) string {
	if state.ChartError != nil {
		return `<p class="sparkline-error">Chart unavailable</p>`
	}
	if state.ChartLoading && state.Chart == nil {
		return `<div class="sparkline-skeleton"></div>`
	}
	if state.Chart == nil {
		return `<div class="sparkline-empty">No chart data yet.</div>`
	}
	return RenderSparklineListForPeriod(*state.Chart, state.Period)
}

// findChartRowByPair returns the MeChartPairRow whose Pair field equals pair.
// Returns the zero value and false when chart is nil or no match is found.
func findChartRowByPair(chart *dto.MeChartResponse, pair string) (dto.MeChartPairRow, bool) {
	if chart == nil {
		return dto.MeChartPairRow{}, false
	}
	for _, row := range chart.Pairs {
		if row.Pair == pair {
			return row, true
		}
	}
	return dto.MeChartPairRow{}, false
}

// latestPointAcrossSeries returns the MeChartPoint with the maximum Timestamp
// across all non-empty series. Returns ok=false when every series has no points.
func latestPointAcrossSeries(series []dto.MeChartSeries) (dto.MeChartPoint, bool) {
	var latest dto.MeChartPoint
	found := false
	for _, sr := range series {
		for _, pt := range sr.Points {
			if !found || pt.Timestamp.After(latest.Timestamp) {
				latest = pt
				found = true
			}
		}
	}
	return latest, found
}
