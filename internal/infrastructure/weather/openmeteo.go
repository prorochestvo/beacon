// Package weather provides an HTTP client for the Open-Meteo weather API
// (keyless, global JSON), the sole weather data source.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"

	_ "time/tzdata" // embed IANA tzdata so LoadLocation works without system tzdata (WASM, containers)

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
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

// NewOpenMeteo creates an OpenMeteo client whose outbound requests are routed
// through proxyURL when non-empty (direct connection otherwise).
//
// An empty proxyURL produces a direct connection. The Go proxy environment
// triplet (HTTPS_PROXY, HTTP_PROXY, NO_PROXY) is intentionally NOT consulted —
// proxy config is injected explicitly via BEACON_PROXY_URL, matching the rest
// of the app.
//
// logger receives one line per retry and one per recovery, so a run that survived a
// flaky upstream still says so. A clean first attempt writes nothing. A nil logger
// discards.
func NewOpenMeteo(proxyURL string, logger io.Writer) (*OpenMeteo, error) {
	transport := &http.Transport{}

	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			// Redact the raw URL from the log; the operator has it in the env file.
			return nil, errors.New("open-meteo: parse proxy URL: invalid format (value redacted; check the configured proxy URL)")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	if logger == nil {
		logger = io.Discard
	}

	return &OpenMeteo{
		httpClient: &http.Client{
			Timeout:   openMeteoTimeout,
			Transport: transport,
		},
		logger: logger,
	}, nil
}

// NewOpenMeteoWithClient creates an OpenMeteo client with a caller-supplied HTTP
// client. Use this in tests to inject a custom transport or an httptest server.
// A nil logger discards.
func NewOpenMeteoWithClient(client *http.Client, logger io.Writer) *OpenMeteo {
	if logger == nil {
		logger = io.Discard
	}
	return &OpenMeteo{httpClient: client, logger: logger}
}

// OpenMeteo is a proxy-aware HTTP client for the Open-Meteo API (keyless).
// Construct with NewOpenMeteo; do not copy after first use.
type OpenMeteo struct {
	httpClient *http.Client
	logger     io.Writer
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
		return nil, errors.Join(err, loginjector.NewTraceError())
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
			loginjector.NewTraceError(),
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
		return nil, errors.Join(err, loginjector.NewTraceError())
	}
	q := u.Query()
	q.Set("latitude", fmt.Sprintf("%f", lat))
	q.Set("longitude", fmt.Sprintf("%f", lng))
	q.Set("current", "temperature_2m,apparent_temperature,relative_humidity_2m,wind_speed_10m,wind_direction_10m,precipitation,weather_code,cloud_cover")
	q.Set("daily", "temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,weather_code,sunrise,sunset")
	q.Set("hourly", "precipitation_probability,temperature_2m")
	q.Set("timezone", "auto")
	// forecast_days=2 extends the hourly block past local midnight so a "next 6h"
	// rain-alert window is always available late in the day. daily[0] remains today
	// (the API always starts daily[] from the current local calendar day with
	// timezone=auto), so the morning-summary path is unaffected.
	q.Set("forecast_days", "2")
	u.RawQuery = q.Encode()

	body, err := o.get(ctx, u.String())
	if err != nil {
		return nil, err
	}

	return decodeOpenMeteoForecast(body, lat, lng)
}

// get fetches rawURL, re-sending the request when the failure looks transient.
//
// Open-Meteo intermittently answers 5xx — one production log carried 167 of them across
// two locations — and without a retry each one dropped that location for the whole run.
// The request is a GET, so re-sending is safe by construction.
//
// Attempts are bounded by openMeteoMaxAttempts and the wait between them respects ctx, so
// a tick cancelled mid-backoff stops immediately instead of sleeping out its schedule.
func (o *OpenMeteo) get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error

	// Both exits carry the elapsed time because nothing recorded how long an
	// Open-Meteo fault actually lasts, and the whole question of whether this
	// budget is the right size turns on that number. A recovery time is the
	// informative half — it is an upper bound on an outage this window did absorb,
	// so a log full of sub-second recoveries says the budget is already right and
	// their absence says it is always too narrow. The give-up time only confirms
	// what was spent.
	start := time.Now()

	for attempt := 1; attempt <= openMeteoMaxAttempts; attempt++ {
		body, err := o.attempt(ctx, rawURL)
		if err == nil {
			if attempt > 1 {
				fmt.Fprintf(o.logger, "open-meteo: recovered on attempt %d of %d after %s\n",
					attempt, openMeteoMaxAttempts, retryElapsed(start))
			}
			return body, nil
		}

		lastErr = err
		if !isRetryable(err) || attempt == openMeteoMaxAttempts {
			break
		}

		fmt.Fprintf(o.logger, "open-meteo: attempt %d of %d failed, retrying: %v\n", attempt, openMeteoMaxAttempts, err)

		if waitErr := sleepWithContext(ctx, retryBackoff(attempt)); waitErr != nil {
			return nil, errors.Join(waitErr, lastErr, loginjector.NewTraceError())
		}
	}

	// The attempt count rides in the message so an outright failure in the log says how
	// hard it tried, rather than looking identical to a single unlucky request.
	return nil, errors.Join(
		fmt.Errorf("open-meteo: giving up after %d attempt(s) in %s: %w",
			attemptsMade(lastErr), retryElapsed(start), lastErr),
		loginjector.NewTraceError(),
	)
}

// attempt performs exactly one request. Its errors are classified by retryableError so
// get can tell an upstream hiccup from an answer that will not change.
func (o *OpenMeteo) attempt(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("open-meteo: create request: %w", err)
	}
	req.Header.Set("User-Agent", openMeteoUserAgent)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		// Transport-level failures — timeout, reset, refused — are indistinguishable
		// from a 5xx from here and just as transient. A cancelled context is not: the
		// caller asked to stop, and re-sending would ignore that.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("open-meteo: do request: %w", err)
		}
		return nil, retryableError{err: fmt.Errorf("open-meteo: do request: %w", err)}
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Omit the query string from the error to avoid leaking latitude/longitude
		// coordinates (forecast) or search terms (geocode) into logs.
		statusErr := fmt.Errorf("open-meteo: unexpected status %d for %s%s", resp.StatusCode, req.URL.Host, req.URL.Path)
		if resp.StatusCode >= 500 {
			return nil, retryableError{err: statusErr}
		}
		// Everything else in 4xx describes a request that will not become valid by being
		// sent again. 429 is included deliberately: Open-Meteo is keyless and limits by
		// IP, so an immediate re-send asks for the same refusal and pushes the caller
		// further into the limit.
		return nil, statusErr
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, openMeteoMaxResponseBytes))
	if err != nil {
		// A body that died mid-read is the same class of upstream fault as a 5xx.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("open-meteo: read response body: %w", err)
		}
		return nil, retryableError{err: fmt.Errorf("open-meteo: read response body: %w", err)}
	}
	return body, nil
}

const (
	openMeteoGeocodingBase = "https://geocoding-api.open-meteo.com/v1/search"
	openMeteoForecastBase  = "https://api.open-meteo.com/v1/forecast"
	openMeteoUserAgent     = internal.UserAgent
	openMeteoTimeout       = 10 * time.Second

	// openMeteoMaxResponseBytes caps the response body read to protect against
	// runaway servers returning multi-megabyte payloads.
	openMeteoMaxResponseBytes = 1 << 20 // 1 MiB

	// openMeteoMaxAttempts is how many times one request may be sent, first try
	// included. Open-Meteo returns 5xx intermittently — 167 of them across one
	// production log — and a single re-send absorbs most of that. Three bounds the
	// worst case at roughly 31s against an hourly collection tick, and a fault that
	// survives three attempts is not the transient this is for.
	openMeteoMaxAttempts = 3

	// openMeteoRetryBackoff is the wait before the second attempt; it doubles for
	// each attempt after that. Short on purpose: the failure being absorbed is an
	// upstream hiccup answered in milliseconds, not a rate limit that needs to
	// decay, and the whole collection run waits on this.
	openMeteoRetryBackoff = 250 * time.Millisecond

	// openMeteoRetryJitter is the fraction of each backoff that is randomised, so
	// several locations failing on the same tick do not re-send in lockstep.
	openMeteoRetryJitter = 0.2
)

// retryableError marks a failure as an upstream hiccup rather than an answer.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

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
		Hourly struct {
			Time                     []string  `json:"time"`
			PrecipitationProbability []*int    `json:"precipitation_probability"`
			Temperature2m            []float64 `json:"temperature_2m"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.Join(
			fmt.Errorf("open-meteo forecast: decode response: %w", err),
			loginjector.NewTraceError(),
		)
	}

	if len(resp.Daily.Time) == 0 {
		return nil, errors.Join(
			errors.New("open-meteo forecast: daily[] array is empty"),
			loginjector.NewTraceError(),
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
			loginjector.NewTraceError(),
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
				loginjector.NewTraceError(),
			)
		}
		obs.Sunrise = &t
	}
	if len(resp.Daily.Sunset) > 0 && resp.Daily.Sunset[0] != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", resp.Daily.Sunset[0], tzLoc)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("open-meteo forecast: parse sunset %q: %w", resp.Daily.Sunset[0], err),
				loginjector.NewTraceError(),
			)
		}
		obs.Sunset = &t
	}

	// Hourly block: decode time, precipitation_probability, and temperature_2m arrays.
	// Array lengths may legitimately differ when a provider omits a field for some
	// hours; guard against out-of-bounds by using the time array as the spine and
	// indexing into the others only when long enough.
	nHourly := len(resp.Hourly.Time)
	if nHourly > 0 {
		obs.Hourly = make([]domain.WeatherHourlyPoint, 0, nHourly)
		for i, ts := range resp.Hourly.Time {
			t, err := time.ParseInLocation("2006-01-02T15:04", ts, tzLoc)
			if err != nil {
				// Malformed time string — skip this slot rather than hard-failing; the
				// rain evaluator degrades gracefully when fewer points are present.
				continue
			}
			pt := domain.WeatherHourlyPoint{Time: t.UTC()}
			if i < len(resp.Hourly.PrecipitationProbability) && resp.Hourly.PrecipitationProbability[i] != nil {
				v := *resp.Hourly.PrecipitationProbability[i]
				pt.PrecipProb = &v
			}
			if i < len(resp.Hourly.Temperature2m) {
				v := resp.Hourly.Temperature2m[i]
				pt.Temp = &v
			}
			obs.Hourly = append(obs.Hourly, pt)
		}
	}

	return obs, nil
}

func isRetryable(err error) bool {
	var r retryableError
	return errors.As(err, &r)
}

// attemptsMade reports how many attempts a failure represents: a retryable one exhausted
// the budget, anything else stopped on the first answer.
func attemptsMade(err error) int {
	if isRetryable(err) {
		return openMeteoMaxAttempts
	}
	return 1
}

// retryBackoff is the wait before attempt+1, doubling each time and randomised within
// openMeteoRetryJitter so concurrent callers do not re-send in lockstep.
func retryBackoff(attempt int) time.Duration {
	base := openMeteoRetryBackoff << (attempt - 1)
	spread := float64(base) * openMeteoRetryJitter
	// rand is fine here: this randomises timing, it does not protect anything.
	return time.Duration(float64(base) - spread + rand.Float64()*2*spread)
}

// retryElapsed reports how long a retried request has been running, rounded to
// milliseconds: these numbers are read off log lines and compared against each
// other, and the nanosecond tail time.Duration prints by default makes two of
// them hard to rank at a glance.
func retryElapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}

// sleepWithContext waits for d, or returns ctx's error the moment it is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func float64Ptr(v float64) *float64 { return &v }
func intPtr(v int) *int             { return &v }
