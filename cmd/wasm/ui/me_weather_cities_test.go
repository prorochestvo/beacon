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
				{ID: "abc123", LocationID: "loc-abc", DisplayName: "Almaty", Country: "Kazakhstan",
					Timezone: "Asia/Almaty", NotifyHour: 9, NotifyKind: "morning_summary"},
			},
		})
		// Rows are now rendered as weather-kind-row inside weather-city-group.
		require.Contains(t, html, "weather-kind-row")
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

	t.Run("duplicate result labels are rendered once keeping original indices", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			SearchQuery: "astana",
			SearchResults: []dto.WeatherCitySearchItem{
				{LocationID: "1", DisplayName: "Astana", Admin1: "Panjshir", Country: "Afghanistan"},
				{LocationID: "2", DisplayName: "Astana", Admin1: "Astana", Country: "Kazakhstan"},
				{LocationID: "3", DisplayName: "Astana", Admin1: "Panjshir", Country: "Afghanistan"},
			},
		})
		assert.Equal(t, 1, strings.Count(html, "Astana, Panjshir, Afghanistan"), "identical labels must collapse to one row")
		assert.Contains(t, html, `data-index="0"`)
		assert.Contains(t, html, `data-index="1"`)
		assert.NotContains(t, html, `data-index="2"`, "duplicate keeps first occurrence's index")
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

func TestGroupWeatherCities(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns empty groups", func(t *testing.T) {
		t.Parallel()
		result := ui.GroupWeatherCities(nil)
		assert.Empty(t, result)
	})

	t.Run("single city single kind produces one group with one row", func(t *testing.T) {
		t.Parallel()
		rows := []dto.WeatherCityRow{
			{ID: "c1", LocationID: "loc1", DisplayName: "Almaty", NotifyKind: "morning_summary"},
		}
		groups := ui.GroupWeatherCities(rows)
		require.Len(t, groups, 1)
		assert.Equal(t, "loc1", groups[0].LocationID)
		assert.Equal(t, "Almaty", groups[0].DisplayName)
		assert.Len(t, groups[0].Rows, 1)
	})

	t.Run("two rows for same location_id produce one group with two rows", func(t *testing.T) {
		t.Parallel()
		rows := []dto.WeatherCityRow{
			{ID: "c1", LocationID: "loc1", DisplayName: "Almaty", NotifyKind: "morning_summary"},
			{ID: "c2", LocationID: "loc1", DisplayName: "Almaty", NotifyKind: "alert_heat", ConditionValue: "35"},
		}
		groups := ui.GroupWeatherCities(rows)
		require.Len(t, groups, 1, "two kinds for same location must be one group")
		assert.Len(t, groups[0].Rows, 2)
	})

	t.Run("two different location_ids produce two groups", func(t *testing.T) {
		t.Parallel()
		rows := []dto.WeatherCityRow{
			{ID: "c1", LocationID: "loc1", DisplayName: "Almaty", NotifyKind: "morning_summary"},
			{ID: "c2", LocationID: "loc2", DisplayName: "Astana", NotifyKind: "morning_summary"},
		}
		groups := ui.GroupWeatherCities(rows)
		require.Len(t, groups, 2)
		assert.Equal(t, "loc1", groups[0].LocationID)
		assert.Equal(t, "loc2", groups[1].LocationID)
	})

	t.Run("insertion order within group is preserved", func(t *testing.T) {
		t.Parallel()
		rows := []dto.WeatherCityRow{
			{ID: "c1", LocationID: "loc1", NotifyKind: "morning_summary"},
			{ID: "c2", LocationID: "loc1", NotifyKind: "alert_heat"},
			{ID: "c3", LocationID: "loc1", NotifyKind: "alert_frost"},
		}
		groups := ui.GroupWeatherCities(rows)
		require.Len(t, groups, 1)
		require.Len(t, groups[0].Rows, 3)
		assert.Equal(t, "c1", groups[0].Rows[0].ID)
		assert.Equal(t, "c2", groups[0].Rows[1].ID)
		assert.Equal(t, "c3", groups[0].Rows[2].ID)
	})
}

func TestRenderMeWeatherCities_AlertForm(t *testing.T) {
	t.Parallel()

	baseCity := dto.WeatherCityRow{
		ID:          "c1",
		LocationID:  "loc1",
		DisplayName: "Almaty",
		Country:     "Kazakhstan",
		Timezone:    "Asia/Almaty",
		NotifyKind:  "morning_summary",
		NotifyHour:  7,
	}

	t.Run("city group shows Add alert button when no form open", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{baseCity},
		})
		assert.Contains(t, html, "weather-add-alert-btn")
		assert.Contains(t, html, `data-location-id="loc1"`)
		assert.NotContains(t, html, "weather-alert-form")
	})

	t.Run("open alert form renders kind selector and value input for heat", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_heat",
			AlertFormValue:      "35",
		})
		assert.Contains(t, html, "weather-alert-form")
		assert.Contains(t, html, "weather-alert-kind")
		assert.Contains(t, html, "weather-alert-value")
		assert.Contains(t, html, `value="35"`)
		// "Add alert" button must not appear for the city whose form is open.
		assert.NotContains(t, html, "weather-add-alert-btn")
	})

	t.Run("thunderstorm form hides value input", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_thunderstorm",
		})
		assert.Contains(t, html, "weather-alert-form")
		// Value input must not appear for thunderstorm (no numeric threshold).
		assert.NotContains(t, html, "weather-alert-value")
	})

	t.Run("alert form renders save and cancel buttons", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_heat",
		})
		assert.Contains(t, html, "weather-alert-save")
		assert.Contains(t, html, "weather-alert-cancel")
	})

	t.Run("alert form error is displayed", func(t *testing.T) {
		t.Parallel()
		st := application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_heat",
		}
		st.AlertSaveError = errString("condition_value must be a valid number")
		html := ui.RenderMeWeatherCities(st)
		assert.Contains(t, html, "weather-alert-error")
		assert.Contains(t, html, "condition_value must be a valid number")
	})

	t.Run("alert form error is HTML-escaped", func(t *testing.T) {
		t.Parallel()
		st := application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_heat",
		}
		st.AlertSaveError = errString("<script>xss</script>")
		html := ui.RenderMeWeatherCities(st)
		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;script&gt;")
	})

	t.Run("heat alert row shows kind label with threshold", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				baseCity,
				{ID: "c2", LocationID: "loc1", DisplayName: "Almaty", Timezone: "Asia/Almaty",
					NotifyKind: "alert_heat", ConditionValue: "35"},
			},
		})
		assert.Contains(t, html, "Heat alert ≥ 35°C")
	})

	t.Run("frost alert row shows kind label with threshold", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				baseCity,
				{ID: "c2", LocationID: "loc1", DisplayName: "Almaty", Timezone: "Asia/Almaty",
					NotifyKind: "alert_frost", ConditionValue: "0"},
			},
		})
		assert.Contains(t, html, "Frost alert ≤ 0°C")
	})

	t.Run("thunderstorm row shows kind label without threshold", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				baseCity,
				{ID: "c2", LocationID: "loc1", DisplayName: "Almaty", Timezone: "Asia/Almaty",
					NotifyKind: "alert_thunderstorm"},
			},
		})
		assert.Contains(t, html, "Thunderstorm alert")
	})

	t.Run("rain_alert row shows kind label with threshold percent", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				baseCity,
				{ID: "c2", LocationID: "loc1", DisplayName: "Almaty", Timezone: "Asia/Almaty",
					NotifyKind: "rain_alert", ConditionValue: "70"},
			},
		})
		assert.Contains(t, html, "Rain alert ≥ 70% within 6h")
	})

	t.Run("rain_alert form shows value input (numeric threshold visible)", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "rain_alert",
			AlertFormValue:      "70",
		})
		assert.Contains(t, html, "weather-alert-form")
		assert.Contains(t, html, "weather-alert-value")
		assert.Contains(t, html, `value="70"`)
	})

	t.Run("rain_alert option appears in kind selector", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_heat",
		})
		assert.Contains(t, html, `value="rain_alert"`)
		assert.Contains(t, html, "Rain alert (%)")
	})

	t.Run("thaw option does not appear in kind selector (forced, not user-addable)", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_heat",
		})
		assert.NotContains(t, html, `value="alert_thaw"`)
	})

	t.Run("thaw alert row shows kind label, an always-on chip, and no delete button", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{
				baseCity,
				{ID: "c2", LocationID: "loc1", DisplayName: "Almaty", Timezone: "Asia/Almaty",
					NotifyKind: "alert_thaw"},
			},
		})
		assert.Contains(t, html, "Thaw alert")
		assert.Contains(t, html, "weather-kind-locked")
		assert.Contains(t, html, "always on")

		// Scope the delete-button-absence check to the thaw row's own <li> so it cannot
		// false-pass on the sibling non-thaw row's delete button rendered elsewhere in
		// the same HTML string.
		thawIdx := strings.Index(html, "Thaw alert")
		require.NotEqual(t, -1, thawIdx, "thaw row label must be present")
		liStart := strings.LastIndex(html[:thawIdx], "<li")
		require.NotEqual(t, -1, liStart, "thaw row must be wrapped in an <li>")
		liEndRel := strings.Index(html[liStart:], "</li>")
		require.NotEqual(t, -1, liEndRel, "thaw row <li> must be closed")
		thawRowHTML := html[liStart : liStart+liEndRel+len("</li>")]

		assert.NotContains(t, thawRowHTML, `data-id="c2"`, "thaw row must not carry a delete data-id")
		assert.NotContains(t, thawRowHTML, "weather-city-delete", "thaw row must render no delete button")

		// The sibling non-thaw row must still keep its own delete button and data-id.
		assert.Contains(t, html, `data-id="`+baseCity.ID+`"`, "non-thaw sibling row must keep its data-id")
		assert.Contains(t, html, "weather-city-delete", "non-thaw sibling row must keep its delete button")
	})

	t.Run("each city group renders a Remove city control carrying data-location-id", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities: []dto.WeatherCityRow{baseCity},
		})
		assert.Contains(t, html, "weather-city-remove")
		assert.Contains(t, html, `data-location-id="loc1"`)
	})

	t.Run("different city gets Add alert button when another city has form open", func(t *testing.T) {
		t.Parallel()
		city2 := dto.WeatherCityRow{
			ID: "c3", LocationID: "loc2", DisplayName: "Astana",
			Country: "Kazakhstan", Timezone: "Asia/Astana", NotifyKind: "morning_summary",
		}
		html := ui.RenderMeWeatherCities(application.WeatherCitiesState{
			Cities:              []dto.WeatherCityRow{baseCity, city2},
			AlertFormLocationID: "loc1",
			AlertFormKind:       "alert_heat",
		})
		// loc1 has the form open; loc2 must still show the add-alert button.
		assert.Contains(t, html, "weather-alert-form")
		assert.Contains(t, html, `data-location-id="loc2"`)
	})
}

// errString implements error with a plain message, defined once for the test file.
type errString string

func (e errString) Error() string { return string(e) }
