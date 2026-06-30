package ui_test

import (
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
		require.Contains(t, html, "weather-current-back")
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
