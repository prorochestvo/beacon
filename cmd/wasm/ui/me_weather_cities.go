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
// carries data-index so the click handler can call SelectSearchResult(i). The
// selected item gets an extra class for CSS highlight. A Save and a Clear button
// appear below the list when a selection is active.
func renderWeatherSearchResults(state application.WeatherCitiesState) string {
	var b strings.Builder
	b.WriteString(`<ul class="weather-search-results" id="weather-search-results">`)
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

// renderWeatherCityList emits the caller's saved city subscription list. Each
// row carries a delete button with data-id so the WASM dispatcher can call
// DeleteCity(id).
func renderWeatherCityList(state application.WeatherCitiesState) string {
	var b strings.Builder
	b.WriteString(`<section class="weather-cities-section">`)
	b.WriteString(`<h2 class="weather-section-title">Your cities</h2>`)

	if len(state.Cities) == 0 {
		b.WriteString(`<p class="weather-cities-empty">No cities yet. Use the search above to add one.</p>`)
	} else {
		b.WriteString(`<ul class="weather-cities-list" id="weather-cities-list">`)
		for _, c := range state.Cities {
			b.WriteString(renderWeatherCityRow(c.ID, c.DisplayName, c.Country, c.Admin1, c.Timezone, c.NotifyHour))
		}
		b.WriteString(`</ul>`)
	}

	b.WriteString(`</section>`)
	return b.String()
}

// renderWeatherCityRow emits one saved-city row with a delete button.
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
