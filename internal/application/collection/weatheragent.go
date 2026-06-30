package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// DefaultWeatherThrottleInterval is the minimum elapsed time between consecutive
// Open-Meteo fetches for the same location. It is shorter than a typical
// morning-summary window (07:00 ± 30 min) so a fresh observation always exists
// by notification time. Pass this to NewWeatherAgent instead of a bare 0.
const DefaultWeatherThrottleInterval = time.Hour

// DefaultGismeteoThrottleInterval is the minimum elapsed time between consecutive
// gismeteo fetches for the same location. A longer interval than Open-Meteo is
// intentional: gismeteo serves a daily forecast that does not change significantly
// within a few hours, and reducing fetch frequency lowers egress to a third-party
// property site. The value must be short enough that a same-day gismeteo observation
// reliably exists before the earliest user NotifyHour (default 07:00): a 3 h interval
// on an hourly cron guarantees a today-dated gismeteo run by ~06:00.
const DefaultGismeteoThrottleInterval = 3 * time.Hour

// NewWeatherAgent constructs a WeatherAgent. provider, cityRepo, and obsRepo are
// required. gismeteoProvider may be nil — when nil, the gismeteo phase is skipped
// entirely, preserving exact MVP behaviour. throttleInterval controls the minimum
// elapsed time between Open-Meteo fetches for the same location; pass 0 to use
// DefaultWeatherThrottleInterval. gismeteoThrottleInterval controls the gismeteo
// cadence; pass 0 to use DefaultGismeteoThrottleInterval.
func NewWeatherAgent(
	provider weatherForecastProvider,
	cityRepo weatherCollectionCityRepo,
	obsRepo weatherCollectionObsRepo,
	throttleInterval time.Duration,
	logger io.Writer,
	opts ...WeatherAgentOption,
) (*WeatherAgent, error) {
	if provider == nil || cityRepo == nil || obsRepo == nil {
		return nil, errors.New("weather agent: provider, cityRepo, and obsRepo are all required")
	}
	if throttleInterval <= 0 {
		throttleInterval = DefaultWeatherThrottleInterval
	}
	if logger == nil {
		logger = io.Discard
	}
	a := &WeatherAgent{
		provider:                 provider,
		cityRepo:                 cityRepo,
		obsRepo:                  obsRepo,
		throttleInterval:         throttleInterval,
		gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
		logger:                   logger,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// WeatherAgentOption configures optional behaviour on WeatherAgent.
type WeatherAgentOption func(*WeatherAgent)

// WithGismeteo attaches an optional gismeteo provider to the WeatherAgent.
// When provider is nil, the option is a no-op. throttleInterval controls the
// minimum elapsed time between gismeteo fetches for the same location; pass 0 to
// use DefaultGismeteoThrottleInterval.
func WithGismeteo(provider weatherGismeteoProvider, throttleInterval time.Duration) WeatherAgentOption {
	return func(a *WeatherAgent) {
		if provider == nil {
			return
		}
		a.gismeteo = provider
		if throttleInterval > 0 {
			a.gismeteoThrottleInterval = throttleInterval
		}
	}
}

// WeatherAgent collects weather observations from Open-Meteo (and optionally
// gismeteo) for all distinct subscribed locations and persists them. Each Run
// invocation is one-shot; it is called once per cron tick from cmd/collector.
// Per-location throttling ensures frequent cron ticks do not hammer external APIs.
type WeatherAgent struct {
	provider                 weatherForecastProvider
	cityRepo                 weatherCollectionCityRepo
	obsRepo                  weatherCollectionObsRepo
	throttleInterval         time.Duration
	gismeteo                 weatherGismeteoProvider
	gismeteoThrottleInterval time.Duration
	logger                   io.Writer
}

// weatherForecastProvider fetches a weather forecast for the given coordinates.
type weatherForecastProvider interface {
	Forecast(ctx context.Context, lat, lng float64) (*domain.WeatherObservation, error)
}

// weatherGismeteoProvider is the narrow gismeteo surface the WeatherAgent uses.
// Supports checks whether a location_id is in the curated city map; ForecastBatch
// issues one HTTP GET per supported location. A nil implementation disables the
// gismeteo phase entirely.
type weatherGismeteoProvider interface {
	Supports(locationID string) bool
	ForecastBatch(ctx context.Context, locationIDs []string) (map[string]*domain.WeatherObservation, map[string]error)
}

// weatherCollectionCityRepo is the narrow city-repository surface the collector needs.
type weatherCollectionCityRepo interface {
	ObtainDistinctWeatherLocations(ctx context.Context) ([]domain.WeatherUserCity, error)
}

// weatherCollectionObsRepo is the narrow observation-repository surface the collector needs.
type weatherCollectionObsRepo interface {
	ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error)
	RetainWeatherObservation(ctx context.Context, record *domain.WeatherObservation) error
}

// Run loads all distinct subscribed locations, skips those with a recent Open-Meteo
// observation (throttle gate), fetches a fresh forecast for due locations, and
// persists the result. When a gismeteo provider is configured, it adds a second pass
// for curated-map-eligible, due locations. One failing location never aborts the rest.
// Returns a joined error from all per-location failures; nil if all succeeded.
func (a *WeatherAgent) Run(ctx context.Context) error {
	locations, err := a.cityRepo.ObtainDistinctWeatherLocations(ctx)
	if err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	if len(locations) == 0 {
		return nil
	}

	now := time.Now().UTC()
	var errs []error
	var fetched, skipped, failed int
	total := len(locations)

	for _, loc := range locations {
		if !a.isDue(ctx, loc.LocationID, now) {
			skipped++
			continue
		}

		obs, fetchErr := a.provider.Forecast(ctx, loc.Latitude, loc.Longitude)
		if fetchErr != nil {
			failed++
			fmt.Fprintf(a.logger, "weather collection: location %s: fetch error: %v\n", loc.LocationID, fetchErr)
			errs = append(errs, fmt.Errorf("location %s: forecast: %w", loc.LocationID, fetchErr))
			continue
		}
		obs.LocationID = loc.LocationID

		// Persist under context.Background() so a SIGTERM does not discard a
		// successfully fetched observation. The collector is one-shot; dropping the
		// row here would force the next tick to re-fetch everything that completed.
		if retainErr := a.obsRepo.RetainWeatherObservation(context.Background(), obs); retainErr != nil {
			failed++
			fmt.Fprintf(a.logger, "weather collection: location %s: retain error: %v\n", loc.LocationID, retainErr)
			errs = append(errs, fmt.Errorf("location %s: retain observation: %w", loc.LocationID, retainErr))
			continue
		}
		fetched++
	}

	fmt.Fprintf(a.logger, "weather collection: fetched=%d skipped=%d failed=%d total=%d\n", fetched, skipped, failed, total)

	// Gismeteo phase: optional second provider for curated-map cities.
	if a.gismeteo != nil {
		gErr := a.runGismeteoPhase(ctx, locations, now)
		errs = append(errs, gErr...)
	}

	return errors.Join(errs...)
}

// runGismeteoPhase selects curated-map-eligible, due locations, calls ForecastBatch
// once for all of them, patches each observation's ForecastDate to the city-local
// calendar date, and persists the result. Per-location errors are logged inline and
// returned so Run can join them into its return value.
func (a *WeatherAgent) runGismeteoPhase(ctx context.Context, locations []domain.WeatherUserCity, now time.Time) []error {
	// Single pass: count supported cities, collect due ids, and capture timezone per
	// location so ForecastDate can be patched to city-local after ForecastBatch.
	tzByLoc := make(map[string]string)
	var due []string
	var supported int
	for _, loc := range locations {
		if !a.gismeteo.Supports(loc.LocationID) {
			continue
		}
		supported++
		tzByLoc[loc.LocationID] = loc.Timezone
		if a.gismeteoIsDue(ctx, loc.LocationID, now) {
			due = append(due, loc.LocationID)
		}
	}

	var fetched, failed int
	skipped := supported - len(due)
	var errs []error

	if len(due) > 0 {
		obsByLoc, errByLoc := a.gismeteo.ForecastBatch(ctx, due)
		for locID, obs := range obsByLoc {
			obs.LocationID = locID

			// Patch ForecastDate to the city's local calendar date.
			// decodeGismeteoForecast stamps UTC; for UTC+ cities the UTC date can be
			// one day behind the local date during the midnight crossover window, which
			// would cause loadFreshGismeteo to reject the observation via date mismatch.
			if tz := tzByLoc[locID]; tz != "" {
				if cityLoc, tzErr := time.LoadLocation(tz); tzErr == nil {
					obs.ForecastDate = now.In(cityLoc).Format("2006-01-02")
				}
				// On tz load error keep the UTC date — non-fatal; operator will see
				// no gismeteo comparison in the morning summary until the next fetch.
			}

			if retainErr := a.obsRepo.RetainWeatherObservation(context.Background(), obs); retainErr != nil {
				failed++
				fmt.Fprintf(a.logger, "weather gismeteo: location %s: retain error: %v\n", locID, retainErr)
				errs = append(errs, fmt.Errorf("gismeteo location %s: retain: %w", locID, retainErr))
				continue
			}
			fetched++
		}
		for locID, err := range errByLoc {
			failed++
			fmt.Fprintf(a.logger, "weather gismeteo: location %s: %v\n", locID, err)
			errs = append(errs, fmt.Errorf("gismeteo location %s: %w", locID, err))
		}
	}

	// Gate the summary log on supported > 0 — no-op ticks produce zero-info noise.
	if supported > 0 {
		fmt.Fprintf(a.logger, "weather gismeteo: fetched=%d skipped=%d failed=%d supported=%d\n",
			fetched, skipped, failed, supported)
	}

	return errs
}

// isDue reports whether the location needs a fresh Open-Meteo observation.
// Returns true when the latest observation is absent or older than throttleInterval.
func (a *WeatherAgent) isDue(ctx context.Context, locationID string, now time.Time) bool {
	latest, err := a.obsRepo.ObtainLatestObservation(ctx, locationID, domain.ProviderOpenMeteo)
	if err != nil {
		// ErrNotFound → never fetched yet → due.
		// Any other error → treat as due so the location isn't permanently skipped.
		return true
	}
	return now.Sub(latest.CapturedAt) >= a.throttleInterval
}

// gismeteoIsDue reports whether the location needs a fresh gismeteo observation.
// Returns true when the latest gismeteo observation is absent or older than
// gismeteoThrottleInterval. Uses the gismeteo-specific observation to avoid
// desyncing the two providers' throttle clocks.
func (a *WeatherAgent) gismeteoIsDue(ctx context.Context, locationID string, now time.Time) bool {
	latest, err := a.obsRepo.ObtainLatestObservation(ctx, locationID, domain.ProviderGismeteo)
	if err != nil {
		return true
	}
	return now.Sub(latest.CapturedAt) >= a.gismeteoThrottleInterval
}
