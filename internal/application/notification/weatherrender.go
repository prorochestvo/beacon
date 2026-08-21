package notification

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// RenderMorningSummary produces a Telegram HTML morning-weather summary for city
// from a single Open-Meteo observation.
//
// All dynamic text (city name, condition descriptions) is HTML-escaped.
// Times in the header and sunrise/sunset are shown in the city's IANA timezone.
// Sunrise/sunset are stored as correct UTC instants by the Open-Meteo decoder and
// converted to city-local time here via obs.Sunrise.In(cityLoc). Nil optional
// fields render as "—", never "0".
//
// Returns an error when the city timezone fails to load.
func RenderMorningSummary(city domain.WeatherUserCity, obs domain.WeatherObservation) (string, error) {
	loc, err := time.LoadLocation(city.Timezone)
	if err != nil {
		return "", fmt.Errorf("weather render: load timezone %q: %w", city.Timezone, err)
	}

	now := time.Now().UTC()
	ts := now.In(loc).Format("Mon 2 Jan, 15:04 -07")
	cityName := html.EscapeString(city.DisplayName)

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b>\n%s", cityName, ts)
	sb.WriteString("\n\n")
	sb.WriteString(renderWeatherBlock(obs, loc))

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
// kind (heat, frost, thunderstorm, rain, thaw). It includes a header keyed on the kind
// AND the latch edge, the city name, the reason string from EvaluateLatched, and a
// one-line forecast snapshot (condition + high/low).
//
// All dynamic text (city name, condition descriptions) is HTML-escaped; the reason
// string from EvaluateLatched may contain ≥/≤/U+2212 which are safe plain-text
// characters. Nil optional fields render as "—", never "0". Returns an error when the
// (kind, edge) pair has no registered header — an unknown kind, a non-notifiable edge,
// or a cleared edge on a kind that only notifies one way (programming error).
func RenderWeatherAlert(city domain.WeatherUserCity, edge domain.AlertEdge, reason string, obs domain.WeatherObservation) (string, error) {
	header, emoji, ok := alertKindHeader(city.NotifyKind, edge)
	if !ok {
		return "", fmt.Errorf("RenderWeatherAlert: unrenderable alert kind %q edge %d for city %s", city.NotifyKind, edge, city.ID)
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

// alertKindHeader returns the human-readable label and display emoji for the given alert
// kind and latch edge. Returns ok=false for non-alert kinds (morning_summary, unknown),
// for AlertEdgeNone (nothing to render), and for AlertEdgeCleared on any kind but
// rain_alert — the daily-metric kinds re-arm silently, so a cleared edge reaching the
// renderer for one of them is a bug, not a message.
func alertKindHeader(kind domain.WeatherNotifyKind, edge domain.AlertEdge) (header, emoji string, ok bool) {
	if kind == domain.WeatherNotifyAlertRain {
		switch edge {
		case domain.AlertEdgeEntered:
			return "Rain alert", "🌧️", true
		case domain.AlertEdgeCleared:
			return "Rain cleared", "🌤️", true
		default:
			return "", "", false
		}
	}

	if edge != domain.AlertEdgeEntered {
		return "", "", false
	}
	switch kind {
	case domain.WeatherNotifyAlertHeat:
		return "Heat alert", "🔥", true
	case domain.WeatherNotifyAlertFrost:
		return "Frost alert", "❄️", true
	case domain.WeatherNotifyAlertThunderstorm:
		return "Thunderstorm alert", "⛈️", true
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

// RenderForecastOutlook produces the Telegram HTML multi-week outlook digest for city.
//
// It lists only the days the outlook considers notable — rain, snow, or a change in the
// freezing regime — because a line per day over two weeks is a wall of text whose signal is
// the three lines that differ. Days whose entry is new or has changed since prevSignature
// carry a marker; when prevSignature names no comparable previous state, nothing is marked,
// since marking every line of a first digest tells the reader nothing.
//
// Returns an error only for a city of the wrong kind, which is a wiring bug rather than a
// message. A forecast date that will not parse degrades to the raw date rather than failing
// the digest: the reader loses a weekday name, not the warning.
func RenderForecastOutlook(city domain.WeatherUserCity, outlook domain.WeatherOutlook, prevSignature string) (string, error) {
	if city.NotifyKind != domain.WeatherNotifyForecastOutlook {
		return "", fmt.Errorf("RenderForecastOutlook: city %s is %q, not %q", city.ID, city.NotifyKind, domain.WeatherNotifyForecastOutlook)
	}

	cityName := html.EscapeString(city.DisplayName)
	notable := outlook.NotableDays()

	var sb strings.Builder
	fmt.Fprintf(&sb, "🗓 <b>Outlook — %s</b>\n", cityName)

	if len(notable) == 0 {
		fmt.Fprintf(&sb, "No rain, snow or freezing change in the next %d days.", outlook.AheadDays())
		return sb.String(), nil
	}

	change := domain.CompareWeatherOutlookSignatures(prevSignature, outlook.Signature())

	fmt.Fprintf(&sb, "Next %d days\n", outlook.AheadDays())
	for _, day := range notable {
		sb.WriteByte('\n')
		if change.Changed[day.ForecastDate] {
			sb.WriteString("🆕 ")
		}
		fmt.Fprintf(&sb, "<b>%s</b>", html.EscapeString(formatForecastDate(day.ForecastDate)))
		if day.IsRainDay() && day.RainSum != nil {
			fmt.Fprintf(&sb, "  🌧 %.1f mm", *day.RainSum)
		}
		if day.IsSnowDay() && day.SnowfallSum != nil {
			fmt.Fprintf(&sb, "  ❄ %.1f cm", *day.SnowfallSum)
		}
		fmt.Fprintf(&sb, "  %s", day.ZeroState().Symbol())
		if day.TempMax != nil && day.TempMin != nil {
			fmt.Fprintf(&sb, " %s / %s", formatWeatherTemp(*day.TempMax), formatWeatherTemp(*day.TempMin))
		}
	}

	if len(change.Cleared) > 0 {
		labels := make([]string, 0, len(change.Cleared))
		for _, date := range change.Cleared {
			labels = append(labels, html.EscapeString(formatForecastDate(date)))
		}
		fmt.Fprintf(&sb, "\n\nCleared: %s", strings.Join(labels, ", "))
	}

	return sb.String(), nil
}

// formatForecastDate turns a YYYY-MM-DD forecast date into "Sun 23 Aug". The date is already
// city-local, so it is parsed as a bare calendar day and never converted; an unparseable one
// is returned verbatim rather than dropped.
func formatForecastDate(forecastDate string) string {
	t, err := time.Parse(time.DateOnly, forecastDate)
	if err != nil {
		return forecastDate
	}
	return t.Format("Mon 2 Jan")
}
