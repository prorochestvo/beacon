// Package weather provides HTTP clients for external weather data providers.
// Gismeteo scrapes www.gismeteo.kz city pages via plain HTTP and extracts today's
// high/low temperature, precipitation sum, and weather condition into a
// domain.WeatherObservation.
//
// Coverage limitation: gismeteo has no public geocoding or city-search API.
// Supported cities come from the weather_gismeteo_cities table, injected into the
// provider at construction, keyed by the Open-Meteo geocoding id (== LocationKey(geo),
// the location_id stored on every weather_user_cities row). Cities outside the map
// fall back to Open-Meteo only; callers check Supports before calling
// Forecast/ForecastBatch.
//
// Host: www.gismeteo.kz (.kz) — Russian-language, metric (°C, mm). Egress from
// cmd/collector honours BEACON_PROXY_URL via the HTTP transport. The Go proxy
// environment triplet (HTTPS_PROXY, HTTP_PROXY, NO_PROXY) is intentionally NOT
// consulted — proxy config is injected explicitly via BEACON_PROXY_URL, matching
// the rest of the app.
//
// Plain HTTP: gismeteo.kz serves the daily forecast values in the raw HTTP
// response — temperature, precipitation, and condition are present pre-hydration
// without JavaScript execution. A browser User-Agent is set on every request to
// avoid bot-gate redirects.
//
// DOM fragility: gismeteo page structure can change on redesign and break the
// regexes. Every selector and pattern is isolated in a named constant so DOM drift
// is a one-line edit. Failures surface as per-location errors in the collector log
// (weather has no execution_history table).
package weather

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

const (
	gismeteoBaseURL = "https://www.gismeteo.kz"

	// gismeteoUserAgent is a browser-like User-Agent. gismeteo.kz may redirect or
	// return 403 for requests with an empty or bot-like UA — a common bot-defence
	// on property-site CDNs.
	gismeteoUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

	// gismeteoTimeout is the per-request HTTP timeout. It must be shorter than the
	// collector's SIGTERM grace period so a hung request does not prevent clean shutdown.
	gismeteoTimeout = 15 * time.Second

	// gismeteoMaxResponseBytes caps the response body read. gismeteo.kz pages are
	// larger than the Open-Meteo JSON response; 2 MiB accommodates a full HTML page
	// while protecting against runaway servers.
	gismeteoMaxResponseBytes = 1 << 21 // 2 MiB

	// Regex constants for gismeteo page scraping.
	// Each field has its own named constant so DOM drift breaks/fixes one field at a time.

	// reGismeteoCondition matches the Russian condition text from the active tab's
	// data-tooltip attribute. The text is server-rendered and represents today's overall
	// forecast condition (e.g. "облачно, небольшой дождь" = overcast, light rain).
	// Group 1: the full tooltip string, passed to mapGismeteoCondition.
	reGismeteoCondition = `(?s)class="weathertab is-active"\s+data-tooltip="([^"]+)"`

	// reGismeteoTempMax matches today's high temperature (°C) from the active tab's
	// 50%-wide day chart slot at the chart top (top:0px = chart maximum). The regex
	// is anchored to "weathertab is-active" so multi-day pages do not accidentally
	// match tomorrow's chart (which carries the same DOM structure).
	// Group 1: raw value string — validated by strconv.ParseFloat in decodeGismeteoForecast.
	reGismeteoTempMax = `(?s)class="weathertab is-active".*?top:\s*0px;width:\s*50%;[^>]*><temperature-value\s+value="(-?[^"]+)"\s+from-unit="c"`

	// reGismeteoTempMin matches today's low temperature (°C) from the active tab's
	// 50%-wide day chart slot at a non-zero top offset (lower chart position = lower
	// temperature). Anchored to "weathertab is-active" for the same reason as TempMax.
	// Group 1: raw value string — validated by strconv.ParseFloat in decodeGismeteoForecast.
	reGismeteoTempMin = `(?s)class="weathertab is-active".*?top:\s*[1-9]\d*px;width:\s*50%;[^>]*><temperature-value\s+value="(-?[^"]+)"\s+from-unit="c"`

	// reGismeteoPrecip matches each 3-hourly precipitation forecast value (mm) in the
	// precipitation-bars widget. Sum all matches for today's total precipitation sum.
	// Group 1: raw value string — validated by strconv.ParseFloat in decodeGismeteoForecast.
	reGismeteoPrecip = `<precipitation-value\s+class="item-unit\s+unit-blue"\s+value="([^"]+)"\s+from-unit="mm"\s+reactive>`
)

// ErrGismeteoLocationUnavailable is returned by Forecast and ForecastBatch when the
// caller-supplied location_id is absent from the injected coverage map. The caller
// should fall back to Open-Meteo-only data — gismeteo coverage is limited by design
// and does not grow automatically with new user subscriptions.
var ErrGismeteoLocationUnavailable = errors.New("gismeteo unavailable for this location")

// compiled regex values are package-level to avoid recompilation on every call.
var (
	reGismeteoConditionC = regexp.MustCompile(reGismeteoCondition)
	reGismeteoTempMaxC   = regexp.MustCompile(reGismeteoTempMax)
	reGismeteoTempMinC   = regexp.MustCompile(reGismeteoTempMin)
	reGismeteoPrecipC    = regexp.MustCompile(reGismeteoPrecip)
)

// gismeteoCity holds the URL components for one supported city on gismeteo.kz.
// Coverage now lives in the weather_gismeteo_cities table and is injected into the
// provider at construction (see NewGismeteo); a new city is one INSERT, no rebuild.
type gismeteoCity struct {
	slug string // URL slug component, e.g. "almaty"
	id   int    // gismeteo numeric city id, e.g. 5205
}

// gismeteoConditionRules maps Russian weather keywords from the data-tooltip attribute
// to WMO Weather Interpretation Codes. Rules are checked in order — more specific
// phrases (e.g. "небольшой дождь") appear before broader ones (e.g. "дождь") so
// the most precise match wins. Source: gismeteo.kz tooltip vocabulary, June 2026.
// Re-validate after a gismeteo UI update; add entries as new tooltips are observed.
var gismeteoConditionRules = []struct {
	keyword string
	wmo     int
}{
	{"гроза с градом", 99},  // thunderstorm with heavy hail
	{"гроза", 95},           // thunderstorm
	{"сильный снег", 75},    // heavy snowfall
	{"умеренный снег", 73},  // moderate snowfall
	{"снег с дождём", 67},   // freezing rain / sleet
	{"небольшой снег", 71},  // slight snowfall
	{"снег", 71},            // snowfall (fallback)
	{"сильный дождь", 65},   // heavy rain
	{"умеренный дождь", 63}, // moderate rain
	{"небольшой дождь", 61}, // slight rain
	{"дождь", 61},           // rain (fallback)
	{"морось", 51},          // drizzle
	{"туман", 45},           // fog
	{"пасмурно", 3},         // overcast (gloomy)
	// "малооблачно" and "переменная облачность" must appear BEFORE the bare "облачно"
	// rule because "облачно" is a substring of both: strings.Contains would otherwise
	// match the shorter form first and misclassify "mainly clear" as "overcast".
	{"переменная облачность", 2}, // partly cloudy
	{"малооблачно", 1},           // mainly clear
	{"облачно", 3},               // overcast (fallback — keep after more specific forms)
	{"ясно", 0},                  // clear sky
}

// mapGismeteoCondition converts the gismeteo data-tooltip Russian condition text to
// the nearest WMO Weather Interpretation Code. Returns nil when the text is empty or
// no known keyword matches — the caller stores nil (NULL), not zero.
func mapGismeteoCondition(tooltip string) *int {
	lower := strings.ToLower(tooltip)
	for _, rule := range gismeteoConditionRules {
		if strings.Contains(lower, strings.ToLower(rule.keyword)) {
			v := rule.wmo
			return &v
		}
	}
	return nil
}

// Gismeteo scrapes today's weather forecast from www.gismeteo.kz for cities in the
// injected coverage map via plain HTTP. Construct with NewGismeteo; do not copy
// after first use.
type Gismeteo struct {
	httpClient *http.Client
	baseURL    string // "https://www.gismeteo.kz" in production; overridable in tests
	userAgent  string // browser-like UA; falls back to gismeteoUserAgent when injected empty
	cities     map[string]gismeteoCity
}

// NewGismeteo creates a Gismeteo provider whose outbound requests are routed through
// proxyURL when non-empty (direct connection otherwise). The coverage map, base URL,
// and User-Agent are injected from the weather_gismeteo_cities / weather_sources config
// rows: cities is the location_id → coverage map returned by ObtainGismeteoCoverage
// (converted to the internal representation here). An empty baseURL or userAgent falls
// back to the compiled-in gismeteoBaseURL / gismeteoUserAgent constants, so a
// partially-populated config row still works. A nil or empty cities map yields a
// provider that Supports nothing (coverage disabled) rather than one that panics.
//
// An empty proxyURL produces a direct connection. The Go proxy environment
// triplet (HTTPS_PROXY, HTTP_PROXY, NO_PROXY) is intentionally NOT consulted —
// proxy config is injected explicitly via BEACON_PROXY_URL.
func NewGismeteo(proxyURL string, cities map[string]domain.WeatherGismeteoCity, baseURL, userAgent string) (*Gismeteo, error) {
	transport := &http.Transport{}

	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			// Redact the raw URL from the log; the operator has it in the env file.
			return nil, errors.New("gismeteo: parse proxy URL: invalid format (value redacted; check the configured proxy URL)")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	// Trim a trailing slash so buildGismeteoURL never emits a double slash
	// (<base>//weather-...), which would 404. Do this before the empty-fallback so a
	// slash-only base_url also degrades to the compiled-in default.
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = gismeteoBaseURL
	}
	if userAgent == "" {
		userAgent = gismeteoUserAgent
	}

	return &Gismeteo{
		httpClient: &http.Client{
			Timeout:   gismeteoTimeout,
			Transport: transport,
		},
		baseURL:   baseURL,
		userAgent: userAgent,
		cities:    convertGismeteoCities(cities),
	}, nil
}

// convertGismeteoCities maps the injected domain coverage rows to the internal
// gismeteoCity representation, keyed by location_id (the map key). A nil input
// yields a non-nil empty map so Supports and ForecastBatch never dereference nil.
func convertGismeteoCities(cities map[string]domain.WeatherGismeteoCity) map[string]gismeteoCity {
	out := make(map[string]gismeteoCity, len(cities))
	for locID, c := range cities {
		out[locID] = gismeteoCity{slug: c.Slug, id: c.GismeteoID}
	}
	return out
}

// newGismeteoWithClient creates a Gismeteo provider with a caller-supplied HTTP client
// and injected coverage map. Use in tests when you need a fixed production base URL;
// prefer newGismeteoForTest when you also need to redirect requests to an httptest.Server.
func newGismeteoWithClient(client *http.Client, cities map[string]domain.WeatherGismeteoCity) *Gismeteo {
	return &Gismeteo{httpClient: client, baseURL: gismeteoBaseURL, userAgent: gismeteoUserAgent, cities: convertGismeteoCities(cities)}
}

// newGismeteoForTest creates a Gismeteo provider with an injected HTTP client, base
// URL, and coverage map. Use in tests to redirect requests to an httptest.Server
// without live network access.
func newGismeteoForTest(client *http.Client, baseURL string, cities map[string]domain.WeatherGismeteoCity) *Gismeteo {
	return &Gismeteo{httpClient: client, baseURL: baseURL, userAgent: gismeteoUserAgent, cities: convertGismeteoCities(cities)}
}

// Supports reports whether locationID has an entry in the injected coverage map and
// gismeteo data is therefore obtainable for that location.
func (g *Gismeteo) Supports(locationID string) bool {
	_, ok := g.cities[locationID]
	return ok
}

// Forecast fetches today's gismeteo forecast for locationID via a plain HTTP GET.
// It is a convenience wrapper around ForecastBatch for a single location.
//
// Returns ErrGismeteoLocationUnavailable when locationID is not in the curated map.
func (g *Gismeteo) Forecast(ctx context.Context, locationID string) (*domain.WeatherObservation, error) {
	obsByLoc, errByLoc := g.ForecastBatch(ctx, []string{locationID})
	if err, ok := errByLoc[locationID]; ok {
		return nil, err
	}
	obs, ok := obsByLoc[locationID]
	if !ok {
		// Should not happen unless ForecastBatch has a bug; guard defensively.
		return nil, errors.Join(
			fmt.Errorf("gismeteo: Forecast: no result for location %q (unexpected)", locationID),
			internal.NewTraceError(),
		)
	}
	return obs, nil
}

// ForecastBatch fetches today's gismeteo forecast for all supported locationIDs.
// Each supported location issues one HTTP GET; unsupported location_ids are placed
// in errByLoc and skipped (check Supports first if you need to distinguish).
//
// Returns two maps keyed by location_id: obsByLoc for successes and errByLoc for
// failures. A per-location error never aborts the rest of the batch.
func (g *Gismeteo) ForecastBatch(ctx context.Context, locationIDs []string) (map[string]*domain.WeatherObservation, map[string]error) {
	obsByLoc := make(map[string]*domain.WeatherObservation)
	errByLoc := make(map[string]error)

	for _, locID := range locationIDs {
		city, ok := g.cities[locID]
		if !ok {
			errByLoc[locID] = fmt.Errorf("location %q: %w", locID, ErrGismeteoLocationUnavailable)
			continue
		}

		rawURL := buildGismeteoURL(g.baseURL, city)
		html, err := g.get(ctx, rawURL)
		if err != nil {
			errByLoc[locID] = fmt.Errorf("gismeteo: location %q: fetch: %w", locID, err)
			continue
		}

		obs, err := decodeGismeteoForecast(html, locID)
		if err != nil {
			errByLoc[locID] = err
			continue
		}
		obsByLoc[locID] = obs
	}

	return obsByLoc, errByLoc
}

// buildGismeteoURL constructs the canonical gismeteo.kz city forecast URL from the
// base URL and city entry: <base>/weather-<slug>-<id>/. The slug is path-escaped so a
// hand-entered coverage row with an unusual slug cannot break URL parsing.
func buildGismeteoURL(baseURL string, c gismeteoCity) string {
	return fmt.Sprintf("%s/weather-%s-%d/", baseURL, url.PathEscape(c.slug), c.id)
}

// decodeGismeteoForecast is the pure-decode step: it extracts today's high, low,
// precipitation sum, and condition from the raw HTML response and returns a
// WeatherObservation. It is extracted from ForecastBatch so tests can exercise it
// against a committed fixture without a live HTTP server.
//
// Fields that gismeteo does not expose or that fail to parse are left nil (NULL),
// never zero. A parse failure returns a wrapped error; it never panics.
//
// When ALL four scraped fields (TempMax, TempMin, PrecipSum, WeatherCode) are nil,
// decodeGismeteoForecast returns an error so the caller counts it as a failure
// rather than storing an all-nil observation — this makes DOM drift visible in logs.
//
// ForecastDate is set to today's UTC calendar date. The caller (runGismeteoPhase in
// WeatherAgent) patches it to the city's local calendar date after ForecastBatch
// returns, using the timezone from the WeatherUserCity record.
func decodeGismeteoForecast(html []byte, locationID string) (*domain.WeatherObservation, error) {
	obs := &domain.WeatherObservation{
		Provider:     domain.ProviderGismeteo,
		LocationID:   locationID,
		CapturedAt:   time.Now().UTC(),
		ForecastDate: time.Now().UTC().Format("2006-01-02"),
	}

	// TempMax: chart slot at chart-top position (top:0px = highest temperature).
	if m := reGismeteoTempMaxC.FindSubmatch(html); m != nil {
		v, err := strconv.ParseFloat(string(m[1]), 64)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("gismeteo: location %q: parse TempMax %q: %w", locationID, m[1], err),
				internal.NewTraceError(),
			)
		}
		obs.TempMax = float64Ptr(v)
	}

	// TempMin: chart slot at a non-zero top offset (lower chart position = lower temperature).
	if m := reGismeteoTempMinC.FindSubmatch(html); m != nil {
		v, err := strconv.ParseFloat(string(m[1]), 64)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("gismeteo: location %q: parse TempMin %q: %w", locationID, m[1], err),
				internal.NewTraceError(),
			)
		}
		obs.TempMin = float64Ptr(v)
	}

	// PrecipSum: sum of all 3-hourly precipitation values in the forecast widget.
	precipMatches := reGismeteoPrecipC.FindAllSubmatch(html, -1)
	if len(precipMatches) > 0 {
		var precipTotal float64
		for _, m := range precipMatches {
			v, err := strconv.ParseFloat(string(m[1]), 64)
			if err != nil {
				return nil, errors.Join(
					fmt.Errorf("gismeteo: location %q: parse PrecipSum value %q: %w", locationID, m[1], err),
					internal.NewTraceError(),
				)
			}
			precipTotal += v
		}
		obs.PrecipSum = float64Ptr(precipTotal)
	}

	// WeatherCode: derive WMO code from the active tab's Russian condition tooltip.
	if m := reGismeteoConditionC.FindSubmatch(html); m != nil {
		obs.WeatherCode = mapGismeteoCondition(string(m[1]))
	}

	// DOM-drift guard: if no field was extracted, the page structure has changed in
	// a way that breaks all four regexes. Return a descriptive error so the caller
	// counts this as failed=1 rather than silently storing an all-nil observation.
	if obs.TempMax == nil && obs.TempMin == nil && obs.PrecipSum == nil && obs.WeatherCode == nil {
		return nil, errors.Join(
			fmt.Errorf("gismeteo: location %q: no usable fields parsed — possible DOM drift (check regex constants)", locationID),
			internal.NewTraceError(),
		)
	}

	return obs, nil
}

// get issues a plain HTTP GET to rawURL with a browser User-Agent and returns the
// response body capped at gismeteoMaxResponseBytes.
func (g *Gismeteo) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("gismeteo: create request: %w", err),
			internal.NewTraceError(),
		)
	}
	req.Header.Set("User-Agent", g.userAgent)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("gismeteo: do request: %w", err),
			internal.NewTraceError(),
		)
	}
	defer func(c io.Closer) { _ = c.Close() }(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Use resp.Request.URL (final URL after any redirects) so the error message
		// names the host that actually returned the unexpected status, not the original.
		return nil, errors.Join(
			fmt.Errorf("gismeteo: unexpected status %d for %s%s", resp.StatusCode, resp.Request.URL.Host, resp.Request.URL.Path),
			internal.NewTraceError(),
		)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, gismeteoMaxResponseBytes))
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("gismeteo: read response body: %w", err),
			internal.NewTraceError(),
		)
	}
	return body, nil
}
