package domain

// WeatherSource is the declarative per-provider configuration row from the
// weather_sources table. Provider is one of ProviderOpenMeteo | ProviderGismeteo
// (literal data tokens, never translated). ThrottleInterval is a Go duration
// string (e.g. "3h"); an empty value means "use the provider's compiled-in
// default". Active carries the runtime on/off toggle (the INTEGER column is
// converted to bool in the repository scan).
type WeatherSource struct {
	Provider         string
	Title            string
	Active           bool
	BaseURL          string
	ThrottleInterval string
	Options          WeatherSourceOptions
}

// WeatherSourceOptions is the parsed options JSON column of a WeatherSource.
// UserAgent overrides the request User-Agent header when non-empty.
type WeatherSourceOptions struct {
	UserAgent string `json:"user_agent,omitempty"`
	// Headers is reserved for schema symmetry with RateSourceOptions and is NOT yet
	// consumed by any weather provider — the gismeteo client applies only UserAgent.
	// Do not rely on it to inject request headers until a provider wires it in.
	Headers map[string]string `json:"headers,omitempty"`
}

// WeatherGismeteoCity is one gismeteo coverage row from the
// weather_gismeteo_cities table. LocationID is the Open-Meteo geocoding id
// (== the weather_user_cities.location_id); Slug and GismeteoID are the two
// components used to build the forecast URL. Label is operator-facing prose
// only and must never be used to build the URL.
type WeatherGismeteoCity struct {
	LocationID string
	Slug       string
	GismeteoID int
	Label      string
}
