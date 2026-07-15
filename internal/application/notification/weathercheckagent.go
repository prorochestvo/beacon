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

// weatherAlertCooldownForecast is the minimum gap between successive alerts for
// forecast-based kinds (heat, frost, thunderstorm). ~20 h means the alert can
// re-arm the next calendar day without firing twice within one notifier run cycle.
// It must be strictly greater than the notifier's tick interval (typically minutes)
// so the cooldown is what prevents per-tick spam, not luck.
const weatherAlertCooldownForecast = 20 * time.Hour

// weatherAlertCooldownRain is the cooldown for rain alerts. A shorter ~6 h window
// re-arms when a second rain band enters the fixed look-ahead window later in the
// day, while still preventing per-tick spam. Must be strictly greater than the
// notifier tick interval.
const weatherAlertCooldownRain = 6 * time.Hour

// weatherAlertCooldown returns the per-kind cooldown for alert kinds.
func weatherAlertCooldown(kind domain.WeatherNotifyKind) time.Duration {
	switch kind {
	case domain.WeatherNotifyAlertHeat, domain.WeatherNotifyAlertFrost, domain.WeatherNotifyAlertThunderstorm, domain.WeatherNotifyAlertThaw:
		return weatherAlertCooldownForecast
	case domain.WeatherNotifyAlertRain:
		return weatherAlertCooldownRain
	default:
		return weatherAlertCooldownForecast // safe default for any unrecognised future kind
	}
}

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

	// Alert phase: evaluate heat, frost, thunderstorm, and rain threshold kinds.
	// Observations are cached per location_id for the duration of this phase so a
	// city that has multiple alert kinds (or shares a location with another user's
	// city) does not re-query the same row more than once per run.
	obsCache := make(map[string]*domain.WeatherObservation) // location_id → obs
	obsNotFound := make(map[string]bool)                    // location_id → known absent
	var alertQueued, alertAttempted, alertSuppressed int

	// alertKinds lists every alert WeatherNotifyKind processed in a single Run call.
	// Extend this slice when adding a new alert WeatherNotifyKind.
	alertKinds := []domain.WeatherNotifyKind{
		domain.WeatherNotifyAlertHeat,
		domain.WeatherNotifyAlertFrost,
		domain.WeatherNotifyAlertThunderstorm,
		domain.WeatherNotifyAlertRain,
		domain.WeatherNotifyAlertThaw,
	}

	for _, kind := range alertKinds {
		candidates, loadErr := a.cityRepo.ObtainDueWeatherUserCities(ctx, kind)
		if loadErr != nil {
			errs = append(errs, fmt.Errorf("weather alert: load cities for %s: %w", kind, loadErr))
			continue
		}

		for _, city := range candidates {
			obs, obsLoadErr := a.loadCachedObservation(ctx, city.LocationID, obsCache, obsNotFound)
			if obsLoadErr != nil {
				errs = append(errs, obsLoadErr)
				continue
			}
			if obs == nil {
				// No observation yet (ErrNotFound); skip without advancing so the alert
				// fires once data arrives (same behaviour as the morning-summary phase).
				fmt.Fprintf(a.logger, "weather alert: city %s location %s: no observation yet, skipping\n", city.ID, city.LocationID)
				continue
			}

			fired, reason, evalErr := city.EvaluateAlert(*obs, now)
			if evalErr != nil {
				errs = append(errs, fmt.Errorf("weather alert city=%s: evaluate: %w", city.ID, evalErr))
				continue
			}
			if !fired {
				continue
			}

			// Cooldown gate: suppress if an alert was sent recently for this row.
			cooldown := weatherAlertCooldown(city.NotifyKind)
			if !city.LastNotifiedAt.IsZero() && now.Sub(city.LastNotifiedAt) < cooldown {
				alertSuppressed++
				continue // condition still holds but we alerted recently
			}

			msg, renderErr := RenderWeatherAlert(city, reason, *obs)
			if renderErr != nil {
				errs = append(errs, fmt.Errorf("weather alert city=%s: render: %w", city.ID, renderErr))
				continue
			}

			ev := &domain.RateUserEvent{
				UserType: domain.UserTypeTelegram,
				UserID:   city.UserID,
				Message:  msg,
				// SourceName empty → stored as NULL; same transport as morning summary.
			}
			alertAttempted++
			if retainErr := a.eventRepo.RetainRateUserEvent(ctx, ev); retainErr != nil {
				errs = append(errs, fmt.Errorf("weather alert city=%s: queue event: %w", city.ID, retainErr))
				continue // do NOT advance; next run retries
			}
			alertQueued++

			if advErr := a.cityRepo.AdvanceLastNotifiedAt(ctx, city.ID, now); advErr != nil {
				errs = append(errs, fmt.Errorf("weather alert city=%s: advance last_notified_at: %w", city.ID, advErr))
			}
		}
	}

	// Proof-of-execution marker matching RateCheckAgent's pattern.
	fmt.Fprintf(a.logger, "weather check: queued %d/%d events (alerts: %d/%d suppressed: %d)\n",
		totalQueued, totalAttempted, alertQueued, alertAttempted, alertSuppressed)
	return errors.Join(errs...)
}

// loadCachedObservation returns the latest Open-Meteo observation for locationID,
// using obsCache to avoid redundant DB reads within a single Run call. When the
// observation is absent (ErrNotFound) the result is recorded in obsNotFound and
// (nil, nil) is returned on all subsequent lookups for the same locationID. Any
// non-ErrNotFound error is returned as a non-nil error so the caller can append it
// to the run error list and continue — the observation gap is not silently discarded.
func (a *WeatherCheckAgent) loadCachedObservation(
	ctx context.Context,
	locationID string,
	obsCache map[string]*domain.WeatherObservation,
	obsNotFound map[string]bool,
) (*domain.WeatherObservation, error) {
	if obsNotFound[locationID] {
		return nil, nil
	}
	if obs, ok := obsCache[locationID]; ok {
		return obs, nil
	}
	obs, err := a.obsRepo.ObtainLatestObservation(ctx, locationID, domain.ProviderOpenMeteo)
	if err != nil {
		if errors.Is(err, internal.ErrNotFound) {
			obsNotFound[locationID] = true
			return nil, nil
		}
		return nil, fmt.Errorf("weather alert: location %s: load observation: %w", locationID, err)
	}
	obsCache[locationID] = obs
	return obs, nil
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
