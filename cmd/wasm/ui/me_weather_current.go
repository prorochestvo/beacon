package ui

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/dom"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// RenderMeWeatherCurrent returns the full HTML for the weather home tab.
// Auth-failure, loading, and error states short-circuit the content.
//
// This is the weather counterpart of the rates home tab: same section shell, same
// manage gear in the same slot, no back button — it is a top-level destination
// reached from the section rail, not a sub-screen of the city manager. Its gear
// leads to the weather settings screen, so entering settings keeps the section.
//
// All user-influenced or server-returned strings are passed through dom.Escape
// before interpolation. Numeric fields (temperature, humidity, wind) are
// rendered only when the corresponding pointer is non-nil, so partial
// observations from the server display cleanly without zero-value noise.
func RenderMeWeatherCurrent(state application.WeatherCurrentState) string {
	if state.AuthFailure {
		return fmt.Sprintf(`<p class="error-msg">%s</p>`, authFailureMsg)
	}

	var b strings.Builder

	b.WriteString(renderManageGearButton("Manage weather"))
	b.WriteString(renderWeatherCurrentTopbar())

	switch {
	case state.Loading:
		b.WriteString(`<p class="weather-loading">Loading…</p>`)
	case state.LoadError != nil:
		b.WriteString(`<p class="error-msg">`)
		b.WriteString(dom.Escape(state.LoadError.Error()))
		b.WriteString(`</p>`)
	case len(state.Items) == 0:
		b.WriteString(`<p class="weather-current-empty">No weather data yet. Add a city first.</p>`)
	default:
		b.WriteString(`<ul class="weather-current-list">`)
		for _, item := range state.Items {
			b.WriteString(renderWeatherCurrentCard(item))
		}
		b.WriteString(`</ul>`)
	}

	return RenderSectionShell(SectionWeather, b.String())
}

// renderWeatherCurrentTopbar emits the screen title. There is no back button: the
// section rail moves sideways between home tabs and the gear moves into settings,
// so this screen has nothing to go back to.
func renderWeatherCurrentTopbar() string {
	return `<div class="weather-topbar">` +
		`<span class="weather-title">Current weather</span>` +
		`</div>`
}

// renderWeatherForecastStrip emits the multi-week outlook as a horizontally scrollable row
// of one chip per day. It answers the three questions the screen exists for at a glance:
// rain or not, snow or not, and where the day sits against freezing.
//
// A dry day still gets a chip. The reader has to be able to tell a dry day from a wet one,
// and a strip that only showed the wet days would leave them counting gaps.
//
// Every server string goes through dom.Escape; numeric fields render only when their
// pointer is non-nil, so a day the provider had no answer for shows its date and nothing
// invented. An empty outlook emits nothing at all.
func renderWeatherForecastStrip(days []dto.WeatherForecastDayItem) string {
	if len(days) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(`<div class="weather-forecast-strip">`)
	for _, day := range days {
		fmt.Fprintf(&b, `<div class="weather-forecast-day weather-forecast-%s">`, dom.Escape(zeroStateClass(day.ZeroState)))
		fmt.Fprintf(&b, `<div class="weather-forecast-date">%s</div>`, dom.Escape(day.Label))

		b.WriteString(`<div class="weather-forecast-marks">`)
		switch {
		case day.Rain && day.Snow:
			b.WriteString(`🌧❄`)
		case day.Rain:
			b.WriteString(`🌧`)
		case day.Snow:
			b.WriteString(`❄`)
		default:
			// A non-breaking space keeps the dry chips the same height as the wet ones,
			// so the strip does not jump row to row.
			b.WriteString(`&nbsp;`)
		}
		b.WriteString(`</div>`)

		fmt.Fprintf(&b, `<div class="weather-forecast-zero">%s</div>`, zeroStateSymbol(day.ZeroState))

		if day.TempMax != nil && day.TempMin != nil {
			fmt.Fprintf(&b, `<div class="weather-forecast-temp">%.0f° / %.0f°</div>`, *day.TempMax, *day.TempMin)
		}

		if day.Rain && day.RainSum != nil {
			fmt.Fprintf(&b, `<div class="weather-forecast-amount">%.1f mm</div>`, *day.RainSum)
		} else if day.Snow && day.SnowfallSum != nil {
			fmt.Fprintf(&b, `<div class="weather-forecast-amount">%.1f cm</div>`, *day.SnowfallSum)
		}

		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// zeroStateSymbol maps the server's zero_state token to its indicator. An unknown or absent
// state renders as an em dash rather than a guess: the day carried no usable temperature
// bounds, which is not the same as sitting on either side of zero.
func zeroStateSymbol(zeroState string) string {
	switch zeroState {
	case "above":
		return "▲"
	case "crossing":
		return "↕"
	case "below":
		return "▼"
	default:
		return "—"
	}
}

// zeroStateClass maps the zero_state token to the chip modifier class. An unrecognised
// value falls back to "unknown", so a token this build does not know cannot inject a class
// name of its own.
func zeroStateClass(zeroState string) string {
	switch zeroState {
	case "above", "crossing", "below":
		return zeroState
	default:
		return "unknown"
	}
}

// renderWeatherCurrentCard emits one city weather card. When HasData is false,
// a "data not yet available" placeholder is shown instead of the numeric fields.
// All string fields from the server are escaped; emoji fields pass through unchanged
// because they contain no user-controlled input.
func renderWeatherCurrentCard(item dto.WeatherCurrentItem) string {
	var b strings.Builder
	b.WriteString(`<li class="weather-current-card">`)

	fmt.Fprintf(&b, `<div class="weather-current-city">%s</div>`, dom.Escape(item.DisplayName))
	fmt.Fprintf(&b, `<div class="weather-current-tz">%s</div>`, dom.Escape(item.Timezone))

	if !item.HasData {
		b.WriteString(`<p class="weather-current-nodata">Data not yet available.</p>`)
		// The outlook and the current reading are collected on different cadences, so a
		// city can hold one without the other. Returning here without the strip would
		// hide a whole screen of forecast over an unrelated absence.
		b.WriteString(renderWeatherForecastStrip(item.Days))
		b.WriteString(`</li>`)
		return b.String()
	}

	if item.ConditionEmoji != "" || item.ConditionText != "" {
		fmt.Fprintf(&b, `<div class="weather-current-condition">%s %s</div>`,
			dom.Escape(item.ConditionEmoji),
			dom.Escape(item.ConditionText))
	}

	if item.TempCurrent != nil {
		fmt.Fprintf(&b, `<div class="weather-current-temp">%.1f °C`, *item.TempCurrent)
		if item.TempFeels != nil {
			fmt.Fprintf(&b, ` <span class="weather-current-feels">feels %.1f °C</span>`, *item.TempFeels)
		}
		b.WriteString(`</div>`)
	}

	if item.TempMax != nil && item.TempMin != nil {
		fmt.Fprintf(&b, `<div class="weather-current-minmax">▲ %.1f °C / ▼ %.1f °C</div>`,
			*item.TempMax, *item.TempMin)
	}

	if item.Humidity != nil {
		fmt.Fprintf(&b, `<div class="weather-current-humidity">💧 %d%%</div>`, *item.Humidity)
	}

	if item.WindSpeed != nil {
		fmt.Fprintf(&b, `<div class="weather-current-wind">💨 %.1f m/s`, *item.WindSpeed)
		if item.WindDir != nil {
			fmt.Fprintf(&b, ` %d°`, *item.WindDir)
		}
		b.WriteString(`</div>`)
	}

	if item.Precip != nil {
		fmt.Fprintf(&b, `<div class="weather-current-precip">🌧 %.1f mm</div>`, *item.Precip)
	}

	if item.CloudCover != nil {
		fmt.Fprintf(&b, `<div class="weather-current-cloud">☁ %d%%</div>`, *item.CloudCover)
	}

	if item.SunriseLocal != "" || item.SunsetLocal != "" {
		b.WriteString(`<div class="weather-current-sun">`)
		if item.SunriseLocal != "" {
			b.WriteString("🌅 " + dom.Escape(item.SunriseLocal))
		}
		if item.SunsetLocal != "" {
			if item.SunriseLocal != "" {
				b.WriteString(`  `)
			}
			b.WriteString("🌇 " + dom.Escape(item.SunsetLocal))
		}
		b.WriteString(`</div>`)
	}

	if item.CapturedAt != "" {
		fmt.Fprintf(&b, `<div class="weather-current-captured">Updated: %s</div>`, dom.Escape(item.CapturedAt))
	}

	b.WriteString(renderWeatherForecastStrip(item.Days))

	b.WriteString(`</li>`)
	return b.String()
}
