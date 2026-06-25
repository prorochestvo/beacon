package ui

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/cmd/wasm/dom"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// RenderSourceDetail returns the full HTML skeleton for the Source Detail
// screen. The subscriptions and daily-events sections hold a "Loading…"
// placeholder the caller replaces after the async fetches complete.
func RenderSourceDetail(state application.SourceDetailState) string {
	var b strings.Builder

	b.WriteString(`<div class="breadcrumb"><a href="#/">← All Sources</a></div>`)
	b.WriteString(fmt.Sprintf("<h1>%s</h1>", dom.Escape(state.Title)))

	b.WriteString("<h2>Rate History</h2>")
	b.WriteString(`<div class="filters">`)
	b.WriteString(`<input id="rate-filter" type="text" placeholder="Filter by pair…">`)
	b.WriteString("</div>")
	b.WriteString(`<div id="rates-table">`)
	b.WriteString(RenderRatesTable(state))
	b.WriteString("</div>")

	b.WriteString("<h2>Subscriptions</h2>")
	b.WriteString(`<div id="subs-section"><p>Loading…</p></div>`)

	b.WriteString("<h2>Events by Day</h2>")
	b.WriteString(`<div id="daily-events-section"><p>Loading…</p></div>`)

	return b.String()
}

// RenderRatesTable returns the rate history table HTML with the current
// filter+sort applied. Every user-influenced field is escaped.
func RenderRatesTable(state application.SourceDetailState) string {
	arrow := " ↓"
	if !state.RateSortDesc {
		arrow = " ↑"
	}

	visible := state.VisibleRates()
	var rows string
	if len(visible) == 0 {
		rows = `<tr><td colspan="3">No rates.</td></tr>`
	} else {
		var rb strings.Builder
		for _, r := range visible {
			rb.WriteString(renderRateRow(r))
		}
		rows = rb.String()
	}

	return fmt.Sprintf(`<table>
      <thead><tr>
        <th>Pair</th>
        <th>Price</th>
        <th id="rate-sort-header">Timestamp%s</th>
      </tr></thead>
      <tbody>%s</tbody>
    </table>`, arrow, rows)
}

func renderRateRow(r dto.RateResponse) string {
	return fmt.Sprintf(`<tr>
          <td>%s/%s</td>
          <td>%s</td>
          <td>%s</td>
        </tr>`,
		dom.Escape(r.BaseCurrency),
		dom.Escape(r.QuoteCurrency),
		dom.Escape(fmt.Sprintf("%v", r.Price)),
		fmtDate(r.Timestamp),
	)
}

// RenderSubsSection returns the subscriptions table and pagination HTML.
// Every user-influenced field is escaped; condition values can contain < and >
// from user-set thresholds (e.g. "price > 100") and must always be escaped.
func RenderSubsSection(state application.SourceDetailState) string {
	subs := state.Subs
	var rows string
	if len(subs) == 0 {
		rows = `<tr><td colspan="5">No subscriptions.</td></tr>`
	} else {
		var rb strings.Builder
		for _, s := range subs {
			rb.WriteString(renderSubRow(s))
		}
		rows = rb.String()
	}

	table := fmt.Sprintf(`<table>
      <thead><tr>
        <th>ID</th><th>User Type</th><th>Source</th><th>Condition</th><th>Latest Notified At</th>
      </tr></thead>
      <tbody>%s</tbody>
    </table>`, rows)

	pagination := RenderPagination(PaginationState{
		Page:    state.SubsPage,
		Count:   len(subs),
		Limit:   application.SubsLimit,
		Section: "subs",
	})

	return table + pagination
}

func renderSubRow(s dto.SubscriptionDetailResponse) string {
	notifiedAt := "—"
	if s.LatestNotifiedAt != "" {
		notifiedAt = fmtDate(s.LatestNotifiedAt)
	}
	return fmt.Sprintf(`<tr>
          <td>%s</td>
          <td>%s</td>
          <td>%s</td>
          <td>%s</td>
          <td>%s</td>
        </tr>`,
		dom.Escape(s.ID),
		dom.Escape(s.UserType),
		dom.Escape(s.SourceName),
		dom.Escape(s.Condition),
		notifiedAt,
	)
}

// RenderDailyEventsSection returns the daily events table and pagination HTML.
// Every user-influenced field is escaped.
func RenderDailyEventsSection(state application.SourceDetailState) string {
	events := state.DailyEvents
	var rows string
	if len(events) == 0 {
		rows = `<tr><td colspan="3">No daily event data.</td></tr>`
	} else {
		var rb strings.Builder
		for _, e := range events {
			rb.WriteString(renderDailyEventRow(e))
		}
		rows = rb.String()
	}

	table := fmt.Sprintf(`<table>
      <thead><tr><th>Type</th><th>Date</th><th>Count (S/F)</th></tr></thead>
      <tbody>%s</tbody>
    </table>`, rows)

	pagination := RenderPagination(PaginationState{
		Page:    state.DailyEventsPage,
		Count:   len(events),
		Limit:   application.DailyEventsLimit,
		Section: "daily-events",
	})

	return table + pagination
}

func renderDailyEventRow(e dto.DailyEventResponse) string {
	return fmt.Sprintf(`<tr>
          <td>%s</td>
          <td>%s</td>
          <td>%s/%s</td>
        </tr>`,
		dom.Escape(e.Type),
		dom.Escape(e.Date),
		dom.Escape(fmt.Sprintf("%d", e.SuccessCount)),
		dom.Escape(fmt.Sprintf("%d", e.FailedCount)),
	)
}
