package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/dom"
	"github.com/seilbekskindirov/beacon/internal/domain/ratepair"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// historyGenericErrorMsg is shown when a history fetch fails with a non-auth error.
const historyGenericErrorMsg = "Could not load history. Try again."

// RenderPairHistory returns the HTML fragment for the history view rendered
// inside the modal card. It assumes state.OpenPair is non-nil and
// state.HistoryOpen is true; the caller guards those.
//
// Layout: one of loading skeleton (HistoryLoading), error block
// (HistoryError != nil), empty-state, or the entries list; then a pagination
// row (prev / page indicator / next).
//
// "Back to detail" is handled by the modal header X (id="me-pair-modal-close"),
// which branches on HistoryOpen in the delegated click handler. There is no
// inline back button.
//
// Each history entry renders as:
//
//	<div class="me-pair-history-entry">
//	  <div class="me-pair-history-head"><time>...</time> · <span>SourceTitle</span></div>
//	  <div class="me-pair-history-bid">BID 487.21 <span class="delta">+0.12%</span></div>
//	  <div class="me-pair-history-ask">ASK 489.05 <span class="delta">-0.05%</span></div>
//	</div>
//
// Rows are separated by a bottom border on .me-pair-history-entry. A row
// whose Bid is nil omits the BID line; same for Ask.
func RenderPairHistory(state application.MeSubscriptionsState) string {
	var b strings.Builder

	b.WriteString(renderHistorySourceFilter(state))

	switch {
	case state.HistoryLoading:
		b.WriteString(`<div class="me-pair-history-loading">Loading…</div>`)
	case state.HistoryError != nil:
		msg := historyGenericErrorMsg
		if strings.Contains(state.HistoryError.Error(), application.AuthFailureSentinel) {
			msg = authFailureMsg
		}
		fmt.Fprintf(&b, `<div class="me-pair-history-error">%s</div>`, msg)
	case len(state.HistoryItems) == 0:
		emptyMsg := "No history yet."
		if state.SelectedSourceTitle != "" {
			emptyMsg = "No history for this source."
		}
		fmt.Fprintf(&b, `<div class="me-pair-history-empty">%s</div>`, emptyMsg)
	default:
		b.WriteString(`<div class="me-pair-history-list">`)
		for _, row := range state.HistoryItems {
			b.WriteString(renderHistoryEntry(row))
		}
		b.WriteString(`</div>`)
	}

	b.WriteString(renderHistoryPagination(state))
	return b.String()
}

// renderHistoryEntry returns the HTML fragment for one history row.
//
// Layout example for a BID/ASK provider:
//
//	<div class="me-pair-history-entry">
//	  <div class="me-pair-history-head"><time>...</time> · <span>SourceTitle</span></div>
//	  <div class="me-pair-history-bid">BID 487.21 <span class="delta">+0.12%</span></div>
//	  <div class="me-pair-history-ask">ASK 489.05 <span class="delta">-0.05%</span></div>
//	</div>
//
// For equity (LAST-kind) sources, the BID and ASK lines are absent and the LAST
// line appears instead:
//
//	<div class="me-pair-history-last">LAST 230.50 <span class="delta">+0.42%</span></div>
func renderHistoryEntry(row dto.MeHistoryRow) string {
	var b strings.Builder
	b.WriteString(`<div class="me-pair-history-entry">`)
	fmt.Fprintf(&b,
		`<div class="me-pair-history-head"><time>%s</time> · <span>%s</span></div>`,
		dom.Escape(fmtDate(row.Timestamp.Format("2006-01-02T15:04:05Z07:00"))),
		dom.Escape(row.SourceTitle),
	)
	if row.Bid != nil {
		fmt.Fprintf(&b,
			`<div class="me-pair-history-bid">BID %s %s</div>`,
			dom.Escape(strconv.FormatFloat(*row.Bid, 'f', -1, 64)),
			renderHistoryDelta(row.BidDeltaPct),
		)
	}
	if row.Ask != nil {
		fmt.Fprintf(&b,
			`<div class="me-pair-history-ask">ASK %s %s</div>`,
			dom.Escape(strconv.FormatFloat(*row.Ask, 'f', -1, 64)),
			renderHistoryDelta(row.AskDeltaPct),
		)
	}
	if row.Last != nil {
		fmt.Fprintf(&b,
			`<div class="me-pair-history-last">LAST %s %s</div>`,
			dom.Escape(strconv.FormatFloat(*row.Last, 'f', -1, 64)),
			renderHistoryDelta(row.LastDeltaPct),
		)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// renderHistoryDelta returns a <span class="delta"> element for a delta
// percent pointer. Nil delta renders an em-dash with neutral (hint) color.
// Positive delta uses ColorDeltaUp; negative uses ColorDeltaDown.
func renderHistoryDelta(delta *float64) string {
	if delta == nil {
		return `<span class="delta" style="color:var(--tg-theme-hint-color,#888)">&#8212;</span>`
	}
	color := ratepair.ColorDeltaUp
	if *delta < 0 {
		color = ratepair.ColorDeltaDown
	}
	sign := "+"
	if *delta < 0 {
		sign = ""
	}
	return fmt.Sprintf(
		`<span class="delta" style="color:%s">%s%.2f%%</span>`,
		dom.Escape(color),
		sign,
		*delta,
	)
}

// renderHistorySourceFilter returns the source-filter chip row, or an empty
// string when KnownSources has fewer than two entries (a single-source filter
// is noise). The "All" chip is first; source chips follow sorted by provider
// title for deterministic rendering. The chip matching SelectedSourceTitle
// carries me-pair-history-source-chip-active. Both data-source values and chip
// text are the provider title, HTML-escaped via dom.Escape.
func renderHistorySourceFilter(state application.MeSubscriptionsState) string {
	if len(state.KnownSources) < 2 {
		return ""
	}

	titles := make([]string, 0, len(state.KnownSources))
	for t := range state.KnownSources {
		titles = append(titles, t)
	}
	sort.Strings(titles)

	var b strings.Builder
	b.WriteString(`<div class="me-pair-history-source-filter">`)

	allActive := ""
	if state.SelectedSourceTitle == "" {
		allActive = " me-pair-history-source-chip-active"
	}
	fmt.Fprintf(&b,
		`<button class="me-pair-history-source-chip%s" id="me-pair-history-source-all" data-source="" type="button">All</button>`,
		allActive,
	)

	for _, title := range titles {
		active := ""
		if state.SelectedSourceTitle == title {
			active = " me-pair-history-source-chip-active"
		}
		fmt.Fprintf(&b,
			`<button class="me-pair-history-source-chip%s" data-source="%s" type="button">%s</button>`,
			active,
			dom.Escape(title),
			dom.Escape(title),
		)
	}

	b.WriteString(`</div>`)
	return b.String()
}

// renderHistoryPagination returns the pagination row for the history view.
// Prev is disabled when HistoryPage <= 1. Next is disabled when
// HistoryPage * HistoryLimit >= HistoryTotal.
func renderHistoryPagination(state application.MeSubscriptionsState) string {
	limit := state.HistoryLimit
	if limit <= 0 {
		limit = application.MeHistoryDefaultLimit
	}

	atFirst := state.HistoryPage <= 1
	atLast := int64(state.HistoryPage*limit) >= state.HistoryTotal

	var prevBtn, nextBtn string
	if atFirst {
		prevBtn = `<button class="me-pair-history-prev" id="me-pair-history-prev" type="button" disabled>&#8592; Prev</button>`
	} else {
		prevBtn = `<button class="me-pair-history-prev" id="me-pair-history-prev" type="button">&#8592; Prev</button>`
	}
	if atLast {
		nextBtn = `<button class="me-pair-history-next" id="me-pair-history-next" type="button" disabled>Next &#8594;</button>`
	} else {
		nextBtn = `<button class="me-pair-history-next" id="me-pair-history-next" type="button">Next &#8594;</button>`
	}

	pageLabel := fmt.Sprintf(`<span class="me-pair-history-page">%d</span>`, state.HistoryPage)
	return fmt.Sprintf(
		`<div class="me-pair-history-pagination">%s%s%s</div>`,
		prevBtn, pageLabel, nextBtn,
	)
}
