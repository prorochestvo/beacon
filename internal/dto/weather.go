package dto

// WeatherCitySearchItem is one geocoding result returned by
// GET /api/me/weather/cities/search. The client picks one and posts it to
// POST /api/me/weather/cities. All string fields are ready for display.
type WeatherCitySearchItem struct {
	// LocationID is the canonical location key derived from the geocoding result.
	// It is stable for a given city and used to de-duplicate subscriptions.
	LocationID  string  `json:"location_id"`
	DisplayName string  `json:"display_name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Timezone    string  `json:"timezone"`
	Country     string  `json:"country"`
	Admin1      string  `json:"admin1"`
}

// WeatherCitySearchResponse is the JSON envelope for GET /api/me/weather/cities/search.
// Items is always a non-nil slice; an empty search result returns an empty array.
type WeatherCitySearchResponse struct {
	Items []WeatherCitySearchItem `json:"items"`
}

// WeatherCityCreateRequest is the JSON body for POST /api/me/weather/cities.
// The client copies the resolved fields from the chosen search result verbatim.
// NotifyHour is the local hour (0–23) for the daily morning summary; when
// omitted the server applies its default (7 = 07:00 local time).
//
// To create an alert, set NotifyKind to one of: "alert_heat", "alert_frost",
// "alert_thunderstorm", "rain_alert", "alert_thaw". When NotifyKind is empty or
// omitted the server defaults to "morning_summary". ConditionValue is the numeric
// threshold: °C for heat/frost (e.g. "35"), probability percent for rain_alert
// (e.g. "70", range [0,100]); empty for alert_thunderstorm, alert_thaw, and
// morning_summary.
type WeatherCityCreateRequest struct {
	LocationID  string  `json:"location_id"`
	DisplayName string  `json:"display_name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Timezone    string  `json:"timezone"`
	Country     string  `json:"country"`
	Admin1      string  `json:"admin1"`
	// NotifyHour is a pointer so the client can omit the field to use the default.
	NotifyHour *int `json:"notify_hour,omitempty"`
	// NotifyKind identifies the subscription type. Omit or leave empty for the
	// default "morning_summary". Alert kinds: "alert_heat", "alert_frost",
	// "alert_thunderstorm", "rain_alert", "alert_thaw".
	NotifyKind string `json:"notify_kind,omitempty"`
	// ConditionValue is the threshold for alert kinds. Required for alert_heat and
	// alert_frost (a decimal number in °C) and for rain_alert (a probability percent
	// in [0,100], e.g. "70"); empty for alert_thunderstorm, alert_thaw, and
	// morning_summary.
	ConditionValue string `json:"condition_value,omitempty"`
}

// WeatherCityCreateResponse is the JSON envelope for a successful
// POST /api/me/weather/cities (201 Created). Carries the generated city row ID.
type WeatherCityCreateResponse struct {
	ID string `json:"id"`
}

// WeatherCityRow is one row in the caller's saved city subscription list.
// Each (city, notify_kind) pair is its own row: a city with a morning_summary
// subscription and a heat alert appears as two rows sharing the same location_id.
// The client should group by location_id for display.
type WeatherCityRow struct {
	ID          string  `json:"id"`
	LocationID  string  `json:"location_id"`
	DisplayName string  `json:"display_name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Timezone    string  `json:"timezone"`
	Country     string  `json:"country"`
	Admin1      string  `json:"admin1"`
	// NotifyHour is the local hour (0–23) at which the daily morning summary fires.
	NotifyHour int `json:"notify_hour"`
	// NotifyKind is the subscription type: "morning_summary", "alert_heat",
	// "alert_frost", "alert_thunderstorm", "rain_alert", or "alert_thaw".
	NotifyKind string `json:"notify_kind"`
	// ConditionValue is the alert threshold: decimal °C string for heat/frost,
	// decimal percent string (0–100) for rain_alert. Empty for morning_summary,
	// alert_thunderstorm, and alert_thaw.
	ConditionValue string `json:"condition_value,omitempty"`
}

// WeatherCitiesResponse is the JSON envelope for GET /api/me/weather/cities.
// Items is always a non-nil slice.
type WeatherCitiesResponse struct {
	Items []WeatherCityRow `json:"items"`
}

// WeatherCurrentItem is one city's latest stored weather observation in the
// GET /api/me/weather/current response. HasData is false when the collector has
// not yet produced an observation for this location; all observation fields are
// omitted in that case so the client can render a "no data yet" state without
// interpreting absent numeric fields as zero.
type WeatherCurrentItem struct {
	LocationID  string `json:"location_id"`
	DisplayName string `json:"display_name"`
	Timezone    string `json:"timezone"`
	// HasData is true when a stored Open-Meteo observation exists for this city.
	// When false all remaining fields are absent in the JSON.
	HasData bool `json:"has_data"`

	// Current snapshot fields. Nil when the observation has no value for this field.
	TempCurrent *float64 `json:"temp_current,omitempty"`
	TempFeels   *float64 `json:"temp_feels,omitempty"`
	Humidity    *int     `json:"humidity,omitempty"`
	WindSpeed   *float64 `json:"wind_speed,omitempty"`
	WindDir     *int     `json:"wind_dir,omitempty"`
	Precip      *float64 `json:"precip,omitempty"`
	CloudCover  *int     `json:"cloud_cover,omitempty"`

	// Daily forecast fields for the observation date.
	TempMax *float64 `json:"temp_max,omitempty"`
	TempMin *float64 `json:"temp_min,omitempty"`
	// WeatherCode is the raw WMO Weather Interpretation Code. Resolved to
	// human text in ConditionText and ConditionEmoji server-side.
	WeatherCode    *int   `json:"weather_code,omitempty"`
	ConditionText  string `json:"condition_text,omitempty"`
	ConditionEmoji string `json:"condition_emoji,omitempty"`
	// SunriseLocal and SunsetLocal are formatted as "15:04" in the city's IANA
	// timezone, computed server-side so the WASM client needs no tzdata.
	SunriseLocal string `json:"sunrise_local,omitempty"`
	SunsetLocal  string `json:"sunset_local,omitempty"`
	// CapturedAt is the UTC timestamp of the observation in RFC 3339 format.
	CapturedAt string `json:"captured_at,omitempty"`
}

// WeatherCurrentResponse is the JSON envelope for GET /api/me/weather/current.
// Items contains one entry per distinct subscribed location_id; it is always a
// non-nil slice so the client can distinguish "no subscriptions" from an error.
type WeatherCurrentResponse struct {
	Items []WeatherCurrentItem `json:"items"`
}
