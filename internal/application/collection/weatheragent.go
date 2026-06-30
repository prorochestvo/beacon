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

// NewWeatherAgent constructs a WeatherAgent. proxyURL is passed to the Open-Meteo
// provider, which routes all outbound traffic through it when non-empty.
// throttleInterval controls the minimum elapsed time between fetches for the same
// location; pass 0 to use the default (1 hour).
func NewWeatherAgent(
	provider weatherForecastProvider,
	cityRepo weatherCollectionCityRepo,
	obsRepo weatherCollectionObsRepo,
	throttleInterval time.Duration,
	logger io.Writer,
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
	return &WeatherAgent{
		provider:         provider,
		cityRepo:         cityRepo,
		obsRepo:          obsRepo,
		throttleInterval: throttleInterval,
		logger:           logger,
	}, nil
}

// WeatherAgent collects weather observations from Open-Meteo for all distinct
// subscribed locations and persists them. Each Run invocation is one-shot; it is
// called once per cron tick from cmd/collector. Per-location throttling ensures
// frequent cron ticks do not hammer the Open-Meteo API.
type WeatherAgent struct {
	provider         weatherForecastProvider
	cityRepo         weatherCollectionCityRepo
	obsRepo          weatherCollectionObsRepo
	throttleInterval time.Duration
	logger           io.Writer
}

// weatherForecastProvider fetches a weather forecast for the given coordinates.
type weatherForecastProvider interface {
	Forecast(ctx context.Context, lat, lng float64) (*domain.WeatherObservation, error)
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
// observation (throttle gate), fetches a fresh forecast for due locations, and persists
// the result. One failing location never aborts the rest. Returns a joined error from
// all per-location failures; nil if all succeeded.
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
	return errors.Join(errs...)
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
