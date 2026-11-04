package notification

import (
	"strings"
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
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "High +38.2°C ≥ +35.0°C", obs)
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
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "Low −3.5°C ≤ +0.0°C", negObs)
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
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "Thunderstorm", obs)
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
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "reason", noMax)
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
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "reason", noMin)
		require.NoError(t, err)
		assert.Contains(t, result, "—")
	})

	t.Run("nil WeatherCode omits condition from snapshot", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertHeat
		noCode := obs
		noCode.WeatherCode = nil
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "reason", noCode)
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
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "reason", obs)
		require.NoError(t, err)
		assert.NotContains(t, result, "<script>")
		assert.Contains(t, result, "&lt;script&gt;")
	})

	t.Run("morning_summary kind returns error", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyMorningSummary
		_, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "reason", obs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unrenderable alert kind")
	})

	t.Run("rain alert renders emoji header and reason", func(t *testing.T) {
		t.Parallel()
		city := baseCity
		city.NotifyKind = domain.WeatherNotifyAlertRain
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "Rain likely (82%) within 6h", obs)
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
		result, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "Thaw: −3.0°C → +2.0°C", thawObs)
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
		_, err := RenderWeatherAlert(city, domain.AlertEdgeEntered, "reason", obs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unrenderable alert kind")
	})
}

// TestRenderWeatherAlertEdges pins the edge-aware header selection: rain_alert renders a
// different message per direction, and every other kind may only ever render the entered
// direction — a cleared edge reaching the renderer for them is a bug, not a message.
func TestRenderWeatherAlertEdges(t *testing.T) {
	t.Parallel()

	tempMax := 18.0
	tempMin := 9.0
	code := 61
	obs := domain.WeatherObservation{
		TempMax:     &tempMax,
		TempMin:     &tempMin,
		WeatherCode: &code,
	}
	rainCity := domain.WeatherUserCity{
		ID: "WUC-RAIN", DisplayName: "Astana",
		NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "60",
	}

	t.Run("rain entered renders the alert header", func(t *testing.T) {
		t.Parallel()
		result, err := RenderWeatherAlert(rainCity, domain.AlertEdgeEntered, "Rain likely (80%) within 6h", obs)
		require.NoError(t, err)
		assert.Contains(t, result, "Rain alert")
		assert.Contains(t, result, "Astana")
		assert.Contains(t, result, "Rain likely (80%) within 6h")
		assert.NotContains(t, result, "Rain cleared")
	})

	t.Run("rain cleared renders a distinct header", func(t *testing.T) {
		t.Parallel()
		result, err := RenderWeatherAlert(rainCity, domain.AlertEdgeCleared, "No rain expected (30%) within 6h", obs)
		require.NoError(t, err)
		assert.Contains(t, result, "Rain cleared")
		assert.Contains(t, result, "Astana")
		assert.Contains(t, result, "No rain expected (30%) within 6h")
		assert.NotContains(t, result, "Rain alert",
			"the two directions must be visually distinguishable, not the same header")
	})

	t.Run("a cleared edge on a daily-metric kind is an error", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []domain.WeatherNotifyKind{
			domain.WeatherNotifyAlertHeat,
			domain.WeatherNotifyAlertFrost,
			domain.WeatherNotifyAlertThunderstorm,
			domain.WeatherNotifyAlertThaw,
		} {
			city := domain.WeatherUserCity{ID: "WUC-X", NotifyKind: kind}
			_, err := RenderWeatherAlert(city, domain.AlertEdgeCleared, "reason", obs)
			require.Errorf(t, err, "%s re-arms silently and must never render a cleared message", kind)
		}
	})

	t.Run("AlertEdgeNone is an error for every kind", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []domain.WeatherNotifyKind{
			domain.WeatherNotifyAlertRain,
			domain.WeatherNotifyAlertHeat,
			domain.WeatherNotifyAlertThaw,
		} {
			city := domain.WeatherUserCity{ID: "WUC-Y", NotifyKind: kind}
			_, err := RenderWeatherAlert(city, domain.AlertEdgeNone, "reason", obs)
			require.Errorf(t, err, "%s has nothing to render without a transition", kind)
		}
	})
}

func TestRenderForecastOutlook(t *testing.T) {
	t.Parallel()

	outlookOf := func(days ...domain.WeatherForecastDay) domain.WeatherOutlook {
		baseline := domain.WeatherForecastDay{ForecastDate: "2026-08-21", TempMax: fptr(21.5), TempMin: fptr(11.2)}
		return domain.NewWeatherOutlook(append([]domain.WeatherForecastDay{baseline}, days...), "2026-08-21")
	}

	city := domain.WeatherUserCity{
		ID:          "o1",
		DisplayName: "Astana",
		Timezone:    "UTC",
		NotifyKind:  domain.WeatherNotifyForecastOutlook,
	}

	t.Run("renders a notable day with its date, amount and temperatures", func(t *testing.T) {
		t.Parallel()
		wet := domain.WeatherForecastDay{ForecastDate: "2026-08-23", TempMax: fptr(22.0), TempMin: fptr(14.6), RainSum: fptr(1.3)}
		msg, err := RenderForecastOutlook(city, outlookOf(wet), "")
		require.NoError(t, err)

		assert.Contains(t, msg, "<b>Outlook — Astana</b>")
		assert.Contains(t, msg, "Sun 23 Aug")
		assert.Contains(t, msg, "🌧 1.3 mm")
		assert.Contains(t, msg, "+22.0°C")
		assert.Contains(t, msg, "▲")
	})

	t.Run("renders snow in centimetres and a freezing day below zero", func(t *testing.T) {
		t.Parallel()
		snowy := domain.WeatherForecastDay{ForecastDate: "2026-08-25", TempMax: fptr(-2.0), TempMin: fptr(-9.0), SnowfallSum: fptr(4.0)}
		msg, err := RenderForecastOutlook(city, outlookOf(snowy), "")
		require.NoError(t, err)

		assert.Contains(t, msg, "❄ 4.0 cm")
		assert.Contains(t, msg, "▼")
		assert.Contains(t, msg, "−9.0°C", "negative temperatures use the Unicode minus sign")
	})

	t.Run("an empty outlook says so instead of listing nothing", func(t *testing.T) {
		t.Parallel()
		msg, err := RenderForecastOutlook(city, outlookOf(), "o1:2026-08-23:R+")
		require.NoError(t, err)
		assert.Contains(t, msg, "No rain, snow or freezing change")
		assert.NotContains(t, msg, "🌧")
	})

	t.Run("only the days that changed are marked", func(t *testing.T) {
		t.Parallel()
		kept := domain.WeatherForecastDay{ForecastDate: "2026-08-23", TempMax: fptr(22.0), TempMin: fptr(14.6), RainSum: fptr(1.3)}
		added := domain.WeatherForecastDay{ForecastDate: "2026-08-27", TempMax: fptr(19.0), TempMin: fptr(9.0), RainSum: fptr(2.0)}
		msg, err := RenderForecastOutlook(city, outlookOf(kept, added), "o1:2026-08-23:R+")
		require.NoError(t, err)

		assert.Equal(t, 1, strings.Count(msg, "🆕"))
		assert.Contains(t, msg, "🆕 <b>Thu 27 Aug</b>")
	})

	t.Run("days that stopped being notable are listed as cleared", func(t *testing.T) {
		t.Parallel()
		kept := domain.WeatherForecastDay{ForecastDate: "2026-08-23", TempMax: fptr(22.0), TempMin: fptr(14.6), RainSum: fptr(1.3)}
		msg, err := RenderForecastOutlook(city, outlookOf(kept), "o1:2026-08-23:R+;2026-08-27:R+")
		require.NoError(t, err)

		assert.Contains(t, msg, "Cleared: Thu 27 Aug")
	})

	t.Run("the city name is HTML-escaped", func(t *testing.T) {
		t.Parallel()
		hostile := city
		hostile.DisplayName = `Astana <script>alert("x")</script>`
		wet := domain.WeatherForecastDay{ForecastDate: "2026-08-23", TempMax: fptr(22.0), TempMin: fptr(14.6), RainSum: fptr(1.3)}

		msg, err := RenderForecastOutlook(hostile, outlookOf(wet), "")
		require.NoError(t, err)
		assert.NotContains(t, msg, "<script>")
		assert.Contains(t, msg, "&lt;script&gt;")
	})

	t.Run("the wrong notify kind is a wiring bug, not a message", func(t *testing.T) {
		t.Parallel()
		wrong := city
		wrong.NotifyKind = domain.WeatherNotifyAlertHeat
		_, err := RenderForecastOutlook(wrong, outlookOf(), "")
		require.Error(t, err)
	})

	t.Run("an unparseable forecast date degrades to the raw date", func(t *testing.T) {
		t.Parallel()
		broken := domain.WeatherForecastDay{ForecastDate: "not-a-date", TempMax: fptr(2.0), TempMin: fptr(-3.0), RainSum: fptr(5.0)}
		out := domain.NewWeatherOutlook([]domain.WeatherForecastDay{broken}, "2026-08-21")
		msg, err := RenderForecastOutlook(city, out, "")
		require.NoError(t, err)
		assert.Contains(t, msg, "not-a-date", "the reader loses a weekday name, not the warning")
	})
}

// fptr returns a pointer to v so table cases can express an absent measurement as nil.
func fptr(v float64) *float64 { return &v }
