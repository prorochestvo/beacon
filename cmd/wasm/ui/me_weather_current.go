package ui

import (
	"fmt"
	"strings"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/dom"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// RenderMeWeatherCurrent returns the full HTML for the on-demand current-weather
// screen. Auth-failure, loading, and error states short-circuit the content.
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

	b.WriteString(renderWeatherCurrentTopbar())

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

	if len(state.Items) == 0 {
		b.WriteString(`<p class="weather-current-empty">No weather data yet. Add a city first.</p>`)
		return b.String()
	}

	b.WriteString(`<ul class="weather-current-list">`)
	for _, item := range state.Items {
		b.WriteString(renderWeatherCurrentCard(item))
	}
	b.WriteString(`</ul>`)
	return b.String()
}

// renderWeatherCurrentTopbar emits the screen header with a back button.
// The back button id is "weather-current-back" so the WASM dispatcher can
// route it separately from the city-picker back button.
func renderWeatherCurrentTopbar() string {
	return `<div class="weather-topbar">` +
		`<button class="weather-back" id="weather-current-back" type="button">← Back</button>` +
		`<span class="weather-title">Current weather</span>` +
		`</div>`
}

// renderWeatherCurrentCard emits one city weather card. When HasData is false,
// a "data not yet available" placeholder is shown instead of the numeric fields.
// All string fields from the server are escaped; emoji fields pass through unchanged
// because they contain no user-controlled input.
func renderWeatherCurrentCard(item dto.WeatherCurrentItem) string {
	var b strings.Builder
	b.WriteString(`<li class="weather-current-card">`)

	b.WriteString(fmt.Sprintf(`<div class="weather-current-city">%s</div>`, dom.Escape(item.DisplayName)))
	b.WriteString(fmt.Sprintf(`<div class="weather-current-tz">%s</div>`, dom.Escape(item.Timezone)))

	if !item.HasData {
		b.WriteString(`<p class="weather-current-nodata">Data not yet available.</p>`)
		b.WriteString(`</li>`)
		return b.String()
	}

	if item.ConditionEmoji != "" || item.ConditionText != "" {
		b.WriteString(fmt.Sprintf(
			`<div class="weather-current-condition">%s %s</div>`,
			item.ConditionEmoji,
			dom.Escape(item.ConditionText),
		))
	}

	if item.TempCurrent != nil {
		b.WriteString(fmt.Sprintf(`<div class="weather-current-temp">%.1f °C`, *item.TempCurrent))
		if item.TempFeels != nil {
			b.WriteString(fmt.Sprintf(` <span class="weather-current-feels">feels %.1f °C</span>`, *item.TempFeels))
		}
		b.WriteString(`</div>`)
	}

	if item.TempMax != nil && item.TempMin != nil {
		b.WriteString(fmt.Sprintf(
			`<div class="weather-current-minmax">▲ %.1f °C / ▼ %.1f °C</div>`,
			*item.TempMax, *item.TempMin,
		))
	}

	if item.Humidity != nil {
		b.WriteString(fmt.Sprintf(`<div class="weather-current-humidity">💧 %d%%</div>`, *item.Humidity))
	}

	if item.WindSpeed != nil {
		b.WriteString(fmt.Sprintf(`<div class="weather-current-wind">💨 %.1f m/s`, *item.WindSpeed))
		if item.WindDir != nil {
			b.WriteString(fmt.Sprintf(` %d°`, *item.WindDir))
		}
		b.WriteString(`</div>`)
	}

	if item.Precip != nil {
		b.WriteString(fmt.Sprintf(`<div class="weather-current-precip">🌧 %.1f mm</div>`, *item.Precip))
	}

	if item.CloudCover != nil {
		b.WriteString(fmt.Sprintf(`<div class="weather-current-cloud">☁ %d%%</div>`, *item.CloudCover))
	}

	if item.SunriseLocal != "" || item.SunsetLocal != "" {
		b.WriteString(`<div class="weather-current-sun">`)
		if item.SunriseLocal != "" {
			b.WriteString(fmt.Sprintf(`🌅 %s`, dom.Escape(item.SunriseLocal)))
		}
		if item.SunsetLocal != "" {
			if item.SunriseLocal != "" {
				b.WriteString(`  `)
			}
			b.WriteString(fmt.Sprintf(`🌇 %s`, dom.Escape(item.SunsetLocal)))
		}
		b.WriteString(`</div>`)
	}

	if item.CapturedAt != "" {
		b.WriteString(fmt.Sprintf(`<div class="weather-current-captured">Updated: %s</div>`, dom.Escape(item.CapturedAt)))
	}

	b.WriteString(`</li>`)
	return b.String()
}
