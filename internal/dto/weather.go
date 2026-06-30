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
}

// WeatherCityCreateResponse is the JSON envelope for a successful
// POST /api/me/weather/cities (201 Created). Carries the generated city row ID.
type WeatherCityCreateResponse struct {
	ID string `json:"id"`
}

// WeatherCityRow is one row in the caller's saved city subscription list.
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
}

// WeatherCitiesResponse is the JSON envelope for GET /api/me/weather/cities.
// Items is always a non-nil slice.
type WeatherCitiesResponse struct {
	Items []WeatherCityRow `json:"items"`
}
