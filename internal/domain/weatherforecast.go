package domain

import (
	"slices"
	"strings"
	"time"
)

const (
	// WeatherRainDayThresholdMM is the daily rain total, in millimetres, at which a day
	// counts as a rain day. Open-Meteo reports rain_sum in millimetres. The bar sits at
	// 1 mm rather than at any trace above zero because models smear small amounts across
	// most days of a long-range run: at rain_sum > 0 almost every day of a 16-day window
	// comes back wet, which tells the reader nothing.
	WeatherRainDayThresholdMM = 1.0

	// WeatherSnowDayThresholdCM is the daily snowfall total, in CENTIMETRES, at which a
	// day counts as a snow day. Open-Meteo reports snowfall_sum in centimetres while
	// rain_sum is in millimetres, so the two bars are numerically equal and dimensionally
	// different — they must never be collapsed into one constant.
	WeatherSnowDayThresholdCM = 1.0

	// WeatherOutlookHorizonDays is the number of daily rows one long-range fetch covers,
	// counting the current local day. 16 is the documented maximum of the Open-Meteo
	// forecast endpoint (forecast_days), and every variable this feature needs exists over
	// the whole of it.
	WeatherOutlookHorizonDays = 16
)

// WeatherForecastDay is a single day of a long-range forecast for one location, as stored
// in weather_forecast_days. ForecastDate is the city-local calendar day (YYYY-MM-DD) the
// provider labelled the row with; it is stored verbatim and never re-derived from an
// instant. Every measurement is a pointer for the same reason WeatherObservation's are: a
// provider that omits a value must not read back as a zero, since zero is real data here.
type WeatherForecastDay struct {
	ID           string
	LocationID   string
	Provider     string // ProviderOpenMeteo — a literal data token, never translated
	ForecastDate string // YYYY-MM-DD in the city-local timezone
	CapturedAt   time.Time

	TempMax       *float64 // °C
	TempMin       *float64 // °C
	RainSum       *float64 // millimetres
	SnowfallSum   *float64 // centimetres
	PrecipSum     *float64 // millimetres, rain and snow-water combined
	PrecipProbMax *int     // percent, 0–100
	WeatherCode   *int     // raw WMO integer; resolve via WMOWeatherCode at render time
}

// IsRainDay reports whether the day's rain total reaches WeatherRainDayThresholdMM.
// A nil RainSum is not a rain day: an absent measurement is not evidence of rain.
func (d WeatherForecastDay) IsRainDay() bool {
	return d.RainSum != nil && *d.RainSum >= WeatherRainDayThresholdMM
}

// IsSnowDay reports whether the day's snowfall total reaches WeatherSnowDayThresholdCM.
// A nil SnowfallSum is not a snow day, for the same reason as IsRainDay.
func (d WeatherForecastDay) IsSnowDay() bool {
	return d.SnowfallSum != nil && *d.SnowfallSum >= WeatherSnowDayThresholdCM
}

// ZeroState classifies the day against the freezing point. It reports
// WeatherZeroStateUnknown unless both bounds are present — a day with only one of them
// cannot be placed, and guessing would put a wrong badge on the screen with no way for the
// reader to tell.
func (d WeatherForecastDay) ZeroState() WeatherZeroState {
	if d.TempMin == nil || d.TempMax == nil {
		return WeatherZeroStateUnknown
	}
	switch {
	case *d.TempMin > 0:
		return WeatherZeroStateAbove
	case *d.TempMax <= 0:
		return WeatherZeroStateBelow
	default:
		return WeatherZeroStateCrossing
	}
}

// WeatherZeroState is a forecast day's position relative to the freezing point. It is
// three-valued rather than the "above or below" a thermometer reading suggests, because a
// day that starts below zero and ends above it is neither, and is the one that puts ice on
// the ground.
type WeatherZeroState uint8

const (
	// WeatherZeroStateUnknown means the day carries no usable pair of temperature bounds.
	WeatherZeroStateUnknown WeatherZeroState = iota
	// WeatherZeroStateAbove means the day never reached freezing (TempMin > 0 °C).
	WeatherZeroStateAbove
	// WeatherZeroStateCrossing means the day spans the freezing point (TempMin ≤ 0 < TempMax).
	WeatherZeroStateCrossing
	// WeatherZeroStateBelow means the day never rose above freezing (TempMax ≤ 0 °C).
	WeatherZeroStateBelow
)

// Label returns the wire token for the state, as carried by the JSON API. The empty string
// stands for WeatherZeroStateUnknown so an absent classification is omitted rather than
// rendered as a fourth category.
func (s WeatherZeroState) Label() string {
	switch s {
	case WeatherZeroStateAbove:
		return "above"
	case WeatherZeroStateCrossing:
		return "crossing"
	case WeatherZeroStateBelow:
		return "below"
	case WeatherZeroStateUnknown:
		return ""
	default:
		return ""
	}
}

// Symbol returns the single-character indicator used in rendered output.
func (s WeatherZeroState) Symbol() string {
	switch s {
	case WeatherZeroStateAbove:
		return "▲"
	case WeatherZeroStateCrossing:
		return "↕"
	case WeatherZeroStateBelow:
		return "▼"
	case WeatherZeroStateUnknown:
		return "?"
	default:
		return "?"
	}
}

// signatureToken returns the state's one-byte encoding inside an outlook signature. It is
// deliberately not Symbol: a signature is compared byte-for-byte across releases, so it
// uses ASCII that no font substitution or normalisation can touch.
func (s WeatherZeroState) signatureToken() string {
	switch s {
	case WeatherZeroStateAbove:
		return "+"
	case WeatherZeroStateCrossing:
		return "~"
	case WeatherZeroStateBelow:
		return "-"
	case WeatherZeroStateUnknown:
		return "?"
	default:
		return "?"
	}
}

// WeatherOutlook is one location's long-range forecast, reduced to the days worth telling
// a user about. Construct it with NewWeatherOutlook.
type WeatherOutlook struct {
	days     []WeatherForecastDay
	baseline string
}

// NewWeatherOutlook builds an outlook over days for a reader standing on the local
// calendar day baseline (YYYY-MM-DD). days may arrive in any order and may hold dates
// outside the window; the constructor sorts a copy ascending and drops anything before
// baseline.
//
// The baseline day itself is kept but never reported. It is the anchor the first reported
// day's zero-state transition is measured against, and today is already covered by the
// morning summary and the same-day alerts — repeating it in a multi-week outlook would be
// the one line of the message the reader already knows.
func NewWeatherOutlook(days []WeatherForecastDay, baseline string) WeatherOutlook {
	window := make([]WeatherForecastDay, 0, len(days))
	for _, d := range days {
		if d.ForecastDate >= baseline {
			window = append(window, d)
		}
	}
	// Lexicographic order is chronological for YYYY-MM-DD, and the transition scan below
	// depends on it. The repository already returns them ordered; sorting here means the
	// invariant belongs to the type rather than to the caller that happened to satisfy it.
	slices.SortFunc(window, func(a, b WeatherForecastDay) int {
		return strings.Compare(a.ForecastDate, b.ForecastDate)
	})
	return WeatherOutlook{days: window, baseline: baseline}
}

// Days returns the whole window ascending by date, baseline day included. This is the
// view's input; NotableDays is the notification's.
func (o WeatherOutlook) Days() []WeatherForecastDay {
	return o.days
}

// NotableDays returns the days after the baseline that are worth reporting: a rain day, a
// snow day, or a day whose zero-state differs from the last classified day before it.
//
// The third clause is what keeps a continental winter from reporting "below zero" on every
// one of fifteen days. What a reader needs from the temperature axis is the day the regime
// changes; that it is cold in February is not news. A day with unknown bounds neither
// reports a transition nor resets the comparison, so a single gap between two below-zero
// days does not manufacture one.
func (o WeatherOutlook) NotableDays() []WeatherForecastDay {
	notable := make([]WeatherForecastDay, 0, len(o.days))
	prev := WeatherZeroStateUnknown
	for _, d := range o.days {
		state := d.ZeroState()
		transition := state != WeatherZeroStateUnknown && prev != WeatherZeroStateUnknown && state != prev
		if state != WeatherZeroStateUnknown {
			prev = state
		}
		if d.ForecastDate <= o.baseline {
			continue
		}
		if d.IsRainDay() || d.IsSnowDay() || transition {
			notable = append(notable, d)
		}
	}
	return notable
}

// Signature returns a compact, deterministic encoding of the notable days, used as the
// content gate for the daily outlook digest: the digest is sent when this differs from
// what was last sent and stays silent when it does not.
//
// The weatherOutlookSignatureVersion prefix means a future change to the encoding
// re-notifies every subscriber exactly once instead of diffing two encodings that do not
// mean the same thing. An outlook with nothing notable in it encodes as the prefix alone —
// non-empty, and therefore distinguishable from the empty string, which means "never
// evaluated".
func (o WeatherOutlook) Signature() string {
	var b strings.Builder
	b.WriteString(weatherOutlookSignatureVersion)
	b.WriteByte(':')
	for i, d := range o.NotableDays() {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(d.ForecastDate)
		b.WriteByte(':')
		if d.IsRainDay() {
			b.WriteByte('R')
		}
		if d.IsSnowDay() {
			b.WriteByte('S')
		}
		b.WriteString(d.ZeroState().signatureToken())
	}
	return b.String()
}

// WeatherOutlookChange is how one outlook signature differs from an earlier one.
// Changed holds the forecast dates that are newly notable or notable for a different
// reason; Cleared holds the dates that were notable before and no longer are, ascending.
type WeatherOutlookChange struct {
	Changed map[string]bool
	Cleared []string
}

// CompareWeatherOutlookSignatures reports how next differs from prev.
//
// An empty prev, or one written by a different signature version, yields no Changed and no
// Cleared entries: there is nothing meaningful to diff against, and marking every line of
// a first digest as new would make the marker useless on the message where it matters
// least.
func CompareWeatherOutlookSignatures(prev, next string) WeatherOutlookChange {
	change := WeatherOutlookChange{Changed: map[string]bool{}}
	before, ok := parseWeatherOutlookSignature(prev)
	if !ok {
		return change
	}
	after, ok := parseWeatherOutlookSignature(next)
	if !ok {
		return change
	}
	for date, flags := range after {
		if before[date] != flags {
			change.Changed[date] = true
		}
	}
	for date := range before {
		if _, still := after[date]; !still {
			change.Cleared = append(change.Cleared, date)
		}
	}
	slices.Sort(change.Cleared)
	return change
}

// weatherOutlookSignatureVersion prefixes every outlook signature. Bump it whenever the
// encoding below changes meaning: the mismatch is what forces one clean re-notification
// instead of a silently wrong diff.
const weatherOutlookSignatureVersion = "o1"

// parseWeatherOutlookSignature splits a signature into date → flags. ok is false for an
// empty signature or one carrying a different version prefix, which the caller must treat
// as "no comparable previous state" rather than as an empty outlook.
func parseWeatherOutlookSignature(signature string) (map[string]string, bool) {
	body, ok := strings.CutPrefix(signature, weatherOutlookSignatureVersion+":")
	if !ok {
		return nil, false
	}
	entries := map[string]string{}
	if body == "" {
		return entries, true
	}
	for _, part := range strings.Split(body, ";") {
		date, flags, found := strings.Cut(part, ":")
		if !found {
			continue
		}
		entries[date] = flags
	}
	return entries, true
}
