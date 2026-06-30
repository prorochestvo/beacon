package notification

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// weatherGismeteoMaxAge is the maximum age of a gismeteo observation that will be
// included in the morning summary. An observation older than this constant is treated
// as stale and the summary falls back to Open-Meteo only. This is a belt-and-braces
// backstop: the forecast_date equality check is the primary freshness guard.
const weatherGismeteoMaxAge = 24 * time.Hour

// NewWeatherCheckAgent constructs a WeatherCheckAgent. All arguments are required.
func NewWeatherCheckAgent(
	cityRepo weatherCheckCityRepository,
	obsRepo weatherCheckObsRepository,
	eventRepo rateCheckEventRepository,
	logger io.Writer,
) (*WeatherCheckAgent, error) {
	if cityRepo == nil || obsRepo == nil || eventRepo == nil {
		return nil, errors.New("weather check agent: cityRepo, obsRepo, and eventRepo are all required")
	}
	if logger == nil {
		logger = io.Discard
	}
	return &WeatherCheckAgent{
		cityRepo:  cityRepo,
		obsRepo:   obsRepo,
		eventRepo: eventRepo,
		logger:    logger,
	}, nil
}

// WeatherCheckAgent evaluates due weather city subscriptions, renders morning-weather
// summaries, and queues them as RateUserEvents for delivery by RateDispatchAgent.
// It reuses the existing FX notification queue (rate_user_events) with an empty
// SourceName → NULL so there is no FK dependency on rate_sources.
//
// When a fresh gismeteo observation exists for the same forecast_date, both
// observations are passed to RenderMorningSummary for a side-by-side comparison.
// A missing or stale gismeteo observation is a normal non-error condition — the
// summary falls back to Open-Meteo only.
type WeatherCheckAgent struct {
	cityRepo  weatherCheckCityRepository
	obsRepo   weatherCheckObsRepository
	eventRepo rateCheckEventRepository // reuse the same narrow interface as RateCheckAgent
	logger    io.Writer
}

// weatherCheckCityRepository is the narrow city-repository surface the check agent needs.
type weatherCheckCityRepository interface {
	ObtainDueWeatherUserCities(ctx context.Context, notifyKind domain.WeatherNotifyKind) ([]domain.WeatherUserCity, error)
	AdvanceLastNotifiedAt(ctx context.Context, id string, when time.Time) error
}

// weatherCheckObsRepository is the narrow observation-repository surface the check agent needs.
type weatherCheckObsRepository interface {
	ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error)
}

// Run loads all morning-summary city subscriptions, evaluates which are due in
// each city's local timezone, loads the latest Open-Meteo observation, optionally
// loads a gismeteo observation for cross-provider comparison, renders the summary,
// and queues it as a RateUserEvent.
//
// Critical ordering: AdvanceLastNotifiedAt is called only after the event is
// successfully queued. On a RetainRateUserEvent failure, the city is NOT marked
// notified so the next run retries. A city with no observation yet is skipped
// without advancing so it fires once collection data arrives.
func (a *WeatherCheckAgent) Run(ctx context.Context) error {
	cities, err := a.cityRepo.ObtainDueWeatherUserCities(ctx, domain.WeatherNotifyMorningSummary)
	if err != nil {
		return errors.Join(err, internal.NewTraceError())
	}

	now := time.Now().UTC()
	var errs []error
	var totalQueued, totalAttempted int

	for _, city := range cities {
		due, tzErr := city.IsMorningDue(now)
		if tzErr != nil {
			// Timezone load failed — log and skip, not fatal. A bad timezone
			// beats a missed notification (wrong offset is correctable later).
			fmt.Fprintf(a.logger, "weather check: city %s: timezone error: %v\n", city.ID, tzErr)
			continue
		}
		if !due {
			continue
		}

		obs, obsErr := a.obsRepo.ObtainLatestObservation(ctx, city.LocationID, domain.ProviderOpenMeteo)
		if obsErr != nil {
			if errors.Is(obsErr, internal.ErrNotFound) {
				// No observation yet; do NOT advance last_notified_at so the summary
				// fires once the collector has stored data for this location.
				fmt.Fprintf(a.logger, "weather check: city %s: no observation yet, skipping\n", city.ID)
				continue
			}
			errs = append(errs, fmt.Errorf("weather check city=%s: load observation: %w", city.ID, obsErr))
			continue
		}

		// Attempt to load a fresh gismeteo observation for cross-provider comparison.
		// A missing or stale gismeteo observation is a normal non-error condition.
		observations := []domain.WeatherObservation{*obs}
		if gObs := a.loadFreshGismeteo(ctx, city.LocationID, obs.ForecastDate, now); gObs != nil {
			observations = append(observations, *gObs)
		}

		htmlMsg, renderErr := RenderMorningSummary(city, observations...)
		if renderErr != nil {
			errs = append(errs, fmt.Errorf("weather check city=%s: render: %w", city.ID, renderErr))
			continue
		}

		// Queue as a generic notification event. SourceName is intentionally empty
		// so it maps to NULL in the DB (no FK to rate_sources) and the existing
		// RateDispatchAgent delivers it without any weather-specific transport code.
		ev := &domain.RateUserEvent{
			UserType: domain.UserTypeTelegram,
			UserID:   city.UserID,
			Message:  htmlMsg,
			// SourceName empty → sourceNameForDB returns nil → stored as NULL
		}
		totalAttempted++
		if retainErr := a.eventRepo.RetainRateUserEvent(ctx, ev); retainErr != nil {
			errs = append(errs, fmt.Errorf("weather check city=%s: queue event: %w", city.ID, retainErr))
			continue // do NOT advance last_notified_at; next run retries
		}
		totalQueued++

		// Advance last_notified_at only after the event is successfully queued.
		if advErr := a.cityRepo.AdvanceLastNotifiedAt(ctx, city.ID, now); advErr != nil {
			errs = append(errs, fmt.Errorf("weather check city=%s: advance last_notified_at: %w", city.ID, advErr))
		}
	}

	// Proof-of-execution marker matching RateCheckAgent's pattern.
	fmt.Fprintf(a.logger, "weather check: queued %d/%d events\n", totalQueued, totalAttempted)
	return errors.Join(errs...)
}

// loadFreshGismeteo attempts to load the latest gismeteo observation for locationID.
// Returns nil (without error) when:
//   - no gismeteo observation exists (ErrNotFound),
//   - the observation's ForecastDate does not match openMeteoForecastDate,
//   - the observation's CapturedAt is older than weatherGismeteoMaxAge.
//
// A non-ErrNotFound error is logged and treated the same as an absent observation —
// gismeteo must never block the morning summary.
func (a *WeatherCheckAgent) loadFreshGismeteo(ctx context.Context, locationID, openMeteoForecastDate string, now time.Time) *domain.WeatherObservation {
	gObs, err := a.obsRepo.ObtainLatestObservation(ctx, locationID, domain.ProviderGismeteo)
	if err != nil {
		if !errors.Is(err, internal.ErrNotFound) {
			// Unexpected error — log so operators can investigate without failing the summary.
			fmt.Fprintf(a.logger, "weather check: location %s: load gismeteo observation: %v\n", locationID, err)
		}
		return nil
	}

	// forecast_date equality is the primary guard: yesterday's gismeteo scrape must
	// not appear next to today's Open-Meteo observation.
	if gObs.ForecastDate != openMeteoForecastDate {
		fmt.Fprintf(a.logger, "weather check: location %s: gismeteo observation skipped (forecast_date %q != open-meteo %q)\n",
			locationID, gObs.ForecastDate, openMeteoForecastDate)
		return nil
	}

	// CapturedAt age bound is a belt-and-braces backstop in case forecast_date
	// comparison passes but the observation is nonetheless very old.
	if now.Sub(gObs.CapturedAt) > weatherGismeteoMaxAge {
		fmt.Fprintf(a.logger, "weather check: location %s: gismeteo observation skipped (captured_at %s is older than %s)\n",
			locationID, gObs.CapturedAt.Format(time.RFC3339), weatherGismeteoMaxAge)
		return nil
	}

	return gObs
}
