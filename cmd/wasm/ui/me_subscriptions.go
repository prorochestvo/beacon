package ui

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/cmd/wasm/dom"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// defaultSparklineOpts are the viewBox dimensions used for all card sparklines.
// Width/Height define coordinate space only; CSS controls the rendered size.
var defaultSparklineOpts = SparklineOptions{Width: 100, Height: 30}

// authFailureMsg is the exact copy from subscriptions.html lines 85 and 121.
// Both the empty-initData path and the 401-response path render this message.
const authFailureMsg = "This page must be opened from the bot&#39;s button. Please reopen via the bot."

// RenderMeSubscriptions returns the full HTML for the Mini App subscriptions
// screen. The search bar is always rendered; the content area below it depends
// on the state:
//   - AuthFailure → auth-failure message (no pagination)
//   - LastError non-nil → generic error message (no pagination)
//   - Items empty, no error → "No subscriptions found." empty-state
//   - Items non-empty → card list + pagination when applicable
//
// The pagination wrapper div (id="me-subs-pagination") is always present in
// the output so that subsequent in-place updates via getElementById can find
// it. Its inner content is empty when auth-failed or errored.
//
// Every user-influenced field (source_title, pair, conditions) is passed
// through dom.Escape before interpolation.
func RenderMeSubscriptions(state application.MeSubscriptionsState) string {
	var b strings.Builder
	b.WriteString(renderSearchBar(state.Query))
	b.WriteString(renderPeriodToggle(state.Period))
	b.WriteString(`<div id="me-subs-list">`)
	b.WriteString(renderMeSubsContent(state))
	b.WriteString(`</div>`)
	b.WriteString(`<div id="me-subs-pagination">`)
	if !state.AuthFailure && state.LastError == nil {
		b.WriteString(renderMeSubsPagination(state))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// RenderMeSubsList returns only the inner content HTML so the DOM can be
// updated in-place without re-rendering the search bar (avoids losing input
// focus) or the pagination (which lives outside the list div).
func RenderMeSubsList(state application.MeSubscriptionsState) string {
	return renderMeSubsContent(state)
}

// RenderMeSubsPagination returns only the pagination HTML. Returns empty string
// when the state signals an auth-failure or a generic error, because pagination
// must not be shown in either error case.
func RenderMeSubsPagination(state application.MeSubscriptionsState) string {
	if state.AuthFailure || state.LastError != nil {
		return ""
	}
	return renderMeSubsPagination(state)
}

// RenderMeSubCardChartSlot returns the inner HTML for a single card's chart
// slot div (id="card-chart-{name}"). It is called by the chart goroutines in
// main.go to update only their own slot without re-rendering the whole list.
func RenderMeSubCardChartSlot(state application.MeSubscriptionsState, name string) string {
	return renderChartSlot(state, name)
}

func renderSearchBar(currentQuery string) string {
	return fmt.Sprintf(
		`<input class="search-bar" id="me-search" type="text" placeholder="Search subscriptions..." value="%s">`,
		dom.Escape(currentQuery),
	)
}

// renderPeriodToggle renders three period-selector buttons. The active period
// gets class "active". Buttons carry only data-period (no data-section) so
// main.go matches by element id.
func renderPeriodToggle(current string) string {
	var b strings.Builder
	b.WriteString(`<div class="period-toggle" id="me-period-toggle">`)
	for _, p := range []string{
		application.MeSubscriptionsPeriodWeek,
		application.MeSubscriptionsPeriodMonth,
		application.MeSubscriptionsPeriodYear,
	} {
		cls := "period-btn"
		if p == current {
			cls = "period-btn active"
		}
		fmt.Fprintf(&b, `<button class="%s" data-period="%s">%s</button>`,
			cls, p, dom.Escape(p))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func renderMeSubsContent(state application.MeSubscriptionsState) string {
	if state.AuthFailure {
		return fmt.Sprintf(`<p class="error-msg">%s</p>`, authFailureMsg)
	}
	if state.LastError != nil {
		return fmt.Sprintf(
			`<p class="error-msg">Error loading subscriptions: %s</p>`,
			dom.Escape(state.LastError.Error()),
		)
	}
	if len(state.Items) == 0 {
		return `<p class="status">No subscriptions found.</p>`
	}
	var b strings.Builder
	for _, item := range state.Items {
		b.WriteString(renderMeSubCard(state, item))
	}
	return b.String()
}

func renderMeSubCard(state application.MeSubscriptionsState, item dto.MeSubscriptionRow) string {
	title := item.SourceTitle
	if title == "" {
		title = item.SourceName
	}

	var price string
	if item.LatestPrice != 0 {
		price = fmt.Sprintf("%.4f", item.LatestPrice)
	} else {
		price = "—"
	}

	ts := fmtDate(item.LatestAt)

	var b strings.Builder
	// data-source-name lets the delegated click handler in main.go identify the card.
	// Source names are admin-controlled kebab-case slugs; escaping is still applied
	// for correctness even though slugs do not contain HTML-special chars in practice.
	fmt.Fprintf(&b, `<div class="card" data-source-name="%s">`, dom.Escape(item.SourceName))
	b.WriteString(fmt.Sprintf(`<div class="card-title">%s</div>`, dom.Escape(title)))

	if item.BaseCurrency != "" && item.QuoteCurrency != "" {
		pair := item.BaseCurrency + "/" + item.QuoteCurrency
		b.WriteString(fmt.Sprintf(`<div class="card-pair">%s</div>`, dom.Escape(pair)))
	}

	b.WriteString(fmt.Sprintf(`<div class="card-price">%s</div>`, dom.Escape(price)))
	b.WriteString(fmt.Sprintf(`<div class="card-time">Last grab: %s</div>`, dom.Escape(ts)))

	// Chart slot: id is used by chart goroutines to redraw only this slot.
	// The slot is tappable; main.go reads data-source-name from the slot to
	// route ToggleExpand without re-rendering the whole list.
	b.WriteString(renderChartSlotWrapper(state, item.SourceName))

	if len(item.Conditions) > 0 {
		b.WriteString(`<div class="badges">`)
		for _, c := range item.Conditions {
			b.WriteString(fmt.Sprintf(`<span class="badge">%s</span>`, dom.Escape(c)))
		}
		b.WriteString(`</div>`)
	}

	b.WriteString(`</div>`)
	return b.String()
}

// renderChartSlotWrapper renders the stable <div id="card-chart-{name}"> wrapper
// and its inner content. The goroutines in main.go replace only the innerHTML of
// this div, so the id must never change between renders.
func renderChartSlotWrapper(state application.MeSubscriptionsState, name string) string {
	return fmt.Sprintf(
		`<div class="card-chart-slot" id="card-chart-%s" data-source-name="%s">%s</div>`,
		dom.Escape(name), dom.Escape(name), renderChartSlot(state, name),
	)
}

// renderChartSlot returns the inner content of the chart slot: skeleton,
// error badge, or the SVG sparkline (optionally followed by the expanded list).
func renderChartSlot(state application.MeSubscriptionsState, name string) string {
	cs, ok := state.Charts[name]
	if !ok || cs.Loading {
		return `<div class="card-chart-skeleton"></div>`
	}
	if cs.Error != nil {
		return `<span class="card-chart-error">chart unavailable</span>`
	}
	if !cs.Loaded {
		return `<div class="card-chart-skeleton"></div>`
	}

	var b strings.Builder
	b.WriteString(RenderSparkline(cs.Points, defaultSparklineOpts))

	if state.Expanded[name] {
		b.WriteString(renderExpandedList(cs.Points))
	}
	return b.String()
}

func renderExpandedList(points []dto.ChartPointResponse) string {
	if len(points) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<ul class="card-chart-expanded">`)
	for _, p := range points {
		fmt.Fprintf(&b,
			`<li><span class="label">%s</span><span class="price">%.4f</span></li>`,
			dom.Escape(p.Label), p.Price,
		)
	}
	b.WriteString(`</ul>`)
	return b.String()
}

func renderMeSubsPagination(state application.MeSubscriptionsState) string {
	ps := PaginationState{
		Page:    state.Page,
		Count:   len(state.Items),
		Limit:   state.PageSize,
		Section: "me-subs",
	}
	return RenderPagination(ps)
}
