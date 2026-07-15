package notification

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// weatherProviderLabel maps a literal provider data token to a human-readable display label.
// The input is a data token that must never be translated; only the returned label is English prose.
func weatherProviderLabel(provider string) string {
	switch provider {
	case domain.ProviderOpenMeteo:
		return "Open-Meteo"
	case domain.ProviderGismeteo:
		return "Gismeteo"
	default:
		return html.EscapeString(provider)
	}
}

// RenderMorningSummary produces a Telegram HTML morning-weather summary for city,
// incorporating one or more provider observations. The variadic obs signature allows
// the gismeteo increment (Task 13) to pass two observations for a side-by-side
// comparison; the MVP passes a single Open-Meteo observation.
//
// All dynamic text (city name, condition descriptions) is HTML-escaped.
// Times in the header and sunrise/sunset are shown in the city's IANA timezone.
// Sunrise/sunset are stored as correct UTC instants by the Open-Meteo decoder and
// converted to city-local time here via obs.Sunrise.In(cityLoc). Nil optional
// fields render as "—", never "0".
//
// Returns an error when obs is empty or the city timezone fails to load.
func RenderMorningSummary(city domain.WeatherUserCity, obs ...domain.WeatherObservation) (string, error) {
	if len(obs) == 0 {
		return "", fmt.Errorf("RenderMorningSummary: no observations provided for city %s", city.ID)
	}
	loc, err := time.LoadLocation(city.Timezone)
	if err != nil {
		return "", fmt.Errorf("weather render: load timezone %q: %w", city.Timezone, err)
	}

	now := time.Now().UTC()
	ts := now.In(loc).Format("Mon 2 Jan, 15:04 -07")
	cityName := html.EscapeString(city.DisplayName)

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b>\n%s", cityName, ts)

	multiProvider := len(obs) > 1
	for i, o := range obs {
		sb.WriteString("\n\n")
		if multiProvider {
			fmt.Fprintf(&sb, "<b>%s</b>\n", weatherProviderLabel(o.Provider))
		}
		sb.WriteString(renderWeatherBlock(o, loc))
		// between provider sections add an extra blank line
		if multiProvider && i < len(obs)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String(), nil
}

// renderWeatherBlock formats a single observation's daily forecast fields as
// Telegram HTML lines. cityLoc is used to convert sunrise/sunset UTC instants
// to local display times. Nil pointer fields render as "—" to distinguish absent
// values from a real zero (a zero temperature is valid data, not an absence).
func renderWeatherBlock(obs domain.WeatherObservation, cityLoc *time.Location) string {
	var sb strings.Builder

	// Dominant condition: WMO code → text + emoji.
	if obs.WeatherCode != nil {
		text, emoji := domain.WMOWeatherCode(*obs.WeatherCode)
		fmt.Fprintf(&sb, "%s %s\n", emoji, html.EscapeString(text))
	}

	// Temperature high / low.
	maxStr := "—"
	minStr := "—"
	if obs.TempMax != nil {
		maxStr = formatWeatherTemp(*obs.TempMax)
	}
	if obs.TempMin != nil {
		minStr = formatWeatherTemp(*obs.TempMin)
	}

	// Precipitation sum and probability.
	precipStr := "— mm"
	if obs.PrecipSum != nil {
		precipStr = fmt.Sprintf("%.1f mm", *obs.PrecipSum)
	}
	precipProbStr := "—"
	if obs.PrecipProbMax != nil {
		precipProbStr = fmt.Sprintf("%d%%", *obs.PrecipProbMax)
	}
	fmt.Fprintf(&sb, "🌡 %s / %s  •  💧 %s (%s)", maxStr, minStr, precipStr, precipProbStr)

	// Sunrise / sunset: stored as correct UTC instants by the Open-Meteo decoder;
	// convert to city-local time with .In(cityLoc) so the displayed HH:MM is accurate.
	if obs.Sunrise != nil || obs.Sunset != nil {
		sunriseStr := "—"
		sunsetStr := "—"
		if obs.Sunrise != nil {
			sunriseStr = obs.Sunrise.In(cityLoc).Format("15:04")
		}
		if obs.Sunset != nil {
			sunsetStr = obs.Sunset.In(cityLoc).Format("15:04")
		}
		fmt.Fprintf(&sb, "\n🌅 %s  🌇 %s", sunriseStr, sunsetStr)
	}

	return sb.String()
}

// RenderWeatherAlert produces a compact Telegram HTML alert message for an alert
// kind (heat, frost, thunderstorm, rain, thaw). It includes a kind-specific header with
// emoji, the city name, the reason string from EvaluateAlert, and a one-line
// forecast snapshot (condition + high/low).
//
// All dynamic text (city name, condition descriptions) is HTML-escaped; the reason
// string from EvaluateAlert may contain ≥/≤/U+2212 which are safe plain-text
// characters. Nil optional fields render as "—", never "0". Returns an error only
// if called for a kind that has no registered header (programming error).
func RenderWeatherAlert(city domain.WeatherUserCity, reason string, obs domain.WeatherObservation) (string, error) {
	header, emoji, ok := alertKindHeader(city.NotifyKind)
	if !ok {
		return "", fmt.Errorf("RenderWeatherAlert: unrecognised alert kind %q for city %s", city.NotifyKind, city.ID)
	}

	cityName := html.EscapeString(city.DisplayName)
	reasonEscaped := html.EscapeString(reason)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>%s — %s</b>\n", emoji, header, cityName)
	fmt.Fprintf(&sb, "%s\n", reasonEscaped)

	// One-line forecast snapshot: condition + high/low temperature range.
	if obs.WeatherCode != nil {
		text, em := domain.WMOWeatherCode(*obs.WeatherCode)
		fmt.Fprintf(&sb, "%s %s  •  ", em, html.EscapeString(text))
	}
	maxStr := "—"
	minStr := "—"
	if obs.TempMax != nil {
		maxStr = formatWeatherTemp(*obs.TempMax)
	}
	if obs.TempMin != nil {
		minStr = formatWeatherTemp(*obs.TempMin)
	}
	fmt.Fprintf(&sb, "🌡 %s / %s", maxStr, minStr)

	return sb.String(), nil
}

// alertKindHeader returns the human-readable label and display emoji for the
// given alert kind. Returns ok=false for non-alert kinds (morning_summary,
// unknown), which must not be passed to RenderWeatherAlert.
func alertKindHeader(kind domain.WeatherNotifyKind) (header, emoji string, ok bool) {
	switch kind {
	case domain.WeatherNotifyAlertHeat:
		return "Heat alert", "🔥", true
	case domain.WeatherNotifyAlertFrost:
		return "Frost alert", "❄️", true
	case domain.WeatherNotifyAlertThunderstorm:
		return "Thunderstorm alert", "⛈️", true
	case domain.WeatherNotifyAlertRain:
		return "Rain alert", "🌧️", true
	case domain.WeatherNotifyAlertThaw:
		return "Thaw alert", "🫠", true
	default:
		return "", "", false
	}
}

// formatWeatherTemp formats a temperature as "+31.6°C" or "−5.2°C".
// Negative values use the Unicode MINUS SIGN (U+2212, matching minusSign in message.go)
// for visual consistency with the FX alert table.
func formatWeatherTemp(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.1f°C", v)
	}
	// fmt.Sprintf formats the negative float with an ASCII hyphen-minus; replace
	// with the U+2212 MINUS SIGN for visual alignment with the FX table style.
	return fmt.Sprintf("%s%.1f°C", minusSign, -v)
}
