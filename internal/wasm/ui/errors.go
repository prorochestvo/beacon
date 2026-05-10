package ui

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/wasm/dom"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

// RenderErrors returns the full HTML skeleton for the Errors screen. The
// exec-errors and event-errors sections contain "Loading…" placeholders that
// the caller replaces after the async fetches complete.
func RenderErrors(state application.ErrorsState) string {
	var b strings.Builder

	b.WriteString(`<div class="breadcrumb"><a href="#/">← All Sources</a></div>`)
	b.WriteString("<h1>Errors</h1>")

	b.WriteString("<h2>Execution Errors</h2>")
	b.WriteString(`<div id="exec-errors-section">`)
	b.WriteString(RenderExecErrorsSection(state))
	b.WriteString("</div>")

	b.WriteString("<h2>Event Errors</h2>")
	b.WriteString(`<div id="event-errors-section">`)
	b.WriteString(RenderEventErrorsSection(state))
	b.WriteString("</div>")

	return b.String()
}

// RenderExecErrorsSection returns the execution errors table and pagination HTML.
// Every user-influenced field is escaped; the error field can contain upstream
// HTML error pages and is an XSS vector without escaping.
func RenderExecErrorsSection(state application.ErrorsState) string {
	errs := state.ExecErrors
	var rows string
	if len(errs) == 0 {
		rows = `<tr><td colspan="4">No execution errors.</td></tr>`
	} else {
		var rb strings.Builder
		for _, e := range errs {
			rb.WriteString(renderExecErrorRow(e))
		}
		rows = rb.String()
	}

	table := fmt.Sprintf(`<table>
      <thead><tr><th>ID</th><th>Source</th><th>Error</th><th>Timestamp</th></tr></thead>
      <tbody>%s</tbody>
    </table>`, rows)

	pagination := RenderPagination(PaginationState{
		Page:    state.ExecPage,
		Count:   len(errs),
		Limit:   application.ExecLimit,
		Section: "exec",
	})

	return table + pagination
}

func renderExecErrorRow(e api.ExecutionErrorResponse) string {
	return fmt.Sprintf(`<tr>
          <td>%s</td>
          <td>%s</td>
          <td class="err">%s</td>
          <td>%s</td>
        </tr>`,
		dom.Escape(e.ID),
		dom.Escape(e.SourceName),
		dom.Escape(e.Error),
		fmtDate(e.Timestamp),
	)
}

// RenderEventErrorsSection returns the event (notification) errors table and
// pagination HTML. Every user-influenced field is escaped; last_error can
// contain upstream HTML error pages and is an XSS vector without escaping.
func RenderEventErrorsSection(state application.ErrorsState) string {
	errs := state.EventErrors
	var rows string
	if len(errs) == 0 {
		rows = `<tr><td colspan="4">No event errors.</td></tr>`
	} else {
		var rb strings.Builder
		for _, e := range errs {
			rb.WriteString(renderEventErrorRow(e))
		}
		rows = rb.String()
	}

	table := fmt.Sprintf(`<table>
      <thead><tr><th>ID</th><th>User Type</th><th>Error</th><th>Timestamp</th></tr></thead>
      <tbody>%s</tbody>
    </table>`, rows)

	pagination := RenderPagination(PaginationState{
		Page:    state.EventPage,
		Count:   len(errs),
		Limit:   application.EventLimit,
		Section: "event",
	})

	return table + pagination
}

func renderEventErrorRow(e api.NotificationResponse) string {
	ts := "—"
	if !e.SentAt.IsZero() {
		ts = e.SentAt.Local().Format("1/2/2006, 3:04:05 PM")
	}
	return fmt.Sprintf(`<tr>
          <td>%s</td>
          <td>%s</td>
          <td class="err">%s</td>
          <td>%s</td>
        </tr>`,
		dom.Escape(e.ID),
		dom.Escape(e.UserType),
		dom.Escape(e.LastError),
		ts,
	)
}
