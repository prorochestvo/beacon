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

	// Proof-of-execution marker matching RateCheckAgent's pattern.
	fmt.Fprintf(a.logger, "weather check: queued %d/%d events\n", totalQueued, totalAttempted)
	return errors.Join(errs...)
}
