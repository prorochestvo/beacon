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

// Alert kinds (heat, frost, thunderstorm, rain, thaw) are edge-triggered via the
// per-row domain.WeatherUserCity.AlertLatched boolean, not a timer cooldown: a row
// fires once on the transition into its condition and stays silent until the
// condition clears and the latch re-arms (domain.WeatherUserCity.EvaluateLatched).
// On top of the latch, a per-forecast_date fire cap (keyed on the repurposed
// last_notified_at column via domain.ForecastDateKey) caps a row to at most one
// fire per forecast_date — the anti-jitter backstop for a daily min/max that
// crosses the fire/re-arm boundary more than once within one calendar day as the
// collector rewrites the observation. See the alert-phase loop below and
// plans/262-weather-alert-edge-trigger-hysteresis.md for the full design.

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
	SetWeatherAlertLatched(ctx context.Context, id string, latched bool) error
	MarkWeatherAlertFired(ctx context.Context, id string, firedForDate time.Time) error
}

// weatherCheckObsRepository is the narrow observation-repository surface the check agent needs.
type weatherCheckObsRepository interface {
	ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error)
}

// Run loads all morning-summary city subscriptions, evaluates which are due in
// each city's local timezone, loads the latest Open-Meteo observation, renders the
// summary, and queues it as a RateUserEvent.
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

		htmlMsg, renderErr := RenderMorningSummary(city, *obs)
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

			prev := city.AlertLatched
			fire, next, reason, evalErr := city.EvaluateLatched(*obs, now, prev)
			if evalErr != nil {
				errs = append(errs, fmt.Errorf("weather alert city=%s: evaluate: %w", city.ID, evalErr))
				continue
			}

			if !fire {
				// Re-arm (or any latch change without a fire) must still be persisted: this
				// is the day-5 / day-7 transition that produces NO notification. Only write
				// on a real change so steady state costs zero writes per tick.
				if next != prev {
					if setErr := a.cityRepo.SetWeatherAlertLatched(ctx, city.ID, next); setErr != nil {
						errs = append(errs, fmt.Errorf("weather alert city=%s: persist re-arm: %w", city.ID, setErr))
					}
				}
				continue
			}

			// fire == true ⇒ next == true and prev == false (guaranteed by EvaluateLatched).
			// Second gate: cap to one fire per forecast_date (anti-jitter backstop). For
			// alert kinds LastNotifiedAt holds the forecast_date of the last fire.
			fdKey, keyErr := domain.ForecastDateKey(obs.ForecastDate)
			if keyErr == nil && !city.LastNotifiedAt.IsZero() && city.LastNotifiedAt.Equal(fdKey) {
				// Same forecast_date already fired — a within-day jitter re-cross. Record
				// the latch edge (next == true) but do NOT notify again.
				if next != prev {
					if setErr := a.cityRepo.SetWeatherAlertLatched(ctx, city.ID, next); setErr != nil {
						errs = append(errs, fmt.Errorf("weather alert city=%s: persist latch (gated): %w", city.ID, setErr))
					}
				}
				alertSuppressed++
				continue
			}
			if keyErr != nil {
				// Malformed/empty forecast_date is an anomaly: log, allow the fire (never
				// drop an alert), but the fire cap cannot be recorded — fall back to a
				// latch-only write below.
				fmt.Fprintf(a.logger, "weather alert: city %s: unparseable forecast_date %q: %v\n", city.ID, obs.ForecastDate, keyErr)
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
				continue // do NOT latch or record the fire date; next tick retries
			}
			alertQueued++

			// Persist ONLY after a successful enqueue, so a queue failure re-fires next tick.
			var persistErr error
			if keyErr == nil {
				persistErr = a.cityRepo.MarkWeatherAlertFired(ctx, city.ID, fdKey) // latch=1 + record forecast_date
			} else {
				persistErr = a.cityRepo.SetWeatherAlertLatched(ctx, city.ID, true) // anomaly: latch only
			}
			if persistErr != nil {
				errs = append(errs, fmt.Errorf("weather alert city=%s: persist fire: %w", city.ID, persistErr))
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
