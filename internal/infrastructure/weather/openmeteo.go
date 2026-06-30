// Package weather provides HTTP clients for external weather data providers.
// Currently only Open-Meteo (keyless, global JSON API) is implemented; the
// gismeteo provider is deferred to a subsequent increment.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	_ "time/tzdata" // embed IANA tzdata so LoadLocation works without system tzdata (WASM, containers)
)

const (
	openMeteoGeocodingBase = "https://geocoding-api.open-meteo.com/v1/search"
	openMeteoForecastBase  = "https://api.open-meteo.com/v1/forecast"
	openMeteoUserAgent     = "Beacon/1.0 (+https://github.com/seilbekskindirov/beacon)"
	openMeteoTimeout       = 10 * time.Second

	// openMeteoMaxResponseBytes caps the response body read to protect against
	// runaway servers returning multi-megabyte payloads.
	openMeteoMaxResponseBytes = 1 << 20 // 1 MiB
)

// GeoResult holds the fields returned by Open-Meteo geocoding for a single match.
type GeoResult struct {
	// ID is the Open-Meteo internal city identifier, used as the location_id key.
	ID          int64
	Name        string
	Latitude    float64
	Longitude   float64
	Country     string
	CountryCode string
	Admin1      string
	Timezone    string
	Population  int64
}

// OpenMeteo is a proxy-aware HTTP client for the Open-Meteo API (keyless).
// Construct with NewOpenMeteo; do not copy after first use.
type OpenMeteo struct {
	httpClient *http.Client
}

// NewOpenMeteo creates an OpenMeteo client whose outbound requests are routed
// through proxyURL when non-empty (direct connection otherwise).
//
// An empty proxyURL produces a direct connection. The Go proxy environment
// triplet (HTTPS_PROXY, HTTP_PROXY, NO_PROXY) is intentionally NOT consulted —
// proxy config is injected explicitly via BEACON_PROXY_URL, matching the rest
// of the app.
func NewOpenMeteo(proxyURL string) (*OpenMeteo, error) {
	transport := &http.Transport{}

	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			// Redact the raw URL from the log; the operator has it in the env file.
			return nil, errors.New("open-meteo: parse proxy URL: invalid format (value redacted; check the configured proxy URL)")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	return &OpenMeteo{
		httpClient: &http.Client{
			Timeout:   openMeteoTimeout,
			Transport: transport,
		},
	}, nil
}

// NewOpenMeteoWithClient creates an OpenMeteo client with a caller-supplied HTTP
// client. Use this in tests to inject a custom transport or an httptest server.
func NewOpenMeteoWithClient(client *http.Client) *OpenMeteo {
	return &OpenMeteo{httpClient: client}
}

// Geocode queries the Open-Meteo geocoding API for cities matching name and
// returns up to count results. Language is fixed to "ru" so geocoding display
// names come back in Russian (this is a display preference; it does not change
// IDs or coordinates).
//
// Returns an empty slice (not an error) when the API returns no results.
func (o *OpenMeteo) Geocode(ctx context.Context, name string, count int) ([]GeoResult, error) {
	u, err := url.Parse(openMeteoGeocodingBase)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	q := u.Query()
	q.Set("name", name)
	q.Set("count", fmt.Sprintf("%d", count))
	q.Set("language", "ru")
	u.RawQuery = q.Encode()

	body, err := o.get(ctx, u.String())
	if err != nil {
		return nil, err
	}

	var resp struct {
		Results []struct {
			ID          int64   `json:"id"`
			Name        string  `json:"name"`
			Latitude    float64 `json:"latitude"`
			Longitude   float64 `json:"longitude"`
			Country     string  `json:"country"`
			CountryCode string  `json:"country_code"`
			Admin1      string  `json:"admin1"`
			Timezone    string  `json:"timezone"`
			Population  int64   `json:"population"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.Join(
			fmt.Errorf("open-meteo geocode: decode response: %w", err),
			internal.NewTraceError(),
		)
	}

	results := make([]GeoResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		results = append(results, GeoResult{
			ID:          r.ID,
			Name:        r.Name,
			Latitude:    r.Latitude,
			Longitude:   r.Longitude,
			Country:     r.Country,
			CountryCode: r.CountryCode,
			Admin1:      r.Admin1,
			Timezone:    r.Timezone,
			Population:  r.Population,
		})
	}
	return results, nil
}

// LocationKey returns the canonical location_id for geo. It uses the Open-Meteo
// integer geocoding id (as a decimal string) when present; otherwise it falls
// back to coordinates rounded to 4 decimal places so the key is stable. The
// same key must be used by both the city-subscription handler (at subscribe time)
// and the collector (at fetch time) so that observations and subscriptions line up.
func LocationKey(geo GeoResult) string {
	if geo.ID != 0 {
		return fmt.Sprintf("%d", geo.ID)
	}
	return fmt.Sprintf("%.4f,%.4f", geo.Latitude, geo.Longitude)
}

// Forecast fetches the current + daily (today, index 0) forecast for the given
// coordinates from the Open-Meteo forecast API.
//
// The observation Provider is always "open-meteo" (a literal data token). WeatherCode
// carries the raw WMO integer; resolve it via domain.WMOWeatherCode at render time.
//
// timezone=auto makes the daily block local to the queried coordinates, so index 0 of
// daily[] is today in the city-local calendar. sunrise/sunset are also city-local.
//
// The returned observation has a nil ID (caller or repository mints it).
func (o *OpenMeteo) Forecast(ctx context.Context, lat, lng float64) (*domain.WeatherObservation, error) {
	u, err := url.Parse(openMeteoForecastBase)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	q := u.Query()
	q.Set("latitude", fmt.Sprintf("%f", lat))
	q.Set("longitude", fmt.Sprintf("%f", lng))
	q.Set("current", "temperature_2m,apparent_temperature,relative_humidity_2m,wind_speed_10m,wind_direction_10m,precipitation,weather_code,cloud_cover")
	q.Set("daily", "temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,weather_code,sunrise,sunset")
	q.Set("hourly", "precipitation_probability,temperature_2m")
	q.Set("timezone", "auto")
	q.Set("forecast_days", "1")
	u.RawQuery = q.Encode()

	body, err := o.get(ctx, u.String())
	if err != nil {
		return nil, err
	}

	return decodeOpenMeteoForecast(body, lat, lng)
}

// decodeOpenMeteoForecast is the pure-decode step extracted so tests can exercise
// it without a live HTTP server.
func decodeOpenMeteoForecast(body []byte, lat, lng float64) (*domain.WeatherObservation, error) {
	var resp struct {
		Timezone string `json:"timezone"`
		Current  struct {
			Time                string  `json:"time"`
			Temperature2m       float64 `json:"temperature_2m"`
			ApparentTemperature float64 `json:"apparent_temperature"`
			RelativeHumidity2m  int     `json:"relative_humidity_2m"`
			WindSpeed10m        float64 `json:"wind_speed_10m"`
			WindDirection10m    int     `json:"wind_direction_10m"`
			Precipitation       float64 `json:"precipitation"`
			WeatherCode         int     `json:"weather_code"`
			CloudCover          int     `json:"cloud_cover"`
		} `json:"current"`
		Daily struct {
			Time                 []string  `json:"time"`
			Temperature2mMax     []float64 `json:"temperature_2m_max"`
			Temperature2mMin     []float64 `json:"temperature_2m_min"`
			PrecipitationSum     []float64 `json:"precipitation_sum"`
			PrecipitationProbMax []int     `json:"precipitation_probability_max"`
			WeatherCode          []int     `json:"weather_code"`
			Sunrise              []string  `json:"sunrise"`
			Sunset               []string  `json:"sunset"`
		} `json:"daily"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.Join(
			fmt.Errorf("open-meteo forecast: decode response: %w", err),
			internal.NewTraceError(),
		)
	}

	if len(resp.Daily.Time) == 0 {
		return nil, errors.Join(
			errors.New("open-meteo forecast: daily[] array is empty"),
			internal.NewTraceError(),
		)
	}

	// Load the city timezone returned by timezone=auto. Open-Meteo returns sunrise
	// and sunset as local ISO strings without an offset (e.g. "2024-01-15T07:23").
	// Parsing them in the correct location produces a proper UTC instant; without
	// this, time.Parse tags them as UTC and stores a wrong instant (off by the
	// city's UTC offset).
	tzLoc, err := time.LoadLocation(resp.Timezone)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open-meteo forecast: load timezone %q: %w", resp.Timezone, err),
			internal.NewTraceError(),
		)
	}

	capturedAt := time.Now().UTC()

	obs := &domain.WeatherObservation{
		Provider:     domain.ProviderOpenMeteo,
		Latitude:     lat,
		Longitude:    lng,
		CapturedAt:   capturedAt,
		ForecastDate: resp.Daily.Time[0],
	}

	// Current snapshot fields.
	obs.TempCurrent = float64Ptr(resp.Current.Temperature2m)
	obs.TempFeels = float64Ptr(resp.Current.ApparentTemperature)
	obs.Humidity = intPtr(resp.Current.RelativeHumidity2m)
	obs.WindSpeed = float64Ptr(resp.Current.WindSpeed10m)
	obs.WindDir = intPtr(resp.Current.WindDirection10m)
	obs.Precip = float64Ptr(resp.Current.Precipitation)
	obs.CloudCover = intPtr(resp.Current.CloudCover)

	// Current weather_code comes from the current block (not the daily block, which
	// is the dominant code for the whole day).
	obs.WeatherCode = intPtr(resp.Current.WeatherCode)

	// Daily forecast for today (index 0).
	if len(resp.Daily.Temperature2mMax) > 0 {
		obs.TempMax = float64Ptr(resp.Daily.Temperature2mMax[0])
	}
	if len(resp.Daily.Temperature2mMin) > 0 {
		obs.TempMin = float64Ptr(resp.Daily.Temperature2mMin[0])
	}
	if len(resp.Daily.PrecipitationSum) > 0 {
		obs.PrecipSum = float64Ptr(resp.Daily.PrecipitationSum[0])
	}
	if len(resp.Daily.PrecipitationProbMax) > 0 {
		obs.PrecipProbMax = intPtr(resp.Daily.PrecipitationProbMax[0])
	}
	if len(resp.Daily.WeatherCode) > 0 {
		// Overwrite with the dominant daily code (better for morning summary display
		// than the current-snapshot code).
		obs.WeatherCode = intPtr(resp.Daily.WeatherCode[0])
	}

	// sunrise and sunset are local ISO8601 strings without an offset because
	// timezone=auto makes them city-local. ParseInLocation converts them to
	// correct UTC instants using tzLoc loaded above; callers render via .In(cityLoc).
	if len(resp.Daily.Sunrise) > 0 && resp.Daily.Sunrise[0] != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", resp.Daily.Sunrise[0], tzLoc)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("open-meteo forecast: parse sunrise %q: %w", resp.Daily.Sunrise[0], err),
				internal.NewTraceError(),
			)
		}
		obs.Sunrise = &t
	}
	if len(resp.Daily.Sunset) > 0 && resp.Daily.Sunset[0] != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", resp.Daily.Sunset[0], tzLoc)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("open-meteo forecast: parse sunset %q: %w", resp.Daily.Sunset[0], err),
				internal.NewTraceError(),
			)
		}
		obs.Sunset = &t
	}

	return obs, nil
}

func (o *OpenMeteo) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open-meteo: create request: %w", err),
			internal.NewTraceError(),
		)
	}
	req.Header.Set("User-Agent", openMeteoUserAgent)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open-meteo: do request: %w", err),
			internal.NewTraceError(),
		)
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.Join(
			// Omit the query string from the error to avoid leaking latitude/longitude
			// coordinates (forecast) or search terms (geocode) into logs.
			fmt.Errorf("open-meteo: unexpected status %d for %s%s", resp.StatusCode, req.URL.Host, req.URL.Path),
			internal.NewTraceError(),
		)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, openMeteoMaxResponseBytes))
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open-meteo: read response body: %w", err),
			internal.NewTraceError(),
		)
	}
	return body, nil
}

func float64Ptr(v float64) *float64 { return &v }
func intPtr(v int) *int             { return &v }
