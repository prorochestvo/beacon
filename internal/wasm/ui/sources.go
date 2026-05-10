package ui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/wasm/dom"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

// RenderSources returns the full HTML for the Sources List screen given the
// current state. Every user-influenced field is passed through dom.Escape
// before interpolation.
func RenderSources(state application.SourcesState) string {
	var b strings.Builder

	b.WriteString(`<h1>FX Rate Monitor</h1>`)
	b.WriteString(renderStatsLine(state.Stats))
	b.WriteString(renderFilters())
	b.WriteString(`<div id="sources-table">`)
	b.WriteString(renderSourcesTable(state))
	b.WriteString(`</div>`)

	return b.String()
}

// RenderSourcesTable returns only the inner table HTML so the DOM can be
// updated in-place without re-rendering the filters.
func RenderSourcesTable(state application.SourcesState) string {
	return renderSourcesTable(state)
}

func renderStatsLine(stats api.StatsResponse) string {
	errLink := ""
	if stats.ErrorsTotal > 0 {
		errLink = fmt.Sprintf(` · <a href="#/errors">%d error(s)</a>`, stats.ErrorsTotal)
	}
	return fmt.Sprintf(
		`<p class="stats-line">%d/%d source(s) active%s</p>`,
		stats.SourcesActive, stats.SourcesTotal, errLink,
	)
}

func renderFilters() string {
	return `<div class="filters">` +
		`<input id="f-title"  type="text"   placeholder="Filter by title…">` +
		`<input id="f-pair"   type="text"   placeholder="Filter by pair…">` +
		`<select id="f-status">` +
		`<option value="all">All statuses</option>` +
		`<option value="ok">OK</option>` +
		`<option value="error">Error</option>` +
		`</select>` +
		`<select id="f-active">` +
		`<option value="all">All</option>` +
		`<option value="yes">Active only</option>` +
		`<option value="no">Inactive only</option>` +
		`</select>` +
		`</div>`
}

func renderSourcesTable(state application.SourcesState) string {
	visible := state.Visible()

	arrow := " ↓"
	if !state.SortDesc {
		arrow = " ↑"
	}

	var rows string
	if len(visible) == 0 {
		rows = `<tr><td colspan="6">No sources match the current filters.</td></tr>`
	} else {
		var rb strings.Builder
		for _, src := range visible {
			rb.WriteString(renderSourceRow(src))
		}
		rows = rb.String()
	}

	return fmt.Sprintf(`<table>
      <thead><tr>
        <th>Title</th>
        <th>Pair</th>
        <th>Interval</th>
        <th id="sort-lastrun">Last Run%s</th>
        <th>Status</th>
        <th>Active</th>
      </tr></thead>
      <tbody>%s</tbody>
    </table>`, arrow, rows)
}

func renderSourceRow(src api.SourceResponse) string {
	statusCell := renderStatusCell(src)
	activeCell := renderActiveCell(src)

	return fmt.Sprintf(`<tr>
            <td><a href="#/sources/%s">%s</a></td>
            <td>%s/%s</td>
            <td>%s</td>
            <td>%s</td>
            <td>%s</td>
            <td>%s</td>
          </tr>`,
		url.PathEscape(src.Name),
		dom.Escape(titleOrName(src)),
		dom.Escape(src.BaseCurrency),
		dom.Escape(src.QuoteCurrency),
		dom.Escape(src.Interval),
		fmtDate(src.LastRunAt),
		statusCell,
		activeCell,
	)
}

func renderStatusCell(src api.SourceResponse) string {
	if src.LastSuccess {
		return `<span class="ok">✓ OK</span>`
	}
	errText := src.LastError
	if errText == "" {
		errText = "unknown"
	}
	return fmt.Sprintf(`<span class="err">✗ %s</span>`, dom.Escape(errText))
}

func renderActiveCell(src api.SourceResponse) string {
	if src.Active {
		return fmt.Sprintf(
			`<button class="toggle-btn active-on"  data-name="%s" data-active="false">Yes</button>`,
			dom.Escape(src.Name),
		)
	}
	return fmt.Sprintf(
		`<button class="toggle-btn active-off" data-name="%s" data-active="true">No</button>`,
		dom.Escape(src.Name),
	)
}

func titleOrName(src api.SourceResponse) string {
	if src.Title != "" {
		return src.Title
	}
	return src.Name
}

func fmtDate(iso string) string {
	if iso == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		t, err = time.Parse(time.RFC3339, iso)
		if err != nil {
			return dom.Escape(iso)
		}
	}
	return t.Local().Format("1/2/2006, 3:04:05 PM")
}
