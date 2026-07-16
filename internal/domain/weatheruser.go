package domain

import (
	"fmt"
	"strconv"
	"time"
)

// WeatherNotifyKind is a string enum identifying the delivery trigger for a weather subscription.
// Values are persisted to the database; do not rename existing constants without a data migration.
type WeatherNotifyKind string

const (
	// WeatherNotifyMorningSummary delivers a daily morning forecast summary
	// for the subscribed city, evaluated in the city's local timezone.
	WeatherNotifyMorningSummary WeatherNotifyKind = "morning_summary"

	// WeatherNotifyAlertHeat fires when the daily forecast high (TempMax) meets or
	// exceeds the configured threshold (ConditionValue, °C). Evaluated against the
	// Open-Meteo daily observation.
	WeatherNotifyAlertHeat WeatherNotifyKind = "alert_heat"

	// WeatherNotifyAlertFrost fires when the daily forecast low (TempMin) meets or
	// falls below the configured threshold (ConditionValue, °C).
	WeatherNotifyAlertFrost WeatherNotifyKind = "alert_frost"

	// WeatherNotifyAlertThunderstorm fires when the daily-dominant WMO weather code
	// is in the thunderstorm band (95, 96, or 99). ConditionValue is empty — no
	// numeric threshold applies. "Today is forecast stormy" semantics, not "storm
	// at this instant."
	WeatherNotifyAlertThunderstorm WeatherNotifyKind = "alert_thunderstorm"

	// WeatherNotifyAlertRain fires when the maximum precipitation probability within
	// the fixed look-ahead window (weatherRainWindow = 6 h) meets or exceeds the
	// configured threshold. ConditionValue stores the probability threshold as a
	// decimal percent string (e.g. "70"). The window is a fixed constant, not
	// user-configurable; a per-user window would encode as "70@6h" — that is why
	// ConditionValue is TEXT rather than a single REAL column. Evaluated against
	// Open-Meteo hourly data.
	WeatherNotifyAlertRain WeatherNotifyKind = "rain_alert"

	// WeatherNotifyAlertThaw fires when the forecast day itself crosses zero:
	// TempMin ≤ 0 °C and TempMax > 0 °C ("froze overnight, thawed during the
	// day"). ConditionValue is empty — no numeric threshold applies, same as
	// alert_thunderstorm.
	WeatherNotifyAlertThaw WeatherNotifyKind = "alert_thaw"
)

// alertMinusSign is the U+2212 MINUS SIGN used in alert reason strings to format
// negative temperatures, matching the notification package's visual style.
const alertMinusSign = "−"

// weatherRainWindow is the fixed look-ahead window for the rain alert. Any hourly
// precipitation_probability point in [now, now+weatherRainWindow) at or above the
// configured threshold fires the alert. A per-user window is deferred; if needed
// later, switch condition_value to a compound "70@6h" encoding — that is exactly
// why the column is TEXT, not a single REAL threshold.
const weatherRainWindow = 6 * time.Hour

// WeatherUserCity records a user's per-city weather subscription.
// NotifyHour is the local-time hour (0–23) at which the daily summary fires, in Timezone.
// ConditionValue holds the alert threshold for heat/frost kinds (a decimal number in °C),
// and is empty for morning_summary, thunderstorm, and thaw (which need no numeric bound).
//
// LastNotifiedAt has a dual meaning depending on NotifyKind, because a row is exactly one
// kind and the two meanings never coexist: for morning_summary it is the wall-clock time of
// the last summary (the IsMorningDue cursor), zero when no summary has ever been sent; for
// alert kinds it is the forecast_date of the last fire, stored as that local date's
// UTC-midnight instant (see ForecastDateKey), used by the per-forecast_date fire cap.
//
// AlertLatched is the system-managed edge-trigger latch for alert kinds: true means an
// alert already fired and the condition has not yet cleared, so the row will not fire again
// until it re-arms. Never set from user input; ignored for morning_summary.
type WeatherUserCity struct {
	ID             string
	UserType       UserType
	UserID         string
	LocationID     string
	DisplayName    string
	Latitude       float64
	Longitude      float64
	Timezone       string // IANA timezone name, e.g. "Asia/Almaty"
	Country        string
	Admin1         string
	NotifyKind     WeatherNotifyKind
	NotifyHour     int // local 0–23
	ConditionValue string
	LastNotifiedAt time.Time
	AlertLatched   bool
	UpdatedAt      time.Time
	CreatedAt      time.Time
}

// ForecastDateKey parses a YYYY-MM-DD forecast_date (WeatherObservation.ForecastDate)
// into a stable UTC-midnight instant used as the per-forecast_date fire cursor stored in
// WeatherUserCity.LastNotifiedAt for alert kinds. It returns an error for a malformed date.
// Both the stored cursor and the live comparison run through this one parse, so equality is
// well-defined regardless of the forecast_date's local timezone — the key is a stable
// token, not a timezone-correct instant, and that is intentional and sufficient for date
// equality.
func ForecastDateKey(forecastDate string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", forecastDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse forecast_date %q: %w", forecastDate, err)
	}
	return t, nil
}

// alertCondition is the tri-state result of evaluating an alert kind's fire predicate
// against an observation. It exists so that "the condition is not met" (evaluable, clear)
// and "the condition cannot be evaluated" (a required obs field is missing) are
// distinguishable — collapsing them would let a data gap masquerade as a genuinely
// cleared condition and wrongly re-arm a latch.
type alertCondition int

const (
	alertConditionUnevaluable alertCondition = iota // a required obs field is missing
	alertConditionNotMet                            // evaluable, fire predicate not satisfied
	alertConditionMet                               // evaluable, fire predicate satisfied
)

// Validate reports whether ConditionValue is consistent with NotifyKind.
// Returns a non-nil error with a human-readable message on mismatch so the
// caller can surface it as a user-facing validation failure.
// morning_summary ignores ConditionValue; thunderstorm accepts any value (empty
// is canonical); heat and frost require a parseable float64 in [-100, 100] °C;
// rain requires a parseable float64 in (0, 100].
func (c *WeatherUserCity) Validate() error {
	switch c.NotifyKind {
	case WeatherNotifyMorningSummary:
		return nil
	case WeatherNotifyAlertHeat, WeatherNotifyAlertFrost:
		v, err := strconv.ParseFloat(c.ConditionValue, 64)
		if err != nil {
			return fmt.Errorf("condition_value must be a valid number for %s", string(c.NotifyKind))
		}
		if v < -100 || v > 100 {
			return fmt.Errorf("condition_value for %s must be a temperature in °C in [-100, 100]", string(c.NotifyKind))
		}
		return nil
	case WeatherNotifyAlertThunderstorm, WeatherNotifyAlertThaw:
		return nil
	case WeatherNotifyAlertRain:
		v, err := strconv.ParseFloat(c.ConditionValue, 64)
		if err != nil {
			return fmt.Errorf("condition_value must be a valid number for %s", string(c.NotifyKind))
		}
		if v <= 0 || v > 100 {
			return fmt.Errorf("condition_value for %s must be a probability percent in (0, 100]", string(c.NotifyKind))
		}
		return nil
	default:
		return fmt.Errorf("unknown notify_kind: %s", string(c.NotifyKind))
	}
}

// AlertThreshold parses ConditionValue as a float64 threshold for alert_heat,
// alert_frost, and rain_alert. Returns an error for kinds that take no numeric
// threshold (thunderstorm, morning_summary, unknown).
func (c *WeatherUserCity) AlertThreshold() (float64, error) {
	switch c.NotifyKind {
	case WeatherNotifyAlertHeat, WeatherNotifyAlertFrost, WeatherNotifyAlertRain:
		v, err := strconv.ParseFloat(c.ConditionValue, 64)
		if err != nil {
			return 0, fmt.Errorf("weather city %s: parse condition_value %q as threshold: %w", c.ID, c.ConditionValue, err)
		}
		return v, nil
	default:
		return 0, fmt.Errorf("weather city %s: %q does not have a numeric threshold", c.ID, c.NotifyKind)
	}
}

// EvaluateAlert reports whether this city's alert condition is currently met by obs
// and a short human reason string. The metric and comparison are implied by NotifyKind:
//
//   - alert_heat:  obs.TempMax  ≥ threshold (forecast daily high, °C, Open-Meteo)
//   - alert_frost: obs.TempMin  ≤ threshold (forecast daily low,  °C, Open-Meteo)
//   - alert_thunderstorm: obs.WeatherCode ≥ 95 (WMO thunderstorm band; "today is
//     forecast stormy," not instantaneous — the daily-dominant code is used)
//   - alert_thaw: obs.TempMax > 0 °C (warm-side only, like alert_heat at an implicit
//     threshold of 0; both TempMin and TempMax must be present to be evaluable, but
//     TempMin no longer gates the comparison — see evaluateAlertCondition)
//   - rain_alert: maximum precipitation probability in the fixed look-ahead window,
//     with now as the window anchor.
//
// now is used only for the rain_alert case to define the look-ahead window; it is
// ignored for all other kinds. Pass time.Now().UTC() in production; pass a fixed
// time in tests for deterministic rain evaluation.
//
// A nil required field means the condition cannot be evaluated: fired=false, err=nil.
// Anti-spam is the caller's responsibility, not this pure evaluator — see EvaluateLatched
// for the stateful edge-trigger decorator. morning_summary and unknown kinds return an
// error; they are not alert kinds.
func (c *WeatherUserCity) EvaluateAlert(obs WeatherObservation, now time.Time) (fired bool, reason string, err error) {
	cond, reason, err := c.evaluateAlertCondition(obs, now)
	return cond == alertConditionMet, reason, err
}

// EvaluateLatched applies edge-trigger semantics on top of the alert condition: fire is
// true only on the transition into the condition (met && !prevLatched); nextLatched tracks
// the condition, latched while met and re-armed once it clears. An unevaluable observation
// (a data gap such as a nil TempMin/TempMax or an empty rain window) leaves the latch
// unchanged and never fires — a data gap must not be mistaken for "condition cleared," or a
// latched alert would spuriously re-arm and re-fire once data returns. On error the
// previous latch is preserved. reason is non-empty only when fire is true.
func (c *WeatherUserCity) EvaluateLatched(obs WeatherObservation, now time.Time, prevLatched bool) (fire, nextLatched bool, reason string, err error) {
	cond, reason, err := c.evaluateAlertCondition(obs, now)
	if err != nil {
		return false, prevLatched, "", err
	}
	switch cond {
	case alertConditionUnevaluable:
		return false, prevLatched, "", nil // data gap: no state change, no fire
	case alertConditionNotMet:
		return false, false, "", nil // condition cleared: re-arm
	default: // alertConditionMet
		return !prevLatched, true, reason, nil
	}
}

// evaluateAlertCondition is the single tri-state evaluator every alert kind routes
// through, so "condition false" (alertConditionNotMet) and "cannot evaluate"
// (alertConditionUnevaluable, a required obs field is missing) stay distinguishable.
// Both EvaluateAlert and EvaluateLatched delegate here so there is one source of truth
// for the per-kind fire predicate.
func (c *WeatherUserCity) evaluateAlertCondition(obs WeatherObservation, now time.Time) (alertCondition, string, error) {
	switch c.NotifyKind {
	case WeatherNotifyAlertHeat:
		t, err := c.AlertThreshold()
		if err != nil {
			return alertConditionUnevaluable, "", err
		}
		if obs.TempMax == nil {
			return alertConditionUnevaluable, "", nil
		}
		if *obs.TempMax < t {
			return alertConditionNotMet, "", nil
		}
		return alertConditionMet, fmt.Sprintf("High %s ≥ %s", formatAlertTemp(*obs.TempMax), formatAlertTemp(t)), nil
	case WeatherNotifyAlertFrost:
		t, err := c.AlertThreshold()
		if err != nil {
			return alertConditionUnevaluable, "", err
		}
		if obs.TempMin == nil {
			return alertConditionUnevaluable, "", nil
		}
		if *obs.TempMin > t {
			return alertConditionNotMet, "", nil
		}
		return alertConditionMet, fmt.Sprintf("Low %s ≤ %s", formatAlertTemp(*obs.TempMin), formatAlertTemp(t)), nil
	case WeatherNotifyAlertThunderstorm:
		if obs.WeatherCode == nil {
			return alertConditionUnevaluable, "", nil
		}
		if *obs.WeatherCode < 95 {
			return alertConditionNotMet, "", nil
		}
		text, _ := WMOWeatherCode(*obs.WeatherCode)
		return alertConditionMet, text, nil
	case WeatherNotifyAlertThaw:
		if obs.TempMin == nil || obs.TempMax == nil {
			return alertConditionUnevaluable, "", nil // cannot evaluate without both bounds present
		}
		if *obs.TempMax <= 0 {
			return alertConditionNotMet, "", nil // the day never rose above freezing
		}
		// Thaw is keyed purely on TempMax (the warm-side metric), matching alert_heat's
		// warm-side comparison at an implicit threshold of 0 — see the "thaw vs heat
		// overlap" note in plans/262-weather-alert-edge-trigger-hysteresis.md. TempMin is
		// still required to be present (evaluability), but no longer gates met/notMet:
		// the latch model keys cold-side kinds (frost) on TempMin and warm-side kinds
		// (heat, thaw) on TempMax exclusively, so re-arming thaw is TempMax ≤ 0 alone.
		return alertConditionMet, fmt.Sprintf("Thaw: %s → %s",
			formatAlertTemp(*obs.TempMin), formatAlertTemp(*obs.TempMax)), nil
	case WeatherNotifyAlertRain:
		threshold, err := c.AlertThreshold()
		if err != nil {
			return alertConditionUnevaluable, "", err
		}
		cond, reason := evaluateRainCondition(obs, now, threshold)
		return cond, reason, nil
	default:
		return alertConditionUnevaluable, "", fmt.Errorf("weather city %s: not an alert kind: %q", c.ID, c.NotifyKind)
	}
}

// EvaluateRain scans obs.Hourly for hourly points in [now, now+weatherRainWindow)
// and fires when the maximum precipitation probability in that window meets or
// exceeds the configured threshold (ConditionValue percent). Returns fired=false
// without error when:
//   - obs.Hourly is empty or nil (no hourly data yet — the alert fires once data arrives)
//   - no hourly points fall within the window
//   - the max probability in the window is below the threshold
//
// Points with nil PrecipProb are skipped. Anti-spam is the caller's concern (see
// EvaluateLatched for the stateful edge-trigger decorator).
//
// Use this method directly in tests to supply a deterministic now; EvaluateAlert
// dispatches here (via evaluateAlertCondition) with time.Now().UTC() as the window
// anchor for production use.
func (c *WeatherUserCity) EvaluateRain(obs WeatherObservation, now time.Time) (bool, string, error) {
	threshold, err := c.AlertThreshold()
	if err != nil {
		return false, "", err
	}
	cond, reason := evaluateRainCondition(obs, now, threshold)
	return cond == alertConditionMet, reason, nil
}

// evaluateRainCondition is the tri-state rain evaluator shared by EvaluateRain and
// evaluateAlertCondition's rain_alert case (one source of truth for the window scan).
// alertConditionUnevaluable means no hourly point fell in the look-ahead window (empty
// Hourly, all points in the past, too far ahead, or all nil PrecipProb) — a data gap, not
// a cleared condition.
func evaluateRainCondition(obs WeatherObservation, now time.Time, threshold float64) (alertCondition, string) {
	windowEnd := now.Add(weatherRainWindow)
	maxProb := -1
	for _, h := range obs.Hourly {
		if h.Time.Before(now) || !h.Time.Before(windowEnd) {
			continue
		}
		if h.PrecipProb == nil {
			continue
		}
		if *h.PrecipProb > maxProb {
			maxProb = *h.PrecipProb
		}
	}
	if maxProb < 0 {
		return alertConditionUnevaluable, ""
	}
	if float64(maxProb) < threshold {
		return alertConditionNotMet, ""
	}
	return alertConditionMet, fmt.Sprintf("Rain likely (%d%%) within %dh", maxProb, int(weatherRainWindow.Hours()))
}

// formatAlertTemp formats a temperature as "+31.6°C" or "−5.2°C" using the
// U+2212 MINUS SIGN for negative values, matching the notification package style.
func formatAlertTemp(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.1f°C", v)
	}
	return fmt.Sprintf("%s%.1f°C", alertMinusSign, -v)
}

// IsMorningDue reports whether the daily morning summary should fire now,
// evaluated in the city's local timezone. now must be UTC. It fires once per
// local calendar day at NotifyHour. Returns an error if the stored timezone
// is not loadable.
func (c *WeatherUserCity) IsMorningDue(now time.Time) (bool, error) {
	if c.Timezone == "" {
		return false, fmt.Errorf("weather city %s: timezone is empty", c.ID)
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return false, fmt.Errorf("weather city %s: load timezone %q: %w", c.ID, c.Timezone, err)
	}
	local := now.In(loc)
	fire := time.Date(local.Year(), local.Month(), local.Day(), c.NotifyHour, 0, 0, 0, loc)
	if local.Before(fire) {
		return false, nil
	}
	if c.LastNotifiedAt.IsZero() {
		return true, nil
	}
	return c.LastNotifiedAt.In(loc).Before(fire), nil
}
