package ui_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/ui"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

func TestRenderMeWeatherCities(t *testing.T) {
	t.Parallel()

	t.Run("auth failure renders error message", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{AuthFailure: true})
		require.Contains(t, html, "error-msg")
		require.NotContains(t, html, "weather-topbar")
	})

	t.Run("loading renders loading placeholder", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{Loading: true})
		require.Contains(t, html, "weather-loading")
		require.NotContains(t, html, "weather-search-section")
	})

	t.Run("load error renders error message", func(t *testing.T) {
		t.Parallel()
		st := application.WeatherCitiesState{}
		st.LoadError = errString("db down")
		html := ui.RenderMeWeatherCities(st)
		require.Contains(t, html, "error-msg")
		require.Contains(t, html, "db down")
		require.NotContains(t, html, "weather-cities-section")
	})

	t.Run("happy path renders topbar and sections", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				{ID: "c1", DisplayName: "Almaty", Country: "Kazakhstan", Admin1: "Almaty",
					Timezone: "Asia/Almaty", NotifyHour: 7},
			},
		})
		require.Contains(t, html, "weather-topbar")
		require.Contains(t, html, "weather-search-section")
		require.Contains(t, html, "weather-cities-section")
		require.Contains(t, html, "Almaty")
	})

	t.Run("empty city list renders empty message", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{},
		})
		require.Contains(t, html, "weather-cities-empty")
		require.NotContains(t, html, "weather-city-row")
	})

	t.Run("city rows are rendered with delete button", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				{ID: "abc123", DisplayName: "Almaty", Country: "Kazakhstan",
					Timezone: "Asia/Almaty", NotifyHour: 9},
			},
		})
		require.Contains(t, html, "weather-city-row")
		require.Contains(t, html, `data-id="abc123"`)
		require.Contains(t, html, "weather-city-delete")
		require.Contains(t, html, "09:00")
	})

	t.Run("city display name XSS is escaped", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				{ID: "x", DisplayName: `<script>alert(1)</script>`, Country: "Evil", Timezone: "UTC"},
			},
		})
		assert.NotContains(t, html, "<script>", "raw script tag must not appear in output")
		assert.Contains(t, html, "&lt;script&gt;", "script tag must be HTML-escaped")
	})

	t.Run("search results are rendered when present", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			SearchQuery: "Alm",
			SearchResults: []dto.WeatherCitySearchItem{
				{LocationID: "1234", DisplayName: "Almaty", Country: "Kazakhstan", Admin1: "Almaty", Timezone: "Asia/Almaty"},
			},
		})
		require.Contains(t, html, "weather-search-results")
		require.Contains(t, html, `data-index="0"`)
		require.Contains(t, html, "Almaty")
	})

	t.Run("search result XSS is escaped", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			SearchQuery: "xss",
			SearchResults: []dto.WeatherCitySearchItem{
				{LocationID: "1", DisplayName: `"><img src=x onerror=alert(1)>`, Country: "Evil"},
			},
		})
		assert.NotContains(t, html, `"><img`, "unescaped XSS payload must not appear")
		assert.Contains(t, html, "&lt;", "angle bracket must be escaped")
	})

	t.Run("selected result gets selected class", func(t *testing.T) {
		t.Parallel()
		item := dto.WeatherCitySearchItem{LocationID: "777", DisplayName: "Paris"}
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			SearchQuery:   "Paris",
			SearchResults: []dto.WeatherCitySearchItem{item},
			Selected:      &item,
		})
		require.Contains(t, html, "weather-search-item-selected")
		require.Contains(t, html, "weather-save-btn")
		require.Contains(t, html, "weather-clear-btn")
	})

	t.Run("save button absent when nothing selected", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			SearchQuery: "Paris",
			SearchResults: []dto.WeatherCitySearchItem{
				{LocationID: "777", DisplayName: "Paris"},
			},
		})
		assert.NotContains(t, html, "weather-save-btn")
	})

	t.Run("no results message shown when query non-empty but results empty", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			SearchQuery:   "xyzzy",
			SearchResults: []dto.WeatherCitySearchItem{},
		})
		require.Contains(t, html, "No cities found.")
	})

	t.Run("search error is displayed", func(t *testing.T) {
		t.Parallel()
		st := application.WeatherCitiesState{SearchQuery: "fail"}
		st.SearchError = errString("geocoder is down")
		html := ui.RenderMeWeatherCities(st)
		require.Contains(t, html, "weather-search-error")
		require.Contains(t, html, "geocoder is down")
	})

	t.Run("save error is displayed", func(t *testing.T) {
		t.Parallel()
		st := application.WeatherCitiesState{}
		st.SaveError = errString("timezone invalid")
		html := ui.RenderMeWeatherCities(st)
		require.Contains(t, html, "weather-save-error")
		require.Contains(t, html, "timezone invalid")
	})
}

// errString implements error with a plain message, defined once for the test file.
type errString string

func (e errString) Error() string { return string(e) }
