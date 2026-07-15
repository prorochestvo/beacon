// Package ui provides HTML renderers for the WASM frontend. This file renders
// the city weather subscription screen: a text input for geocoding search, a
// list of matches to pick from, and the caller's saved city list with per-row
// delete controls. All user-supplied and server-returned text is HTML-escaped.
package ui

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/dom"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// RenderMeWeatherCities returns the full HTML for the city weather subscription
// screen. Auth-failure and load-error states short-circuit the content.
//
// Every user-influenced string — city names, country names, timezone labels —
// is escaped through dom.Escape before interpolation to prevent XSS.
func RenderMeWeatherCities(state application.WeatherCitiesState) string {
	if state.AuthFailure {
		return fmt.Sprintf(`<p class="error-msg">%s</p>`, authFailureMsg)
	}

	var b strings.Builder

	b.WriteString(renderWeatherTopbar())

	if state.Loading {
		b.WriteString(`<p class="weather-loading">Loading…</p>`)
		return b.String()
	}
	if state.LoadError != nil {
		b.WriteString(`<p class="error-msg">`)
		b.WriteString(dom.Escape(state.LoadError.Error()))
		b.WriteString(`</p>`)
		return b.String()
	}

	b.WriteString(renderWeatherSearchSection(state))
	b.WriteString(renderWeatherCityList(state))
	return b.String()
}

// renderWeatherTopbar emits the screen header with a back button.
func renderWeatherTopbar() string {
	return `<div class="weather-topbar">` +
		`<button class="weather-back" id="weather-back" type="button">← Back</button>` +
		`<span class="weather-title">My cities</span>` +
		`</div>`
}

// renderWeatherSearchSection emits the geocoding input, result list, and
// save/clear affordances. The search input carries id="weather-search" so the
// WASM event dispatcher can attach a debounced oninput handler.
func renderWeatherSearchSection(state application.WeatherCitiesState) string {
	var b strings.Builder
	b.WriteString(`<section class="weather-search-section">`)
	b.WriteString(`<h2 class="weather-section-title">Add a city</h2>`)

	b.WriteString(fmt.Sprintf(
		`<input class="weather-search-input" id="weather-search" type="text" `+
			`placeholder="Search city…" value="%s" autocomplete="off">`,
		dom.Escape(state.SearchQuery),
	))

	if state.SearchLoading {
		b.WriteString(`<p class="weather-search-loading">Searching…</p>`)
	} else if state.SearchError != nil {
		b.WriteString(`<p class="weather-search-error">`)
		b.WriteString(dom.Escape(state.SearchError.Error()))
		b.WriteString(`</p>`)
	} else if len(state.SearchResults) > 0 {
		b.WriteString(renderWeatherSearchResults(state))
	} else if strings.TrimSpace(state.SearchQuery) != "" {
		b.WriteString(`<p class="weather-search-empty">No cities found.</p>`)
	}

	if state.SaveError != nil {
		b.WriteString(`<p class="weather-save-error">`)
		b.WriteString(dom.Escape(state.SaveError.Error()))
		b.WriteString(`</p>`)
	}

	b.WriteString(`</section>`)
	return b.String()
}

// renderWeatherSearchResults emits the list of geocoding matches. Each item
// carries data-index so the click handler can call SelectSearchResult(i); the
// index always refers to the item's position in state.SearchResults, so rows
// skipped by the label dedup below never shift the mapping. The selected item
// gets an extra class for CSS highlight. A Save and a Clear button appear
// below the list when a selection is active.
func renderWeatherSearchResults(state application.WeatherCitiesState) string {
	var b strings.Builder
	b.WriteString(`<ul class="weather-search-results" id="weather-search-results">`)
	// Open-Meteo geocoding can return distinct location_ids whose rendered
	// labels are byte-identical (e.g. two villages of the same name in one
	// province). Identical rows give the user nothing to choose between, so
	// only the first occurrence of each label is shown.
	seen := make(map[string]struct{}, len(state.SearchResults))
	for i, item := range state.SearchResults {
		cls := "weather-search-item"
		if state.Selected != nil && state.Selected.LocationID == item.LocationID {
			cls += " weather-search-item-selected"
		}
		label := item.DisplayName
		if item.Admin1 != "" {
			label += ", " + item.Admin1
		}
		if item.Country != "" {
			label += ", " + item.Country
		}
		if _, dup := seen[label]; dup {
			continue
		}
		seen[label] = struct{}{}
		b.WriteString(fmt.Sprintf(
			`<li class="%s" data-index="%d" role="option" tabindex="0">%s</li>`,
			cls, i, dom.Escape(label),
		))
	}
	b.WriteString(`</ul>`)

	if state.Selected != nil {
		b.WriteString(`<div class="weather-search-actions">`)
		b.WriteString(`<button class="weather-save-btn" id="weather-save-btn" type="button">Add city</button>`)
		b.WriteString(`<button class="weather-clear-btn" id="weather-clear-btn" type="button">Clear</button>`)
		b.WriteString(`</div>`)
	}
	return b.String()
}

// WeatherCityGroup holds all subscription rows for a single physical city,
// grouped by location_id for display.
type WeatherCityGroup struct {
	LocationID  string
	DisplayName string
	Country     string
	Admin1      string
	Timezone    string
	Rows        []dto.WeatherCityRow
}

// GroupWeatherCities groups a flat city list by location_id, preserving the
// server's row order within each group. The result slice follows first-seen
// order of location_id values.
func GroupWeatherCities(cities []dto.WeatherCityRow) []WeatherCityGroup {
	seen := make(map[string]int)
	var groups []WeatherCityGroup
	for _, c := range cities {
		if i, ok := seen[c.LocationID]; ok {
			groups[i].Rows = append(groups[i].Rows, c)
		} else {
			seen[c.LocationID] = len(groups)
			groups = append(groups, WeatherCityGroup{
				LocationID:  c.LocationID,
				DisplayName: c.DisplayName,
				Country:     c.Country,
				Admin1:      c.Admin1,
				Timezone:    c.Timezone,
				Rows:        []dto.WeatherCityRow{c},
			})
		}
	}
	return groups
}

// renderWeatherCityList emits the caller's saved city subscription list grouped by
// location_id, with per-kind delete controls and an "Add alert" form per city.
// A "View current weather" button appears when at least one city is saved.
func renderWeatherCityList(state application.WeatherCitiesState) string {
	var b strings.Builder
	b.WriteString(`<section class="weather-cities-section">`)
	b.WriteString(`<h2 class="weather-section-title">Your cities</h2>`)

	groups := GroupWeatherCities(state.Cities)

	if len(groups) == 0 {
		b.WriteString(`<p class="weather-cities-empty">No cities yet. Use the search above to add one.</p>`)
	} else {
		b.WriteString(`<ul class="weather-cities-list" id="weather-cities-list">`)
		for _, g := range groups {
			b.WriteString(renderWeatherCityGroupItem(g, state))
		}
		b.WriteString(`</ul>`)
		b.WriteString(`<button class="weather-current-btn" id="weather-view-current" type="button">View current weather</button>`)
	}

	b.WriteString(`</section>`)
	return b.String()
}

// renderWeatherCityGroupItem emits one grouped city entry: a city header row
// followed by per-kind subscription rows and an alert form when open.
func renderWeatherCityGroupItem(g WeatherCityGroup, state application.WeatherCitiesState) string {
	label := g.DisplayName
	if g.Admin1 != "" {
		label += ", " + g.Admin1
	}
	if g.Country != "" {
		label += ", " + g.Country
	}

	var b strings.Builder
	b.WriteString(`<li class="weather-city-group">`)
	fmt.Fprintf(&b, `<span class="weather-city-name">%s</span>`, dom.Escape(label))
	b.WriteString(`<ul class="weather-city-kinds">`)

	for _, row := range g.Rows {
		b.WriteString(renderWeatherKindRow(row))
	}

	b.WriteString(`</ul>`)

	// Alert form: show either an "Add alert" button or the open form for this city.
	// AlertFormLocationID must be non-empty to avoid matching cities with no LocationID.
	if state.AlertFormLocationID != "" && state.AlertFormLocationID == g.LocationID {
		b.WriteString(renderWeatherAlertForm(state))
	} else {
		fmt.Fprintf(&b,
			`<button class="weather-add-alert-btn" type="button" data-location-id="%s">+ Add alert</button>`,
			dom.Escape(g.LocationID),
		)
	}

	b.WriteString(`</li>`)
	return b.String()
}

// renderWeatherKindRow emits one subscription kind row with a delete button.
func renderWeatherKindRow(row dto.WeatherCityRow) string {
	label := alertKindLabel(row.NotifyKind, row.ConditionValue, row.NotifyHour)
	return fmt.Sprintf(
		`<li class="weather-kind-row">`+
			`<span class="weather-kind-label">%s</span>`+
			`<button class="weather-city-delete" type="button" data-id="%s" aria-label="Remove">✕</button>`+
			`</li>`,
		dom.Escape(label),
		dom.Escape(row.ID),
	)
}

// alertKindLabel returns a human-readable label for a subscription row.
func alertKindLabel(kind, conditionValue string, notifyHour int) string {
	switch kind {
	case "alert_heat":
		return fmt.Sprintf("Heat alert ≥ %s°C", conditionValue)
	case "alert_frost":
		return fmt.Sprintf("Frost alert ≤ %s°C", conditionValue)
	case "alert_thunderstorm":
		return "Thunderstorm alert"
	case "alert_thaw":
		return "Thaw alert"
	case "rain_alert":
		return fmt.Sprintf("Rain alert ≥ %s%% within 6h", conditionValue)
	default: // morning_summary or empty
		return fmt.Sprintf("Morning summary · %02d:00", notifyHour)
	}
}

// renderWeatherAlertForm emits the open alert-creation form for the current city.
func renderWeatherAlertForm(state application.WeatherCitiesState) string {
	var b strings.Builder
	b.WriteString(`<div class="weather-alert-form" id="weather-alert-form">`)

	// Kind selector.
	b.WriteString(`<select class="weather-alert-kind" id="weather-alert-kind">`)
	kinds := []struct{ value, label string }{
		{"alert_heat", "Heat alert (°C)"},
		{"alert_frost", "Frost alert (°C)"},
		{"alert_thunderstorm", "Thunderstorm alert"},
		{"rain_alert", "Rain alert (%)"},
		{"alert_thaw", "Thaw alert"},
	}
	for _, k := range kinds {
		selected := ""
		if state.AlertFormKind == k.value {
			selected = ` selected`
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, dom.Escape(k.value), selected, dom.Escape(k.label))
	}
	b.WriteString(`</select>`)

	// Value input (hidden for thunderstorm and thaw — neither has a numeric threshold).
	if state.AlertFormKind != "alert_thunderstorm" && state.AlertFormKind != "alert_thaw" {
		fmt.Fprintf(&b,
			`<input class="weather-alert-value" id="weather-alert-value" type="number" step="0.1" `+
				`placeholder="threshold" value="%s">`,
			dom.Escape(state.AlertFormValue),
		)
	}

	// Error message.
	if state.AlertSaveError != nil {
		fmt.Fprintf(&b, `<p class="weather-alert-error">%s</p>`, dom.Escape(state.AlertSaveError.Error()))
	}

	b.WriteString(`<div class="weather-alert-actions">`)
	b.WriteString(`<button class="weather-alert-save" id="weather-alert-save" type="button">Save</button>`)
	b.WriteString(`<button class="weather-alert-cancel" id="weather-alert-cancel" type="button">Cancel</button>`)
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// renderWeatherCityRow emits one saved-city row with a delete button.
// Retained for backward compatibility; new code calls renderWeatherKindRow.
// All displayed strings are escaped; id is stored in data-id on the delete button.
func renderWeatherCityRow(id, displayName, country, admin1, timezone string, notifyHour int) string {
	label := displayName
	if admin1 != "" {
		label += ", " + admin1
	}
	if country != "" {
		label += ", " + country
	}
	return fmt.Sprintf(
		`<li class="weather-city-row">`+
			`<span class="weather-city-name">%s</span>`+
			`<span class="weather-city-detail">%s · %02d:00</span>`+
			`<button class="weather-city-delete" type="button" data-id="%s" aria-label="Remove city">✕</button>`+
			`</li>`,
		dom.Escape(label),
		dom.Escape(timezone),
		notifyHour,
		dom.Escape(id),
	)
}
