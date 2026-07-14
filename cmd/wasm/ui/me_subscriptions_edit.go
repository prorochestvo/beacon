// Package ui provides HTML renderers for the WASM frontend. This file handles
// the subscription editor screen which is split into two logical views
// dispatched by state.ActiveView:
//
//	EditViewList — paginated, searchable list of existing subscriptions plus
//	               a "+ Add" button that switches to the form view.
//	EditViewForm — create form (provider/pair/direction/condition) plus a
//	               Back button that returns to the list view.
//
// The provider and pair pickers replace the original flat <select> that listed
// every (provider, pair) combination at once. They share one CSS class family
// (.me-edit-picker-*) and the same backdrop; only one is open at a time. The
// search input drives a targeted redraw of just the results region so the input
// element survives keystrokes and keeps focus.
//
// AuthFailure short-circuits both views to the auth-failure message.
package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/dom"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// RenderMeSubscriptionsEdit returns the full HTML for the subscription editor
// screen. Content is dispatched by state.ActiveView: list view shows the
// paginated subscription list with search; form view shows the create form.
// AuthFailure and load-error states short-circuit both.
//
// Every user-influenced field is escaped through dom.Escape before interpolation.
func RenderMeSubscriptionsEdit(state application.MeSubscriptionsEditState) string {
	if state.AuthFailure {
		return fmt.Sprintf(`<p class="error-msg">%s</p>`, authFailureMsg)
	}

	var b strings.Builder
	b.WriteString(renderEditTopbar(state))

	if state.Loading {
		b.WriteString(`<p class="me-edit-loading">Loading…</p>`)
		return b.String()
	}
	if state.LoadError != nil {
		b.WriteString(`<p class="error-msg">`)
		b.WriteString(dom.Escape(state.LoadError.Error()))
		b.WriteString(`</p>`)
		return b.String()
	}

	// List view requires explicit opt-in via EditViewList; the zero-value
	// falls through to the form view, keeping pre-split tests working with
	// their zero-valued state literals. Production state is always built with
	// EditViewList by NewMeSubscriptionsEditPage, so users land on the list.
	if state.ActiveView == application.EditViewList {
		b.WriteString(renderEditListView(state))
	} else {
		b.WriteString(renderEditFormView(state))
	}
	return b.String()
}

// renderEditTopbar emits the screen header. The Back button's destination
// depends on the active view: from list view it returns to the main Mini App
// screen; from form view it returns to the list. Routing is done by the click
// dispatcher reading state.ActiveView off the controller — the HTML carries the
// same id either way. The title tracks the view too.
func renderEditTopbar(state application.MeSubscriptionsEditState) string {
	title := "Manage subscriptions"
	if state.ActiveView == application.EditViewForm {
		title = "New subscription"
	}
	return fmt.Sprintf(
		`<div class="me-edit-topbar">`+
			`<button class="me-edit-back" id="me-edit-back" type="button">← Back</button>`+
			`<span class="me-edit-title">%s</span>`+
			`</div>`,
		dom.Escape(title),
	)
}

// renderEditListView returns the list-view body top-to-bottom: section title,
// "+ Add new subscription" CTA, search input, paginated list, pagination row.
// The Add button sits up top so it is reachable without scrolling past a long
// list. The list-area / pagination split inside the slot mirrors the picker
// pattern so the pagination row stays in reach regardless of list length.
func renderEditListView(state application.MeSubscriptionsEditState) string {
	var b strings.Builder
	b.WriteString(`<section class="me-edit-list">`)
	b.WriteString(`<h2 class="me-edit-section-title">Your subscriptions</h2>`)

	b.WriteString(`<div class="me-edit-list-actions">`)
	b.WriteString(`<button class="me-edit-save" id="me-edit-add" type="button">+ Add new subscription</button>`)
	b.WriteString(`</div>`)

	// Search input — always present. Targeted slot redraw preserves caret.
	b.WriteString(fmt.Sprintf(
		`<input class="me-edit-list-search" id="me-edit-list-search" type="text" `+
			`placeholder="Search subscriptions…" value="%s" autocomplete="off">`,
		dom.Escape(state.ListQuery),
	))

	// Results slot — items + pagination. The search-input handler rewrites
	// only this slot, leaving the search input alive.
	b.WriteString(`<div class="me-edit-list-results" id="me-edit-list-results-slot">`)
	b.WriteString(RenderEditListResultsSlot(state))
	b.WriteString(`</div>`)

	b.WriteString(`</section>`)
	return b.String()
}

// RenderEditListResultsSlot emits the filtered + paginated list rows and the
// prev/info/next bar. Exported so the search-input handler can redraw only this
// slot without recreating the search input element.
func RenderEditListResultsSlot(state application.MeSubscriptionsEditState) string {
	items := filterSubscriptionItems(state.Items, state.ListQuery)
	page := state.ListPage
	if page < 1 {
		page = 1
	}
	pageItems, totalPages := paginateSubscriptionItems(items, page, application.SubscriptionListPageSize)
	if page > totalPages && totalPages > 0 {
		page = totalPages
		pageItems, totalPages = paginateSubscriptionItems(items, page, application.SubscriptionListPageSize)
	}

	var b strings.Builder
	b.WriteString(`<div class="me-edit-list-rows">`)
	if len(items) == 0 {
		if state.ListQuery != "" {
			b.WriteString(`<p class="me-edit-empty">No matches.</p>`)
		} else {
			b.WriteString(`<p class="me-edit-empty">No subscriptions yet.</p>`)
		}
	} else {
		b.WriteString(`<ul class="me-edit-item-list">`)
		for _, item := range pageItems {
			b.WriteString(renderEditListItem(item))
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(`</div>`)

	if totalPages <= 1 {
		return b.String()
	}

	prevDisabled := ""
	if page <= 1 {
		prevDisabled = " disabled"
	}
	nextDisabled := ""
	if page >= totalPages {
		nextDisabled = " disabled"
	}
	b.WriteString(`<div class="me-edit-picker-pagination">`)
	b.WriteString(fmt.Sprintf(
		`<button class="me-edit-picker-prev" type="button" data-kind="list" data-page="%d"%s>Prev</button>`,
		page-1, prevDisabled,
	))
	b.WriteString(fmt.Sprintf(
		`<span class="me-edit-picker-page-info">page %d of %d</span>`,
		page, totalPages,
	))
	b.WriteString(fmt.Sprintf(
		`<button class="me-edit-picker-next" type="button" data-kind="list" data-page="%d"%s>Next</button>`,
		page+1, nextDisabled,
	))
	b.WriteString(`</div>`)
	return b.String()
}

// renderEditFormView returns the create-form body: provider + pair pickers,
// optional direction radio group, condition type radios, value input,
// inline error region, and Save / Clear buttons.
func renderEditFormView(state application.MeSubscriptionsEditState) string {
	var b strings.Builder
	b.WriteString(`<section class="me-edit-form">`)
	b.WriteString(renderSourcePickers(state))
	if len(state.PairDirections) >= 2 {
		b.WriteString(renderDirectionRadios(state))
	}

	// Condition type radios.
	b.WriteString(`<div class="me-edit-cond-type-group" role="group" aria-label="Condition type">`)
	for _, ct := range []struct{ val, label string }{
		{"delta", "Delta"},
		{"interval", "Interval"},
		{"daily", "Daily"},
		{"cron", "Cron"},
	} {
		checked := ""
		if ct.val == state.Draft.ConditionType {
			checked = ` checked`
		}
		b.WriteString(fmt.Sprintf(
			`<label class="me-edit-cond-radio"><input type="radio" name="me-edit-cond-type" id="me-edit-cond-%s" value="%s"%s> %s</label>`,
			dom.Escape(ct.val), dom.Escape(ct.val), checked, ct.label,
		))
	}
	b.WriteString(`</div>`)

	placeholder := conditionValuePlaceholder(state.Draft.ConditionType)
	b.WriteString(fmt.Sprintf(
		`<label class="me-edit-label" for="me-edit-value">Value</label>`+
			`<input class="me-edit-input" id="me-edit-value" type="text" value="%s" placeholder="%s">`,
		dom.Escape(state.Draft.ConditionValue),
		dom.Escape(placeholder),
	))
	b.WriteString(`<p class="me-edit-help">`)
	b.WriteString(dom.Escape(conditionValueHelp(state.Draft.ConditionType)))
	b.WriteString(`</p>`)

	if state.FormError != nil {
		b.WriteString(`<p class="me-edit-form-error" id="me-edit-form-error">`)
		b.WriteString(dom.Escape(state.FormError.Error()))
		b.WriteString(`</p>`)
	} else {
		b.WriteString(`<p class="me-edit-form-error" id="me-edit-form-error" hidden></p>`)
	}

	b.WriteString(`<div class="me-edit-buttons">`)
	b.WriteString(`<button class="me-edit-save" id="me-edit-save" type="button">Save</button>`)
	b.WriteString(`<button class="me-edit-cancel" id="me-edit-cancel" type="button">Clear</button>`)
	b.WriteString(`</div>`)
	b.WriteString(`</section>`)
	return b.String()
}

// filterSubscriptionItems returns items whose source title, pair, condition
// type, or condition value contains query (case-insensitive). Empty query
// passes every item through.
func filterSubscriptionItems(items []dto.MeSubscriptionEditRow, query string) []dto.MeSubscriptionEditRow {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	out := make([]dto.MeSubscriptionEditRow, 0, len(items))
	for _, it := range items {
		haystack := strings.ToLower(it.SourceTitle + " " + it.BaseCurrency + "/" + it.QuoteCurrency + " " + it.ConditionType + " " + it.ConditionValue)
		if strings.Contains(haystack, q) {
			out = append(out, it)
		}
	}
	return out
}

// paginateSubscriptionItems returns the slice for the requested 1-based page
// and the total page count. Behaves like paginateStrings / paginateSources.
func paginateSubscriptionItems(items []dto.MeSubscriptionEditRow, page, size int) ([]dto.MeSubscriptionEditRow, int) {
	totalPages := (len(items) + size - 1) / size
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * size
	if start >= len(items) {
		return nil, totalPages
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], totalPages
}

// renderDirectionRadios emits a radio group whose options come from the
// resolved PairDirections of the chosen pair. Labels are derived by the
// application layer; the renderer escapes and prints them. The BID/ASK help
// line shows only when both literal labels appear, so non-BID/ASK schemes don't
// get a misleading explanation.
//
// Each radio carries name="me-edit-direction" and value=<source-name> so the
// change handler routes to SetDraftDirection. No radio is pre-checked until a
// direction is picked — the user must choose explicitly for Save to succeed.
func renderDirectionRadios(state application.MeSubscriptionsEditState) string {
	var b strings.Builder
	b.WriteString(`<label class="me-edit-label">Direction</label>`)
	b.WriteString(`<div class="me-edit-cond-type-group" role="group" aria-label="Pair direction">`)
	hasBID, hasASK := false, false
	for _, d := range state.PairDirections {
		checked := ""
		if d.SourceName == state.Draft.SourceName {
			checked = ` checked`
		}
		label := d.Label
		if label == "" {
			label = "Rate"
		}
		if label == "BID" {
			hasBID = true
		}
		if label == "ASK" {
			hasASK = true
		}
		b.WriteString(fmt.Sprintf(
			`<label class="me-edit-cond-radio"><input type="radio" name="me-edit-direction" value="%s"%s> %s</label>`,
			dom.Escape(d.SourceName), checked, dom.Escape(label),
		))
	}
	b.WriteString(`</div>`)
	if hasBID && hasASK {
		b.WriteString(`<p class="me-edit-help">BID = bank&#39;s purchase price; ASK = bank&#39;s sale price.</p>`)
	} else {
		b.WriteString(`<p class="me-edit-help">Pick which direction to subscribe to.</p>`)
	}
	return b.String()
}

// renderSourcePickers emits the provider trigger, pair trigger, optional shared
// backdrop, and either open overlay. Triggers are always visible; overlays are
// conditional on state.{Provider,Pair}PickerOpen.
//
// Layout invariant: the backdrop, when present, is the immediate previous
// sibling of the open overlay so a click on it does not pass through to the
// trigger. Only one picker is open at a time (enforced by the controller).
func renderSourcePickers(state application.MeSubscriptionsEditState) string {
	var b strings.Builder

	b.WriteString(`<label class="me-edit-label">Provider</label>`)
	b.WriteString(`<div class="me-edit-picker" id="me-edit-provider-picker">`)
	providerLabel := "— select provider —"
	if state.SelectedProviderTitle != "" {
		providerLabel = state.SelectedProviderTitle
	}
	b.WriteString(fmt.Sprintf(
		`<button class="me-edit-picker-trigger" id="me-edit-provider-trigger" type="button" aria-haspopup="listbox" aria-expanded="%t">`+
			`<span class="me-edit-picker-trigger-label">%s</span>`+
			`<span class="me-edit-picker-caret" aria-hidden="true">&#9662;</span>`+
			`</button>`,
		state.ProviderPickerOpen,
		dom.Escape(providerLabel),
	))
	if state.ProviderPickerOpen {
		b.WriteString(renderProviderPickerOverlay(state))
	}
	b.WriteString(`</div>`)

	pairDisabled := state.SelectedProviderTitle == ""
	pairLabel := "— select pair —"
	if !pairDisabled && state.Draft.SourceName != "" {
		for _, s := range state.Sources {
			if s.Name == state.Draft.SourceName {
				pairLabel = s.BaseCurrency + "/" + s.QuoteCurrency
				break
			}
		}
	}
	b.WriteString(`<label class="me-edit-label">Pair</label>`)
	b.WriteString(`<div class="me-edit-picker" id="me-edit-pair-picker">`)
	disabledAttr := ""
	if pairDisabled {
		disabledAttr = " disabled"
	}
	b.WriteString(fmt.Sprintf(
		`<button class="me-edit-picker-trigger" id="me-edit-pair-trigger" type="button" aria-haspopup="listbox" aria-expanded="%t"%s>`+
			`<span class="me-edit-picker-trigger-label">%s</span>`+
			`<span class="me-edit-picker-caret" aria-hidden="true">&#9662;</span>`+
			`</button>`,
		state.PairPickerOpen,
		disabledAttr,
		dom.Escape(pairLabel),
	))
	if state.PairPickerOpen && !pairDisabled {
		b.WriteString(renderPairPickerOverlay(state))
	}
	b.WriteString(`</div>`)

	return b.String()
}

// renderProviderPickerOverlay emits the open provider overlay with search input
// and a results slot. The slot has its own div (id me-edit-provider-results-slot)
// so keystrokes redraw the list and pagination without recreating the search
// input element, preserving caret position and focus.
func renderProviderPickerOverlay(state application.MeSubscriptionsEditState) string {
	var b strings.Builder
	b.WriteString(`<div class="me-edit-picker-backdrop" id="me-edit-picker-backdrop"></div>`)
	b.WriteString(`<div class="me-edit-picker-overlay" role="listbox">`)
	b.WriteString(fmt.Sprintf(
		`<input class="me-edit-picker-search" id="me-edit-provider-search" type="text" `+
			`placeholder="Search providers…" value="%s" autocomplete="off">`,
		dom.Escape(state.ProviderQuery),
	))
	b.WriteString(`<div class="me-edit-picker-results" id="me-edit-provider-results-slot">`)
	b.WriteString(RenderProviderResultsSlot(state))
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// RenderProviderResultsSlot renders the list + pagination block for the provider
// picker. Exported so the input handler can update only this slot, keeping the
// search-input element alive across keystrokes.
func RenderProviderResultsSlot(state application.MeSubscriptionsEditState) string {
	titles := distinctProviderTitles(state.Sources, state.ProviderQuery)
	page := state.ProviderPage
	if page < 1 {
		page = 1
	}
	pageItems, totalPages := paginateStrings(titles, page, application.PickerPageSize)
	if page > totalPages && totalPages > 0 {
		page = totalPages
		pageItems, totalPages = paginateStrings(titles, page, application.PickerPageSize)
	}
	return renderPickerListAndPagination(
		stringItemsToHTML(pageItems, "provider", state.SelectedProviderTitle, func(s string) string { return s }),
		len(titles), page, totalPages, "provider",
	)
}

// renderPairPickerOverlay mirrors the provider overlay shape for the pair
// picker. The list shows one row per source under the selected provider,
// labelled as Base/Quote (e.g. "USD/KZT") and carrying data-source-name for
// the click handler.
func renderPairPickerOverlay(state application.MeSubscriptionsEditState) string {
	var b strings.Builder
	b.WriteString(`<div class="me-edit-picker-backdrop" id="me-edit-picker-backdrop"></div>`)
	b.WriteString(`<div class="me-edit-picker-overlay" role="listbox">`)
	b.WriteString(fmt.Sprintf(
		`<input class="me-edit-picker-search" id="me-edit-pair-search" type="text" `+
			`placeholder="Search pairs…" value="%s" autocomplete="off">`,
		dom.Escape(state.PairQuery),
	))
	b.WriteString(`<div class="me-edit-picker-results" id="me-edit-pair-results-slot">`)
	b.WriteString(RenderPairResultsSlot(state))
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// RenderPairResultsSlot renders the list + pagination block for the pair
// picker. Exported for the input-driven slot redraw, same as
// RenderProviderResultsSlot.
func RenderPairResultsSlot(state application.MeSubscriptionsEditState) string {
	sources := pairsForProvider(state.Sources, state.SelectedProviderTitle, state.PairQuery)
	page := state.PairPage
	if page < 1 {
		page = 1
	}
	pageItems, totalPages := paginateSources(sources, page, application.PickerPageSize)
	if page > totalPages && totalPages > 0 {
		page = totalPages
		pageItems, totalPages = paginateSources(sources, page, application.PickerPageSize)
	}
	html := sourceItemsToHTML(pageItems, state.Draft.SourceName)
	return renderPickerListAndPagination(html, len(sources), page, totalPages, "pair")
}

// renderPickerListAndPagination wraps the pre-built item HTML in a scrollable
// list-area and emits the prev/info/next pagination bar as a sibling outside
// that scroll area. The slot's flex column grows the list-area to fill height
// while the pagination row stays pinned at the bottom, so the user can change
// pages without scrolling through a long list first.
//
// itemKind ("provider" or "pair") is forwarded to the pagination buttons via
// data-kind so the click handler routes to the right controller method.
func renderPickerListAndPagination(itemsHTML string, totalItems, page, totalPages int, itemKind string) string {
	var b strings.Builder
	b.WriteString(`<div class="me-edit-picker-list-area">`)
	if totalItems == 0 {
		b.WriteString(`<p class="me-edit-picker-empty">No matches.</p>`)
	} else {
		b.WriteString(`<ul class="me-edit-picker-list">`)
		b.WriteString(itemsHTML)
		b.WriteString(`</ul>`)
	}
	b.WriteString(`</div>`)

	if totalPages <= 1 {
		// Pagination row hidden for a single page so the overlay stays compact.
		return b.String()
	}

	prevDisabled := ""
	if page <= 1 {
		prevDisabled = " disabled"
	}
	nextDisabled := ""
	if page >= totalPages {
		nextDisabled = " disabled"
	}
	b.WriteString(`<div class="me-edit-picker-pagination">`)
	b.WriteString(fmt.Sprintf(
		`<button class="me-edit-picker-prev" type="button" data-kind="%s" data-page="%d"%s>Prev</button>`,
		itemKind, page-1, prevDisabled,
	))
	b.WriteString(fmt.Sprintf(
		`<span class="me-edit-picker-page-info">page %d of %d</span>`,
		page, totalPages,
	))
	b.WriteString(fmt.Sprintf(
		`<button class="me-edit-picker-next" type="button" data-kind="%s" data-page="%d"%s>Next</button>`,
		itemKind, page+1, nextDisabled,
	))
	b.WriteString(`</div>`)
	return b.String()
}

// stringItemsToHTML builds <li> rows for a string-keyed picker (the provider
// picker). The selected item gets an extra class so CSS can highlight it.
// labelOf maps the raw string to the visible label (identity for providers).
func stringItemsToHTML(items []string, dataAttr, selected string, labelOf func(string) string) string {
	var b strings.Builder
	for _, item := range items {
		cls := "me-edit-picker-item"
		if item == selected {
			cls += " me-edit-picker-item-active"
		}
		b.WriteString(fmt.Sprintf(
			`<li class="%s" data-%s="%s" role="option" tabindex="0">%s</li>`,
			cls, dataAttr, dom.Escape(item), dom.Escape(labelOf(item)),
		))
	}
	return b.String()
}

// sourceItemsToHTML builds <li> rows for the pair picker. Each row carries
// data-source-name so ChoosePair receives the unique source identifier rather
// than the human-readable pair label.
func sourceItemsToHTML(items []dto.SourceResponse, selectedSourceName string) string {
	var b strings.Builder
	for _, s := range items {
		cls := "me-edit-picker-item"
		if s.Name == selectedSourceName {
			cls += " me-edit-picker-item-active"
		}
		b.WriteString(fmt.Sprintf(
			`<li class="%s" data-source-name="%s" role="option" tabindex="0">%s/%s</li>`,
			cls, dom.Escape(s.Name), dom.Escape(s.BaseCurrency), dom.Escape(s.QuoteCurrency),
		))
	}
	return b.String()
}

// distinctProviderTitles returns sorted unique SourceTitle values across
// sources, optionally filtered by a case-insensitive substring query.
func distinctProviderTitles(sources []dto.SourceResponse, query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	seen := make(map[string]struct{}, len(sources))
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		if q != "" && !strings.Contains(strings.ToLower(s.Title), q) {
			continue
		}
		if _, ok := seen[s.Title]; ok {
			continue
		}
		seen[s.Title] = struct{}{}
		out = append(out, s.Title)
	}
	sort.Strings(out)
	return out
}

// pairsForProvider returns the sources whose Title equals title, deduplicated
// by (BaseCurrency, QuoteCurrency), optionally filtered by a case-insensitive
// substring match on either currency. Results are sorted by Base then Quote
// so a paginated page is deterministic.
//
// Dedup rationale: each currency pair is persisted as TWO source rows — BID
// (bank purchase) and ASK (bank sale) — with the same Title/Base/Quote but
// different Name (e.g. KZ_BCC_FX_BID_USD_KZT, KZ_BCC_FX_ASK_USD_KZT). Showing
// both lists "USD/KZT" twice with no visible difference. We collapse the
// duplicate and keep whichever source sorts first by Name (ASK in the current
// data, since "ASK" < "BID"). Subscribing to both directions today means two
// subscriptions; a direction toggle can come later if a user asks.
func pairsForProvider(sources []dto.SourceResponse, title, query string) []dto.SourceResponse {
	if title == "" {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	type pairKey struct{ base, quote string }
	bestByPair := make(map[pairKey]dto.SourceResponse, len(sources))
	for _, s := range sources {
		if s.Title != title {
			continue
		}
		if q != "" {
			label := strings.ToLower(s.BaseCurrency + "/" + s.QuoteCurrency)
			if !strings.Contains(label, q) {
				continue
			}
		}
		key := pairKey{s.BaseCurrency, s.QuoteCurrency}
		if existing, ok := bestByPair[key]; ok && existing.Name < s.Name {
			continue
		}
		bestByPair[key] = s
	}
	out := make([]dto.SourceResponse, 0, len(bestByPair))
	for _, v := range bestByPair {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BaseCurrency != out[j].BaseCurrency {
			return out[i].BaseCurrency < out[j].BaseCurrency
		}
		return out[i].QuoteCurrency < out[j].QuoteCurrency
	})
	return out
}

// paginateStrings returns the page slice and total page count for a string
// list. Total pages is at least 1 even when items is empty so the UI can
// render "page 1 of 1" rather than "page 1 of 0".
func paginateStrings(items []string, page, size int) ([]string, int) {
	totalPages := (len(items) + size - 1) / size
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * size
	if start >= len(items) {
		return nil, totalPages
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], totalPages
}

// paginateSources is the dto.SourceResponse counterpart of paginateStrings.
// The two are not collapsed into one generic because the WASM build target
// keeps the function-instantiation surface small.
func paginateSources(items []dto.SourceResponse, page, size int) ([]dto.SourceResponse, int) {
	totalPages := (len(items) + size - 1) / size
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * size
	if start >= len(items) {
		return nil, totalPages
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], totalPages
}

// renderEditListItem renders a single subscription row in the editor list as
// two stacked lines: the provider title on top, the pair and condition below.
// The pair label disambiguates rows sharing one provider title (all Yahoo
// sources are titled "Yahoo Finance"; only the pair tells them apart). The
// pair span is omitted when the currencies are unknown (source row missing),
// so the list never shows a bare "/" — the CSS ::after separator disappears
// with it.
func renderEditListItem(item dto.MeSubscriptionEditRow) string {
	pair := ""
	if item.BaseCurrency != "" && item.QuoteCurrency != "" {
		pair = fmt.Sprintf(
			`<span class="me-edit-item-pair">%s</span>`,
			dom.Escape(item.BaseCurrency+"/"+item.QuoteCurrency),
		)
	}
	return fmt.Sprintf(
		`<li class="me-edit-item">`+
			`<div class="me-edit-item-main">`+
			`<span class="me-edit-item-source">%s</span>`+
			`<div class="me-edit-item-detail">%s<span class="me-edit-item-cond">%s: %s</span></div>`+
			`</div>`+
			`<button class="me-edit-delete" type="button" data-id="%s" aria-label="Delete subscription">✕</button>`+
			`</li>`,
		dom.Escape(item.SourceTitle),
		pair,
		dom.Escape(item.ConditionType),
		dom.Escape(item.ConditionValue),
		dom.Escape(item.ID),
	)
}

// conditionValuePlaceholder returns a placeholder hint for the value input
// based on the selected condition type.
func conditionValuePlaceholder(conditionType string) string {
	switch conditionType {
	case "daily":
		return "09:00:00"
	case "delta":
		return "1.5"
	case "interval":
		return "1h30m"
	case "cron":
		return "0 9 * * 1-5"
	default:
		return "value"
	}
}

// conditionValueHelp returns a descriptive hint for the value input.
func conditionValueHelp(conditionType string) string {
	switch conditionType {
	case "daily":
		return "Time of day to receive a daily notification (HH:MM:SS in UTC)."
	case "delta":
		return "Non-negative rate change threshold that triggers a notification."
	case "interval":
		return "Go duration ≥ 1 minute between notifications (e.g. 1h, 30m)."
	case "cron":
		return "Standard 5-field cron expression (minute hour dom month dow)."
	default:
		return "Select a condition type above."
	}
}
