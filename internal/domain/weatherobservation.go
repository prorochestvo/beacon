package domain

import "time"

const (
	// ProviderOpenMeteo is the literal provider token stored in WeatherObservation.Provider
	// for Open-Meteo forecasts. It is a data token; do not translate it.
	ProviderOpenMeteo = "open-meteo"
	// ProviderGismeteo is the literal provider token for Gismeteo forecasts.
	// It is a data token; do not translate it.
	ProviderGismeteo = "gismeteo"
)

// WeatherObservation is a weather forecast snapshot for a (location, provider, day) triple.
// Nullable forecast fields use pointer types so that a provider that omits a field stores
// NULL rather than a misleading zero — zero temperature is real data, not absence.
type WeatherObservation struct {
	ID           string
	LocationID   string
	Provider     string // ProviderOpenMeteo | ProviderGismeteo — literal data tokens, never translated
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
