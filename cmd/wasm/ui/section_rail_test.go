package ui_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/cmd/wasm/application"
	"github.com/seilbekskindirov/beacon/cmd/wasm/ui"
)

func TestRenderSectionRail(t *testing.T) {
	t.Parallel()

	t.Run("both sections render with labels and routing attributes", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSectionRail(ui.SectionRates)

		assert.Contains(t, html, `role="tablist"`)
		assert.Contains(t, html, `data-section="rates"`)
		assert.Contains(t, html, `data-section="weather"`)
		// #2 requires each tab to carry an unambiguous label, not a bare glyph.
		assert.Contains(t, html, `>Rates</span>`)
		assert.Contains(t, html, `>Weather</span>`)
		assert.Equal(t, 2, strings.Count(html, `class="section-rail-item"`))
		assert.Equal(t, 2, strings.Count(html, "<svg"), "each tab carries its own glyph")
	})

	t.Run("exactly one tab is selected, and it is the active one", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			active   ui.Section
			selected string
		}{
			{ui.SectionRates, `id="section-tab-rates" type="button" role="tab" data-section="rates" aria-selected="true"`},
			{ui.SectionWeather, `id="section-tab-weather" type="button" role="tab" data-section="weather" aria-selected="true"`},
		} {
			html := ui.RenderSectionRail(tc.active)
			assert.Equalf(t, 1, strings.Count(html, `aria-selected="true"`),
				"exactly one tab may be selected for %s", tc.active)
			assert.Containsf(t, html, tc.selected, "%s must be the selected tab", tc.active)
		}
	})

	t.Run("an unknown section selects nothing rather than guessing", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSectionRail(ui.Section("nonsense"))
		assert.NotContains(t, html, `aria-selected="true"`,
			"a routing bug must surface as a dead rail, not as a silently wrong highlight")
		assert.Equal(t, 2, strings.Count(html, `class="section-rail-item"`), "both tabs still render")
	})
}

func TestRenderSectionShell(t *testing.T) {
	t.Parallel()

	t.Run("wraps the body in the rail plus panel layout", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSectionShell(ui.SectionWeather, `<p id="body-marker">x</p>`)

		require.Contains(t, html, `class="app-shell"`)
		require.Contains(t, html, `class="section-panel"`)
		assert.Contains(t, html, `<p id="body-marker">x</p>`)
		assert.Contains(t, html, `data-section="weather" aria-selected="true"`)

		railIdx := strings.Index(html, `class="section-rail"`)
		panelIdx := strings.Index(html, `class="section-panel"`)
		require.NotEqual(t, -1, railIdx)
		require.NotEqual(t, -1, panelIdx)
		assert.Less(t, railIdx, panelIdx, "the rail must precede the panel so it renders as the left column")
	})

	t.Run("an empty body still renders a usable rail", func(t *testing.T) {
		t.Parallel()
		html := ui.RenderSectionShell(ui.SectionRates, "")
		assert.Contains(t, html, `data-section="weather"`, "the other section must stay reachable")
	})
}

// TestMiniAppNavigationContract pins the 2×2 section/mode matrix across all four
// Mini App screens: the rail marks the section, the gear appears only on the home
// tabs, and the ← Back button only in settings. Issues #1 and #2 are both about
// this contract, so it is asserted in one place rather than scattered across the
// four per-screen test files.
func TestMiniAppNavigationContract(t *testing.T) {
	t.Parallel()

	homeRates := ui.RenderMeSubscriptions(application.MeSubscriptionsState{})
	homeWeather := ui.RenderMeWeatherCurrent(application.WeatherCurrentState{})
	settingsRates := ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{
		ActiveView: application.EditViewList,
	})
	settingsWeather := ui.RenderMeWeatherCities(application.WeatherCitiesState{})

	t.Run("every screen carries the rail with its own section active", func(t *testing.T) {
		t.Parallel()
		for name, tc := range map[string]struct {
			html   string
			active string
		}{
			"home rates":       {homeRates, "rates"},
			"home weather":     {homeWeather, "weather"},
			"settings rates":   {settingsRates, "rates"},
			"settings weather": {settingsWeather, "weather"},
		} {
			assert.Containsf(t, tc.html, `class="section-rail"`, "%s must render the rail", name)
			assert.Containsf(t, tc.html, `data-section="`+tc.active+`" aria-selected="true"`,
				"%s must mark %s as the active section", name, tc.active)
			assert.Equalf(t, 1, strings.Count(tc.html, `aria-selected="true"`),
				"%s must mark exactly one section active", name)
		}
	})

	t.Run("the gear is the only settings entry and appears on home tabs only", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, homeRates, `id="me-manage"`)
		assert.Contains(t, homeWeather, `id="me-manage"`, "both home tabs must offer the same settings entry")
		assert.NotContains(t, settingsRates, `id="me-manage"`, "already in settings")
		assert.NotContains(t, settingsWeather, `id="me-manage"`, "already in settings")
	})

	t.Run("back buttons exist only in settings", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, settingsRates, `id="me-edit-back"`)
		assert.Contains(t, settingsWeather, `id="weather-back"`)
		// The home tabs are top-level destinations: the rail moves sideways and
		// the gear moves into settings, so there is nothing to go back to.
		assert.NotContains(t, homeRates, `id="me-edit-back"`)
		assert.NotContains(t, homeWeather, `weather-current-back`)
	})

	t.Run("the retired weather entry points are gone everywhere", func(t *testing.T) {
		t.Parallel()
		for name, html := range map[string]string{
			"home rates":       homeRates,
			"home weather":     homeWeather,
			"settings rates":   settingsRates,
			"settings weather": settingsWeather,
		} {
			assert.NotContainsf(t, html, "me-weather-cloud", "%s: the cloud is a tab glyph now, not a button", name)
			assert.NotContainsf(t, html, "weather-view-current", "%s: current weather is a home tab, not a sub-screen", name)
			assert.NotContainsf(t, html, "weather-current-back", "%s: the current-weather back button is retired", name)
		}
	})

	t.Run("auth failure short-circuits every screen without a rail", func(t *testing.T) {
		t.Parallel()
		for name, html := range map[string]string{
			"home rates":       ui.RenderMeSubscriptions(application.MeSubscriptionsState{AuthFailure: true}),
			"home weather":     ui.RenderMeWeatherCurrent(application.WeatherCurrentState{AuthFailure: true}),
			"settings rates":   ui.RenderMeSubscriptionsEdit(application.MeSubscriptionsEditState{AuthFailure: true}),
			"settings weather": ui.RenderMeWeatherCities(application.WeatherCitiesState{AuthFailure: true}),
		} {
			assert.Containsf(t, html, "must be opened from the bot", "%s must keep its auth-failure message", name)
			assert.NotContainsf(t, html, `class="section-rail"`, "%s must not render a rail with no content", name)
		}
	})
}
