package ui

import "fmt"

// PaginationState holds the data needed to render a pagination control.
//
// Page is the current 1-based page number. Count is the number of items
// rendered on the current page. Limit is the page size. Section is a stable
// identifier embedded as data-section on each button so a delegated click
// handler can route the event to the right data-loading function.
type PaginationState struct {
	Page    int
	Count   int
	Limit   int
	Section string
}

// RenderPagination returns the HTML for a pagination control, or an empty
// string when neither a previous nor a next button is appropriate.
//
// Prev is shown (disabled) when Page == 1 and hasNext is true, or enabled when
// Page > 1. Next is omitted entirely when Count < Limit, matching the JS
// semantics: hasNext = count >= limit.
func RenderPagination(state PaginationState) string {
	hasPrev := state.Page > 1
	hasNext := state.Count >= state.Limit
	if !hasPrev && !hasNext {
		return ""
	}

	sec := state.Section

	var prev string
	if hasPrev {
		prev = fmt.Sprintf(
			`<button data-section="%s" data-page="%d">‹ Prev</button>`,
			sec, state.Page-1,
		)
	} else {
		prev = `<button disabled>‹ Prev</button>`
	}

	var next string
	if hasNext {
		next = fmt.Sprintf(
			`<button data-section="%s" data-page="%d">Next ›</button>`,
			sec, state.Page+1,
		)
	}

	return fmt.Sprintf(`<div class="pagination">%s%s</div>`, prev, next)
}
