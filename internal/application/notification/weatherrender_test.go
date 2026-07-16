package notification

import (
	"testing"
	"time"
	_ "time/tzdata" // embedded IANA tzdata so LoadLocation works without system tzdata

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMorningSummary(t *testing.T) {
	t.Parallel()

	// Base city in Asia/Almaty (UTC+5, no DST).
	baseCity := domain.WeatherUserCity{
		ID:          "WUC01",
		DisplayName: "Алматы",
		Timezone:    "Asia/Almaty",
		UserType:    domain.UserTypeTelegram,
		UserID:      "123",
	}

	// Prepare a full Open-Meteo observation with all fields set.
	tempMax := 31.6
	tempMin := 20.8
	precipSum := 1.1
	precipProb := 69
	code := 53 // Moderate drizzle
	// After fix #6 sunrise/sunset are stored as correct UTC instants.
	// 04:15 local in Asia/Almaty (UTC+5) = 23:15 UTC on the previous calendar day.
	// 19:36 local in Asia/Almaty (UTC+5) = 14:36 UTC on the same calendar day.
	sunriseTime := time.Date(2026, 6, 29, 23, 15, 0, 0, time.UTC)
	sunsetTime := time.Date(2026, 6, 30, 14, 36, 0, 0, time.UTC)
	fullObs := domain.WeatherObservation{
		Provider:      "open-meteo",
		LocationID:    "12345",
		TempMax:       &tempMax,
		TempMin:       &tempMin,
		PrecipSum:     &precipSum,
		PrecipProbMax: &precipProb,
		WeatherCode:   &code,
		Sunrise:       &sunriseTime,
		Sunset:        &sunsetTime,
	}

	t.Run("single provider renders all fields", func(t *testing.T) {
		t.Parallel()
		result, err := RenderMorningSummary(baseCity, fullObs)
		require.NoError(t, err)
		assert.Contains(t, result, "Алматы")
		assert.Contains(t, result, "+31.6°C")
		assert.Contains(t, result, "+20.8°C")
		assert.Contains(t, result, "Moderate drizzle")
		assert.Contains(t, result, "1.1 mm")
		assert.Contains(t, result, "69%")
		assert.Contains(t, result, "04:15")
		assert.Contains(t, result, "19:36")
		// provider label must not appear in single-provider layout
		assert.NotContains(t, result, "Open-Meteo")
	})

	t.Run("timestamp in city timezone includes offset", func(t *testing.T) {
		t.Parallel()
		result, err := RenderMorningSummary(baseCity, fullObs)
		require.NoError(t, err)
		// Almaty is UTC+5 → timestamp must carry +05 offset
		assert.Contains(t, result, "+05")
	})

	t.Run("city name is HTML-escaped", func(t *testing.T) {
		t.Parallel()
		xssCity := baseCity
		xssCity.DisplayName = "<script>alert(1)</script>"
		result, err := RenderMorningSummary(xssCity, fullObs)
		require.NoError(t, err)
		assert.NotContains(t, result, "<script>")
		assert.Contains(t, result, "&lt;script&gt;")
	})

	t.Run("nil precip prob renders dash not zero", func(t *testing.T) {
		t.Parallel()
		obsNullProb := domain.WeatherObservation{
			Provider:  "open-meteo",
			TempMax:   &tempMax,
			TempMin:   &tempMin,
			PrecipSum: &precipSum,
			// PrecipProbMax intentionally nil
		}
		result, err := RenderMorningSummary(baseCity, obsNullProb)
		require.NoError(t, err)
		assert.Contains(t, result, "(—)")
		assert.NotContains(t, result, "(0%)")
	})

	t.Run("nil precip sum renders dash not zero", func(t *testing.T) {
		t.Parallel()
		obsNullSum := domain.WeatherObservation{
			Provider:      "open-meteo",
			TempMax:       &tempMax,
			TempMin:       &tempMin,
			PrecipProbMax: &precipProb,
			// PrecipSum intentionally nil
		}
		result, err := RenderMorningSummary(baseCity, obsNullSum)
		require.NoError(t, err)
		assert.Contains(t, result, "— mm")
		assert.NotContains(t, result, "0.0 mm")
	})

	t.Run("nil weather code omits condition line", func(t *testing.T) {
		t.Parallel()
		obsNoCode := domain.WeatherObservation{
			Provider: "open-meteo",
			TempMax:  &tempMax,
			TempMin:  &tempMin,
		}
		result, err := RenderMorningSummary(baseCity, obsNoCode)
		require.NoError(t, err)
		// No condition line; temp line still present.
		assert.Contains(t, result, "+31.6°C")
		// Condition text and emoji must be absent — the line is omitted, not rendered as garbage.
		assert.NotContains(t, result, "Clear sky", "condition text must be absent when WeatherCode is nil")
		assert.NotContains(t, result, "Unknown", "fallback WMO text must not appear when WeatherCode is nil")
		assert.NotContains(t, result, "❓", "fallback emoji must not appear when WeatherCode is nil")
	})

	t.Run("no sunrise/sunset omits that line", func(t *testing.T) {
		t.Parallel()
		obsNoSun := domain.WeatherObservation{
			Provider: "open-meteo",
			TempMax:  &tempMax,
			TempMin:  &tempMin,
		}
		result, err := RenderMorningSummary(baseCity, obsNoSun)
		require.NoError(t, err)
		assert.NotContains(t, result, "🌅")
		assert.NotContains(t, result, "🌇")
	})

	t.Run("negative temperature renders with unicode minus sign", func(t *testing.T) {
		t.Parallel()
		negTemp := -5.2
		obsNeg := domain.WeatherObservation{
			Provider: "open-meteo",
			TempMax:  &negTemp,
			TempMin:  &negTemp,
		}
		result, err := RenderMorningSummary(baseCity, obsNeg)
		require.NoError(t, err)
		// must contain the value without ASCII minus as leading char
		assert.Contains(t, result, "5.2°C")
		// the minus sign itself is the U+2212 minusSign constant
		assert.Contains(t, result, minusSign+"5.2°C")
		assert.NotContains(t, result, "-5.2°C")
	})

	t.Run("bad timezone returns error", func(t *testing.T) {
		t.Parallel()
		badCity := baseCity
		badCity.Timezone = "Galaxy/Nowhere"
		_, err := RenderMorningSummary(badCity, fullObs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Galaxy/Nowhere")
	})
}

func TestRenderWeatherAlert(t *testing.T) {
	t.Parallel()

	baseCity := domain.WeatherUserCity{
		ID:          "WUC01",
		DisplayName: "Almaty",
		Timezone:    "Asia/Almaty",
		UserType:    domain.UserTypeTelegram,
		UserID:      "123",
	}

	tempMax := 38.2
	tempMin := 24.1
	code := 95
	obs := domain.WeatherObservation{
		Provider:    domain.ProviderOpenMeteo,
		LocationID:  "loc1",
		TempMax:     &tempMax,
		TempMin:     &tempMin,
		WeatherCode: &code,
	}

	t.Run("heat alert renders emoji header and reason", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertHeat
		result, err := RenderWeatherAlert(city, "High +38.2°C ≥ +35.0°C", obs)
		require.NoError(t, err)
		assert.Contains(t, result, "🔥")
		assert.Contains(t, result, "Heat alert")
		assert.Contains(t, result, "Almaty")
		assert.Contains(t, result, "+38.2°C ≥ +35.0°C")
		assert.Contains(t, result, "+24.1")
	})

	t.Run("frost alert renders emoji header and reason with minus sign", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertFrost
		negMin := -3.5
		negObs := obs
		negObs.TempMin = &negMin
		result, err := RenderWeatherAlert(city, "Low −3.5°C ≤ +0.0°C", negObs)
		require.NoError(t, err)
		assert.Contains(t, result, "❄️")
		assert.Contains(t, result, "Frost alert")
		assert.Contains(t, result, "Almaty")
		assert.Contains(t, result, "−3.5°C")
	})

	t.Run("thunderstorm alert renders emoji header and WMO reason", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertThunderstorm
		result, err := RenderWeatherAlert(city, "Thunderstorm", obs)
		require.NoError(t, err)
		assert.Contains(t, result, "⛈️")
		assert.Contains(t, result, "Thunderstorm alert")
		assert.Contains(t, result, "Almaty")
		assert.Contains(t, result, "Thunderstorm")
	})

	t.Run("nil TempMax renders dash in snapshot line", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertHeat
		noMax := obs
		noMax.TempMax = nil
		result, err := RenderWeatherAlert(city, "reason", noMax)
		require.NoError(t, err)
		// temperature snapshot: max is "—" not zero
		assert.Contains(t, result, "—")
		assert.NotContains(t, result, "+0.0°C")
	})

	t.Run("nil TempMin renders dash in snapshot line", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertFrost
		noMin := obs
		noMin.TempMin = nil
		result, err := RenderWeatherAlert(city, "reason", noMin)
		require.NoError(t, err)
		assert.Contains(t, result, "—")
	})

	t.Run("nil WeatherCode omits condition from snapshot", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertHeat
		noCode := obs
		noCode.WeatherCode = nil
		result, err := RenderWeatherAlert(city, "reason", noCode)
		require.NoError(t, err)
		// no WMO emoji in the snapshot line
		assert.NotContains(t, result, "⛈️")
		assert.Contains(t, result, "🌡") // temperature line still present
	})

	t.Run("city name is HTML-escaped", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertHeat
		city.DisplayName = "<script>xss</script>"
		result, err := RenderWeatherAlert(city, "reason", obs)
		require.NoError(t, err)
		assert.NotContains(t, result, "<script>")
		assert.Contains(t, result, "&lt;script&gt;")
	})

	t.Run("morning_summary kind returns error", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyMorningSummary
		_, err := RenderWeatherAlert(city, "reason", obs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unrecognised alert kind")
	})

	t.Run("rain alert renders emoji header and reason", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertRain
		result, err := RenderWeatherAlert(city, "Rain likely (82%) within 6h", obs)
		require.NoError(t, err)
		assert.Contains(t, result, "🌧️")
		assert.Contains(t, result, "Rain alert")
		assert.Contains(t, result, "Almaty")
		assert.Contains(t, result, "Rain likely (82%) within 6h")
		assert.Contains(t, result, "+38.2°C")
	})

	t.Run("thaw alert renders emoji header and reason", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertThaw
		negMin := -3.0
		thawObs := obs
		thawObs.TempMin = &negMin
		result, err := RenderWeatherAlert(city, "Thaw: −3.0°C → +2.0°C", thawObs)
		require.NoError(t, err)
		assert.Contains(t, result, "🫠")
		assert.Contains(t, result, "Thaw alert")
		assert.Contains(t, result, "Almaty")
		assert.Contains(t, result, "Thaw: −3.0°C → +2.0°C")
	})

	t.Run("unknown kind returns error", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = "completely_unknown_kind"
		_, err := RenderWeatherAlert(city, "reason", obs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unrecognised alert kind")
	})
}
