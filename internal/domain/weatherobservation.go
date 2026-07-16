package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// ProviderOpenMeteo is the literal provider token stored in WeatherObservation.Provider
// for Open-Meteo forecasts. It is a data token; do not translate it.
const ProviderOpenMeteo = "open-meteo"

// WeatherHourlyPoint holds one hourly forecast slot for a WeatherObservation.
// Time is a UTC instant (parsed from the provider's local-timezone ISO-8601 string via
// time.ParseInLocation so the stored instant is correct regardless of offset). PrecipProb
// is the precipitation probability in percent (0–100); nil when the provider omits it.
// Temp is the temperature in °C; nil when omitted.
type WeatherHourlyPoint struct {
	Time       time.Time
	PrecipProb *int
	Temp       *float64
}

// WeatherObservation is a weather forecast snapshot for a (location, provider, day) triple.
// Nullable forecast fields use pointer types so that a provider that omits a field stores
// NULL rather than a misleading zero — zero temperature is real data, not absence.
// Hourly holds the per-hour forecast from the Open-Meteo hourly block.
type WeatherObservation struct {
	ID           string
	LocationID   string
	Provider     string // ProviderOpenMeteo — a literal data token, never translated
	Latitude     float64
	Longitude    float64
	CapturedAt   time.Time
	ForecastDate string // YYYY-MM-DD in the city-local timezone

	// Daily forecast for ForecastDate. Nullable: not all providers populate every field.
	TempMax       *float64
	TempMin       *float64
	PrecipSum     *float64
	PrecipProbMax *int
	WeatherCode   *int // raw WMO integer; resolve via WMOWeatherCode at render time, not here
	Sunrise       *time.Time
	Sunset        *time.Time

	// Current snapshot at CapturedAt. Nullable for the same reason.
	TempCurrent *float64
	TempFeels   *float64
	Humidity    *int
	WindSpeed   *float64
	WindDir     *int
	Precip      *float64
	CloudCover  *int

	// Hourly is the per-hour forecast block, ordered by ascending time. Use
	// MarshalHourlyJSON / UnmarshalHourlyJSON for the hourly_json column round-trip.
	Hourly []WeatherHourlyPoint
}

// hourlyWire is the compact JSON representation for a single WeatherHourlyPoint stored
// in the hourly_json column. Short field names keep the column small.
type hourlyWire struct {
	Time       string   `json:"t"`
	PrecipProb *int     `json:"p,omitempty"`
	Temp       *float64 `json:"c,omitempty"`
}

// MarshalHourlyJSON serialises the Hourly slice to a compact JSON byte slice for
// the hourly_json column. Returns nil, nil when Hourly is empty (the repository
// stores NULL). The caller must not assume that nil bytes means an error.
func (o *WeatherObservation) MarshalHourlyJSON() ([]byte, error) {
	if len(o.Hourly) == 0 {
		return nil, nil
	}
	wire := make([]hourlyWire, len(o.Hourly))
	for i, h := range o.Hourly {
		wire[i] = hourlyWire{
			Time:       h.Time.UTC().Format(time.RFC3339),
			PrecipProb: h.PrecipProb,
			Temp:       h.Temp,
		}
	}
	return json.Marshal(wire)
}

// UnmarshalHourlyJSON deserialises the hourly_json column bytes into the Hourly
// field. A nil or empty input is treated as an empty (non-nil) slice — callers can
// check len(obs.Hourly) without a nil guard. Returns an error if the JSON is
// malformed or any time string fails to parse as RFC 3339.
func (o *WeatherObservation) UnmarshalHourlyJSON(data []byte) error {
	if len(data) == 0 {
		o.Hourly = []WeatherHourlyPoint{}
		return nil
	}
	var wire []hourlyWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("weather observation: unmarshal hourly_json: %w", err)
	}
	o.Hourly = make([]WeatherHourlyPoint, 0, len(wire))
	for _, w := range wire {
		t, err := time.Parse(time.RFC3339, w.Time)
		if err != nil {
			return fmt.Errorf("weather observation: parse hourly time %q: %w", w.Time, err)
		}
		o.Hourly = append(o.Hourly, WeatherHourlyPoint{
			Time:       t.UTC(),
			PrecipProb: w.PrecipProb,
			Temp:       w.Temp,
		})
	}
	return nil
}

// wmoEntry pairs a human-readable description with a display emoji.
type wmoEntry struct {
	text  string
	emoji string
}

// wmoTable maps WMO Weather Interpretation Codes to descriptions and emojis.
// Declared at package level so the map is allocated once, not on every WMOWeatherCode call.
var wmoTable = map[int]wmoEntry{
	0:  {"Clear sky", "☀️"},
	1:  {"Mainly clear", "🌤️"},
	2:  {"Partly cloudy", "⛅"},
	3:  {"Overcast", "☁️"},
	45: {"Foggy", "🌫️"},
	48: {"Depositing rime fog", "🌫️"},
	51: {"Light drizzle", "🌦️"},
	53: {"Moderate drizzle", "🌦️"},
	55: {"Dense drizzle", "🌧️"},
	56: {"Light freezing drizzle", "🌨️"},
	57: {"Heavy freezing drizzle", "🌨️"},
	61: {"Slight rain", "🌧️"},
	63: {"Moderate rain", "🌧️"},
	65: {"Heavy rain", "🌧️"},
	66: {"Light freezing rain", "🌨️"},
	67: {"Heavy freezing rain", "🌨️"},
	71: {"Slight snowfall", "❄️"},
	73: {"Moderate snowfall", "❄️"},
	75: {"Heavy snowfall", "❄️"},
	77: {"Snow grains", "🌨️"},
	80: {"Slight rain showers", "🌦️"},
	81: {"Moderate rain showers", "🌦️"},
	82: {"Violent rain showers", "⛈️"},
	85: {"Slight snow showers", "🌨️"},
	86: {"Heavy snow showers", "🌨️"},
	95: {"Thunderstorm", "⛈️"},
	96: {"Thunderstorm with slight hail", "⛈️"},
	99: {"Thunderstorm with heavy hail", "⛈️"},
}

// WMOWeatherCode returns a human-readable description and display emoji for the
// given WMO Weather Interpretation Code. For unrecognised codes it returns
// ("Unknown", "❓") so callers can always render something safe rather than an
// empty string.
func WMOWeatherCode(code int) (text string, emoji string) {
	if e, ok := wmoTable[code]; ok {
		return e.text, e.emoji
	}
	return "Unknown", "❓"
}
