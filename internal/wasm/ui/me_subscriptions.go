package ui

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/wasm/dom"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

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

func renderSearchBar(currentQuery string) string {
	return fmt.Sprintf(
		`<input class="search-bar" id="me-search" type="text" placeholder="Search subscriptions..." value="%s">`,
		dom.Escape(currentQuery),
	)
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
		b.WriteString(renderMeSubCard(item))
	}
	return b.String()
}

func renderMeSubCard(item api.MeSubscriptionRow) string {
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
	b.WriteString(`<div class="card">`)
	b.WriteString(fmt.Sprintf(`<div class="card-title">%s</div>`, dom.Escape(title)))

	if item.BaseCurrency != "" && item.QuoteCurrency != "" {
		pair := item.BaseCurrency + "/" + item.QuoteCurrency
		b.WriteString(fmt.Sprintf(`<div class="card-pair">%s</div>`, dom.Escape(pair)))
	}

	b.WriteString(fmt.Sprintf(`<div class="card-price">%s</div>`, dom.Escape(price)))
	b.WriteString(fmt.Sprintf(`<div class="card-time">Last grab: %s</div>`, dom.Escape(ts)))

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

func renderMeSubsPagination(state application.MeSubscriptionsState) string {
	ps := PaginationState{
		Page:    state.Page,
		Count:   len(state.Items),
		Limit:   state.PageSize,
		Section: "me-subs",
	}
	return RenderPagination(ps)
}
