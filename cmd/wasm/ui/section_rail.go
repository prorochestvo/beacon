package ui

// This file renders the vertical section rail — the Mini App's top-level
// navigation.
//
// The Mini App is a 2×2 matrix of a section (what you are looking at) and a mode
// (viewing it or managing it):
//
//	           view (home)                 manage (settings)
//	Rates      RenderMeSubscriptions       RenderMeSubscriptionsEdit
//	Weather    RenderMeWeatherCurrent      RenderMeWeatherCities
//
// The rail switches the section and never the mode; the gear (home) and the ← Back
// button (settings) switch the mode and never the section. Each cell is a separate
// screen mount, so the active tab is implied by which screen rendered the rail —
// there is no tab state to keep anywhere.

import (
	"fmt"
	"strings"
)

// Section identifies a top-level Mini App section. The string values are the
// data-section attribute the delegated rail click handler in cmd/wasm/main.go
// routes on; changing one means changing that handler's route map.
type Section string

const (
	// SectionRates is the FX/equity rates section: the sparkline list and its
	// subscription editor.
	SectionRates Section = "rates"
	// SectionWeather is the weather section: current conditions and the city and
	// alert manager.
	SectionWeather Section = "weather"
)

// sectionTabs is the rail's tab order. Extending the Mini App with a third section
// means adding one entry here and one route in cmd/wasm/main.go's rail handlers.
var sectionTabs = []struct {
	section Section
	label   string
	glyph   string
}{
	{SectionRates, "Rates", sectionRatesSVG},
	{SectionWeather, "Weather", sectionWeatherSVG},
}

// sectionRatesSVG is the inline glyph for the rates tab: a rising line chart.
// Viewbox 24×24 px; rendered at 20×20 px via CSS.
const sectionRatesSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" focusable="false">` +
	`<path d="M3.5 17.5 9 12l4 4 6.5-7.5V13h2V5h-8v2h4.1L13 13l-4-4-7 7z"/>` +
	`</svg>`

// sectionWeatherSVG is the inline glyph for the weather tab. It is the same cloud
// path that used to sit in the chart-card header as a navigation button; here it is
// a section marker, not an entry point.
const sectionWeatherSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" focusable="false">` +
	`<path d="M19.35 10.04C18.67 6.59 15.64 4 12 4 9.11 4 6.6 5.64 5.35 8.04 2.34 8.36 0 10.91 0 14c0 3.31 2.69 6 6 6h13c2.76 0 5-2.24 5-5 0-2.64-2.05-4.78-4.65-4.96z"/>` +
	`</svg>`

// RenderSectionShell wraps a screen body in the two-column layout: the vertical
// section rail on the left, the screen's own content on the right. active marks the
// tab of the screen being rendered.
//
// body is emitted verbatim — every caller builds it from already-escaped fragments.
func RenderSectionShell(active Section, body string) string {
	var b strings.Builder
	b.WriteString(`<div class="app-shell">`)
	b.WriteString(RenderSectionRail(active))
	b.WriteString(`<div class="section-panel">`)
	b.WriteString(body)
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// RenderSectionRail emits the vertical tab rail with active marked. Each tab carries
// a data-section attribute for the delegated click handler and aria-selected for
// assistive technology; the active state is styled off aria-selected rather than a
// separate class, so the two can never disagree.
//
// An unknown active value renders every tab unselected rather than guessing, which
// surfaces a routing bug as a visibly dead rail instead of a silently wrong highlight.
func RenderSectionRail(active Section) string {
	var b strings.Builder
	b.WriteString(`<nav class="section-rail" role="tablist" aria-label="Sections">`)
	for _, tab := range sectionTabs {
		selected := "false"
		if tab.section == active {
			selected = "true"
		}
		fmt.Fprintf(&b,
			`<button class="section-rail-item" id="section-tab-%s" type="button" role="tab" `+
				`data-section="%s" aria-selected="%s">%s<span class="section-rail-label">%s</span></button>`,
			tab.section, tab.section, selected, tab.glyph, tab.label,
		)
	}
	b.WriteString(`</nav>`)
	return b.String()
}
