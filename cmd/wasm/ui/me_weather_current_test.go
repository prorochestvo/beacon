package ui_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/ui"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// ptrOf is a generic helper for constructing pointer-typed test fixtures.
func ptrOf[T any](v T) *T { return &v }

func TestRenderMeWeatherCurrent(t *testing.T) {
	t.Parallel()

	t.Run("auth failure renders error message without topbar", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{AuthFailure: true})
		require.Contains(t, html, "error-msg")
		require.NotContains(t, html, "weather-topbar")
		require.NotContains(t, html, "weather-current-card")
	})

	t.Run("loading renders placeholder without card list", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{Loading: true})
		require.Contains(t, html, "weather-loading")
		require.NotContains(t, html, "weather-current-list")
	})

	t.Run("load error renders error message", func(t *testing.T) {
		t.Parallel()
		st := application.WeatherCurrentState{}
		st.LoadError = errString("upstream timeout")
		html := ui.RenderMeWeatherCurrent(st)
		require.Contains(t, html, "error-msg")
		require.Contains(t, html, "upstream timeout")
		require.NotContains(t, html, "weather-current-card")
	})

	t.Run("empty item list renders empty-state message", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{},
		})
		require.Contains(t, html, "weather-current-empty")
		require.NotContains(t, html, "weather-current-card")
	})

	t.Run("happy path renders topbar and city card", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{
				{
					LocationID:     "1234",
					DisplayName:    "Almaty",
					Timezone:       "Asia/Almaty",
					HasData:        true,
					TempCurrent:    ptrOf(22.5),
					ConditionText:  "Clear sky",
					ConditionEmoji: "☀️",
				},
			},
		})
		require.Contains(t, html, "weather-topbar")
		require.Contains(t, html, "weather-current-card")
		require.Contains(t, html, "Almaty")
		require.Contains(t, html, "Clear sky")
		require.Contains(t, html, "22.5")
	})

	t.Run("no-data city renders placeholder instead of numeric fields", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{
				{LocationID: "1234", DisplayName: "Almaty", Timezone: "Asia/Almaty", HasData: false},
			},
		})
		require.Contains(t, html, "weather-current-card")
		require.Contains(t, html, "weather-current-nodata")
		require.NotContains(t, html, "weather-current-temp")
		require.NotContains(t, html, "weather-current-condition")
	})

	t.Run("XSS in display name is escaped", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{
				{DisplayName: `<script>alert(1)</script>`, HasData: false},
			},
		})
		assert.NotContains(t, html, "<script>", "raw script tag must not appear in output")
		assert.Contains(t, html, "&lt;script&gt;", "angle bracket must be HTML-escaped")
	})

	t.Run("XSS in condition text is escaped", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{
				{DisplayName: "City", HasData: true, ConditionText: `"><img src=x onerror=alert(1)>`},
			},
		})
		assert.NotContains(t, html, `"><img`, "unescaped XSS payload must not appear")
		assert.Contains(t, html, "&lt;", "angle bracket must be escaped")
	})

	t.Run("all optional numeric fields rendered when present", func(t *testing.T) {
		t.Parallel()
		code := 2
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{{
				LocationID:     "x",
				DisplayName:    "Paris",
				Timezone:       "Europe/Paris",
				HasData:        true,
				TempCurrent:    ptrOf(18.0),
				TempFeels:      ptrOf(16.0),
				Humidity:       ptrOf(70),
				WindSpeed:      ptrOf(5.0),
				WindDir:        ptrOf(180),
				Precip:         ptrOf(0.5),
				CloudCover:     ptrOf(40),
				TempMax:        ptrOf(22.0),
				TempMin:        ptrOf(15.0),
				WeatherCode:    &code,
				ConditionText:  "Partly cloudy",
				ConditionEmoji: "⛅",
				SunriseLocal:   "05:30",
				SunsetLocal:    "21:15",
				CapturedAt:     "2026-06-30T00:00:00Z",
			}},
		})
		assert.Contains(t, html, "18.0")
		assert.Contains(t, html, "16.0")
		assert.Contains(t, html, "70%")
		assert.Contains(t, html, "5.0 m/s")
		assert.Contains(t, html, "180°")
		assert.Contains(t, html, "0.5 mm")
		assert.Contains(t, html, "40%")
		assert.Contains(t, html, "22.0")
		assert.Contains(t, html, "15.0")
		assert.Contains(t, html, "Partly cloudy")
		assert.Contains(t, html, "05:30")
		assert.Contains(t, html, "21:15")
		assert.Contains(t, html, "2026-06-30T00:00:00Z")
	})

	t.Run("feels-like is rendered when temp and feels are both present", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{{
				DisplayName: "City", HasData: true,
				TempCurrent: ptrOf(20.0), TempFeels: ptrOf(18.0),
			}},
		})
		require.Contains(t, html, "weather-current-feels")
		require.Contains(t, html, "18.0")
	})

	t.Run("feels-like is absent when only temp is present", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{
			Items: []dto.WeatherCurrentItem{{
				DisplayName: "City", HasData: true,
				TempCurrent: ptrOf(20.0),
			}},
		})
		assert.NotContains(t, html, "weather-current-feels")
	})
}

func TestRenderMeWeatherCurrentForecastStrip(t *testing.T) {
	t.Parallel()

	render := func(days []dto.WeatherForecastDayItem, hasData bool) string {
		return ui.RenderMeWeatherCurrent(application.WeatherCurrentState{Items: []dto.WeatherCurrentItem{{
			LocationID:  "1234",
			DisplayName: "Astana",
			Timezone:    "Asia/Almaty",
			HasData:     hasData,
			TempCurrent: ptrOf(21.5),
			Days:        days,
		}}})
	}

	t.Run("renders one chip per day with its badges and temperatures", func(t *testing.T) {
		t.Parallel()
		html := render([]dto.WeatherForecastDayItem{
			{Date: "2026-08-21", Label: "Fri 21 Aug", TempMax: ptrOf(21.5), TempMin: ptrOf(11.2), ZeroState: "above"},
			{Date: "2026-08-22", Label: "Sat 22 Aug", TempMax: ptrOf(2.0), TempMin: ptrOf(-3.0), Rain: true, RainSum: ptrOf(4.2), ZeroState: "crossing"},
			{Date: "2026-08-23", Label: "Sun 23 Aug", TempMax: ptrOf(-2.0), TempMin: ptrOf(-9.0), Snow: true, SnowfallSum: ptrOf(3.5), ZeroState: "below"},
		}, true)

		require.Contains(t, html, "weather-forecast-strip")
		assert.Equal(t, 3, strings.Count(html, `class="weather-forecast-day`))
		assert.Contains(t, html, "Fri 21 Aug")
		assert.Contains(t, html, "weather-forecast-above")
		assert.Contains(t, html, "weather-forecast-crossing")
		assert.Contains(t, html, "weather-forecast-below")
		assert.Contains(t, html, "4.2 mm")
		assert.Contains(t, html, "3.5 cm")
		assert.Contains(t, html, "▲")
		assert.Contains(t, html, "↕")
		assert.Contains(t, html, "▼")
	})

	t.Run("a dry day still gets a chip so wet days are countable", func(t *testing.T) {
		t.Parallel()
		html := render([]dto.WeatherForecastDayItem{
			{Date: "2026-08-21", Label: "Fri 21 Aug", TempMax: ptrOf(21.5), TempMin: ptrOf(11.2), ZeroState: "above"},
		}, true)

		assert.Contains(t, html, "Fri 21 Aug")
		assert.Contains(t, html, "22° / 11°", "the chip rounds to whole degrees")
		assert.NotContains(t, html, "mm")
	})

	t.Run("no outlook emits no strip at all", func(t *testing.T) {
		t.Parallel()
		html := render(nil, true)
		assert.NotContains(t, html, "weather-forecast-strip")
	})

	t.Run("the strip survives a city with no reading yet", func(t *testing.T) {
		t.Parallel()
		html := render([]dto.WeatherForecastDayItem{
			{Date: "2026-08-22", Label: "Sat 22 Aug", Rain: true, RainSum: ptrOf(4.2), ZeroState: "above"},
		}, false)

		assert.Contains(t, html, "weather-current-nodata")
		assert.Contains(t, html, "weather-forecast-strip")
		assert.Contains(t, html, "Sat 22 Aug")
	})

	t.Run("a day with no bounds shows neither a temperature nor a guessed zero state", func(t *testing.T) {
		t.Parallel()
		html := render([]dto.WeatherForecastDayItem{
			{Date: "2026-08-22", Label: "Sat 22 Aug"},
		}, true)

		assert.Contains(t, html, "weather-forecast-unknown")
		assert.Contains(t, html, "—")
		assert.NotContains(t, html, "weather-forecast-temp")
	})

	t.Run("server-supplied strings are escaped", func(t *testing.T) {
		t.Parallel()
		html := render([]dto.WeatherForecastDayItem{
			{Date: "x", Label: `<script>alert("x")</script>`, ZeroState: `" onload="x`},
		}, true)

		assert.NotContains(t, html, "<script>")
		assert.NotContains(t, html, `onload="x`)
		assert.Contains(t, html, "weather-forecast-unknown", "an unrecognised state must not become a class name of its own")
	})
}
