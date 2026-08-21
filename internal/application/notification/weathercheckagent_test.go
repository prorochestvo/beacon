package notification

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ weatherCheckCityRepository = (*mockWeatherCheckCityRepo)(nil)
var _ weatherCheckObsRepository = (*mockWeatherCheckObsRepo)(nil)
var _ weatherCheckForecastRepository = (*mockWeatherCheckForecastRepo)(nil)

// Compile-time assertions that the concrete repository types satisfy the interfaces.
var _ weatherCheckCityRepository = &repository.WeatherUserCityRepository{}
var _ weatherCheckObsRepository = &repository.WeatherObservationRepository{}
var _ weatherCheckForecastRepository = &repository.WeatherForecastDayRepository{}

func TestNewWeatherCheckAgent(t *testing.T) {
	t.Parallel()

	t.Run("valid construction succeeds", func(t *testing.T) {
		t.Parallel()
		a, err := NewWeatherCheckAgent(
			&mockWeatherCheckCityRepo{},
			&mockWeatherCheckObsRepo{},
			&mockWeatherCheckForecastRepo{},
			&mockCheckEventRepository{},
			io.Discard,
		)
		require.NoError(t, err)
		require.NotNil(t, a)
	})

	t.Run("nil cityRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherCheckAgent(nil, &mockWeatherCheckObsRepo{}, &mockWeatherCheckForecastRepo{}, &mockCheckEventRepository{}, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil obsRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherCheckAgent(&mockWeatherCheckCityRepo{}, nil, &mockWeatherCheckForecastRepo{}, &mockCheckEventRepository{}, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil forecastRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherCheckAgent(&mockWeatherCheckCityRepo{}, &mockWeatherCheckObsRepo{}, nil, &mockCheckEventRepository{}, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil eventRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherCheckAgent(&mockWeatherCheckCityRepo{}, &mockWeatherCheckObsRepo{}, &mockWeatherCheckForecastRepo{}, nil, io.Discard)
		require.Error(t, err)
	})
}

func TestWeatherCheckAgent_Run(t *testing.T) {
	t.Parallel()

	// dueCity returns a city that IsMorningDue always evaluates to true:
	// UTC timezone, NotifyHour=0 (midnight), never notified. Current time is
	// always after midnight so the fire condition is met deterministically.
	dueCity := func(id, userID, locationID string) domain.WeatherUserCity {
		return domain.WeatherUserCity{
			ID:             id,
			UserType:       domain.UserTypeTelegram,
			UserID:         userID,
			LocationID:     locationID,
			DisplayName:    "Test City",
			Timezone:       "UTC",
			NotifyHour:     0,           // midnight; current time is always past midnight
			LastNotifiedAt: time.Time{}, // zero = never notified
		}
	}

	// notDueCity returns a city that IsMorningDue always evaluates to false:
	// already notified at the current moment, so it won't re-fire today.
	notDueCity := func(id, userID, locationID string) domain.WeatherUserCity {
		return domain.WeatherUserCity{
			ID:             id,
			UserType:       domain.UserTypeTelegram,
			UserID:         userID,
			LocationID:     locationID,
			DisplayName:    "Test City",
			Timezone:       "UTC",
			NotifyHour:     0,
			LastNotifiedAt: time.Now().UTC(), // notified just now → not due again today
		}
	}

	today := time.Now().UTC().Format("2006-01-02")

	tempMax := 25.0
	tempMin := 15.0
	goodObs := &domain.WeatherObservation{
		Provider:     domain.ProviderOpenMeteo,
		LocationID:   "loc1",
		TempMax:      &tempMax,
		TempMin:      &tempMin,
		ForecastDate: today,
		CapturedAt:   time.Now().UTC(),
	}

	t.Run("due city with observation queues one event and advances", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{
			cityRepo:     cityRepo,
			obsRepo:      obsRepo,
			forecastRepo: &mockWeatherCheckForecastRepo{},
			eventRepo:    eventRepo,
			logger:       io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))

		require.Len(t, eventRepo.retained, 1)
		ev := eventRepo.retained[0]
		assert.NotEmpty(t, ev.Message, "queued event must have a non-empty message")
		assert.Empty(t, ev.SourceName, "SourceName must be empty so it stores as NULL")
		assert.Equal(t, domain.UserTypeTelegram, ev.UserType)
		assert.Equal(t, "user1", ev.UserID)
		// last_notified_at must be advanced
		require.Len(t, cityRepo.advanced, 1)
		assert.Equal(t, "c1", cityRepo.advanced[0])
	})

	t.Run("not-due city produces no event and no advance", func(t *testing.T) {
		t.Parallel()
		city := notDueCity("c1", "user1", "loc1")
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{
			cityRepo:     cityRepo,
			obsRepo:      obsRepo,
			forecastRepo: &mockWeatherCheckForecastRepo{},
			eventRepo:    eventRepo,
			logger:       io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained)
		require.Empty(t, cityRepo.advanced)
	})

	t.Run("due city with no observation skips and does not advance", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherCheckObsRepo{globalErr: internal.ErrNotFound}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{
			cityRepo:     cityRepo,
			obsRepo:      obsRepo,
			forecastRepo: &mockWeatherCheckForecastRepo{},
			eventRepo:    eventRepo,
			logger:       io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event must be queued when no observation exists")
		require.Empty(t, cityRepo.advanced, "last_notified_at must NOT be advanced when observation is absent")
	})

	t.Run("timezone load error skips city, is not fatal", func(t *testing.T) {
		t.Parallel()
		badCity := domain.WeatherUserCity{
			ID:          "c-bad-tz",
			Timezone:    "Galaxy/Nowhere",
			UserID:      "user1",
			LocationID:  "loc1",
			DisplayName: "X",
			NotifyHour:  0,
		}
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{badCity}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
		}}
		eventRepo := &mockCheckEventRepository{}
		var logBuf strings.Builder

		a := &WeatherCheckAgent{
			cityRepo:     cityRepo,
			obsRepo:      obsRepo,
			forecastRepo: &mockWeatherCheckForecastRepo{},
			eventRepo:    eventRepo,
			logger:       &logBuf,
		}
		// Must NOT return an error; bad-tz city is skipped with a log line.
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained)
		assert.Contains(t, logBuf.String(), "timezone error")
	})

	t.Run("event queue failure does not advance last_notified_at", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
		}}
		eventRepo := &mockCheckEventRepository{err: errors.New("db write fail")}

		a := &WeatherCheckAgent{
			cityRepo:     cityRepo,
			obsRepo:      obsRepo,
			forecastRepo: &mockWeatherCheckForecastRepo{},
			eventRepo:    eventRepo,
			logger:       io.Discard,
		}
		err := a.Run(t.Context())
		require.Error(t, err)
		require.Empty(t, cityRepo.advanced, "advance must not be called after a retain failure")
	})

	t.Run("city repo error is returned immediately", func(t *testing.T) {
		t.Parallel()
		a := &WeatherCheckAgent{
			cityRepo:     &mockWeatherCheckCityRepo{err: errors.New("db down")},
			obsRepo:      &mockWeatherCheckObsRepo{},
			forecastRepo: &mockWeatherCheckForecastRepo{},
			eventRepo:    &mockCheckEventRepository{},
			logger:       io.Discard,
		}
		require.Error(t, a.Run(t.Context()))
	})

	t.Run("one city fails obs load, other cities still processed", func(t *testing.T) {
		t.Parallel()
		city1 := dueCity("c1", "user1", "loc1")
		city2 := dueCity("c2", "user2", "loc2")
		obsRepo := &mockWeatherCheckObsRepo{
			obsErrByLocation: map[string]error{"loc1": errors.New("transient fail")},
			obsByProvider:    map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: goodObs},
		}
		eventRepo := &mockCheckEventRepository{}
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city1, city2}}

		a := &WeatherCheckAgent{
			cityRepo:     cityRepo,
			obsRepo:      obsRepo,
			forecastRepo: &mockWeatherCheckForecastRepo{},
			eventRepo:    eventRepo,
			logger:       io.Discard,
		}
		err := a.Run(t.Context())
		require.Error(t, err, "joined error must contain the failing location")
		assert.Len(t, eventRepo.retained, 1, "city2 must still be queued")
		assert.Len(t, cityRepo.advanced, 1, "only city2 must be advanced")
		assert.Equal(t, "c2", cityRepo.advanced[0])
	})

	// Alert phase subtests. Alert kinds are edge-triggered via AlertLatched
	// (domain.WeatherUserCity.EvaluateLatched), not a timer cooldown; a
	// per-forecast_date fire cap (LastNotifiedAt for alert rows) caps a row to at
	// most one fire per forecast_date. See weathercheckagent.go's alert-phase loop.

	t.Run("alert_heat: armed and met fires and marks the forecast_date fired", func(t *testing.T) {
		t.Parallel()
		tempMax := 38.0 // >= 35 threshold
		alertCity := domain.WeatherUserCity{
			ID:             "alert-c1",
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-alert",
			LocationID:     "loc-alert",
			DisplayName:    "Hot City",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertHeat,
			ConditionValue: "35",
			AlertLatched:   false, // armed
		}
		alertObs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   "loc-alert",
			TempMax:      &tempMax,
			ForecastDate: "2026-01-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertHeat: {alertCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: alertObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "one heat alert event must be queued")
		assert.Contains(t, eventRepo.retained[0].Message, "Heat alert")
		require.Len(t, cityRepo.fired, 1, "the fire must be recorded via MarkWeatherAlertFired")
		assert.Equal(t, "alert-c1", cityRepo.fired[0].id)
		wantKey, err := domain.ForecastDateKey("2026-01-10")
		require.NoError(t, err)
		assert.True(t, wantKey.Equal(cityRepo.fired[0].firedForDate))
		assert.Empty(t, cityRepo.latched, "MarkWeatherAlertFired already sets the latch; no separate SetWeatherAlertLatched call")
	})

	t.Run("alert_heat: already latched and still met does not re-fire or persist", func(t *testing.T) {
		t.Parallel()
		tempMax := 40.0 // still above threshold
		alertCity := domain.WeatherUserCity{
			ID:             "alert-c2",
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-alert",
			LocationID:     "loc-alert",
			DisplayName:    "Hot City",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertHeat,
			ConditionValue: "35",
			AlertLatched:   true, // already fired, condition still met
		}
		alertObs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   "loc-alert",
			TempMax:      &tempMax,
			ForecastDate: "2026-01-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertHeat: {alertCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: alertObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "a latched, still-met row must not re-fire")
		require.Empty(t, cityRepo.fired)
		require.Empty(t, cityRepo.latched, "steady state (next == prev) must cost zero writes")
	})

	t.Run("alert_heat: condition not met produces no event and no persist call", func(t *testing.T) {
		t.Parallel()
		tempMax := 30.0 // below 35 threshold
		alertCity := domain.WeatherUserCity{
			ID:             "alert-c3",
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-alert",
			LocationID:     "loc-alert",
			DisplayName:    "Cool City",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertHeat,
			ConditionValue: "35",
			AlertLatched:   false,
		}
		alertObs := &domain.WeatherObservation{
			Provider:   domain.ProviderOpenMeteo,
			LocationID: "loc-alert",
			TempMax:    &tempMax,
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertHeat: {alertCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: alertObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when condition is not met")
		require.Empty(t, cityRepo.fired)
		require.Empty(t, cityRepo.latched, "armed and still not met is steady state: no write")
	})

	t.Run("alert: no observation for location skips without persisting", func(t *testing.T) {
		t.Parallel()
		alertCity := domain.WeatherUserCity{
			ID:             "alert-c4",
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-alert",
			LocationID:     "loc-no-obs",
			DisplayName:    "No Data City",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertFrost,
			ConditionValue: "0",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertFrost: {alertCity},
			},
		}
		// globalErr = ErrNotFound → no observation for any location
		obsRepo := &mockWeatherCheckObsRepo{globalErr: internal.ErrNotFound}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when observation is absent")
		require.Empty(t, cityRepo.fired, "must not persist when observation absent")
		require.Empty(t, cityRepo.latched)
	})

	t.Run("alert: observation is cached per location_id across multiple alert kinds", func(t *testing.T) {
		t.Parallel()
		tempMax := 36.0
		tempMin := -2.0
		alertObs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   "loc-shared",
			TempMax:      &tempMax,
			TempMin:      &tempMin,
			ForecastDate: "2026-01-10",
		}
		heatCity := domain.WeatherUserCity{
			ID: "heat-c", UserType: domain.UserTypeTelegram, UserID: "u1",
			LocationID: "loc-shared", DisplayName: "SharedCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertHeat, ConditionValue: "35",
		}
		frostCity := domain.WeatherUserCity{
			ID: "frost-c", UserType: domain.UserTypeTelegram, UserID: "u1",
			LocationID: "loc-shared", DisplayName: "SharedCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertHeat:  {heatCity},
				domain.WeatherNotifyAlertFrost: {frostCity},
			},
		}
		callCount := 0
		trackingObs := &mockCountingObsRepo{obs: alertObs, count: &callCount}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: trackingObs, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 2, "both heat and frost must fire")
		// The obs must be fetched only once for the shared location_id.
		assert.Equal(t, 1, callCount, "observation must be cached: only one DB call for the same location_id across two alert kinds")
	})

	t.Run("alert_thunderstorm: armed and met fires and marks fired", func(t *testing.T) {
		t.Parallel()
		code := 95
		alertCity := domain.WeatherUserCity{
			ID: "thunder-c", UserType: domain.UserTypeTelegram, UserID: "u1",
			LocationID: "loc-storm", DisplayName: "StormCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertThunderstorm,
		}
		alertObs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   "loc-storm",
			WeatherCode:  &code,
			ForecastDate: "2026-01-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertThunderstorm: {alertCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: alertObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		assert.Contains(t, eventRepo.retained[0].Message, "Thunderstorm alert")
		require.Len(t, cityRepo.fired, 1)
		assert.Equal(t, "thunder-c", cityRepo.fired[0].id)
	})

	t.Run("rain_alert: armed and met fires and marks fired", func(t *testing.T) {
		t.Parallel()
		// Hourly point 1 h from now falls within the 6 h window for any real clock value.
		now := time.Now().UTC()
		prob := 85
		rainCity := domain.WeatherUserCity{
			ID: "rain-c1", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain1", DisplayName: "RainCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
		}
		rainObs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   "loc-rain1",
			Hourly:       []domain.WeatherHourlyPoint{{Time: now.Add(time.Hour), PrecipProb: &prob}},
			ForecastDate: "2026-01-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {rainCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: rainObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "one rain alert event must be queued")
		assert.Contains(t, eventRepo.retained[0].Message, "Rain alert")
		require.Empty(t, cityRepo.fired, "rain_alert is exempt from the per-forecast_date fire cap")
		require.Len(t, cityRepo.latched, 1, "rain_alert persists the latch alone")
		assert.Equal(t, "rain-c1", cityRepo.latched[0].id)
		assert.True(t, cityRepo.latched[0].latched)
	})

	t.Run("rain_alert: already latched and still met does not re-fire or persist", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		prob := 85
		rainCity := domain.WeatherUserCity{
			ID: "rain-c2", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain2", DisplayName: "RainCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
			AlertLatched: true, // already fired, condition still met
		}
		rainObs := &domain.WeatherObservation{
			Provider:   domain.ProviderOpenMeteo,
			LocationID: "loc-rain2",
			Hourly:     []domain.WeatherHourlyPoint{{Time: now.Add(time.Hour), PrecipProb: &prob}},
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {rainCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: rainObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "a latched, still-met rain row must not re-fire")
		require.Empty(t, cityRepo.fired)
		require.Empty(t, cityRepo.latched)
	})

	t.Run("rain_alert: probability below threshold produces no event", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		prob := 50 // below 70% threshold
		rainCity := domain.WeatherUserCity{
			ID: "rain-c3", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain3", DisplayName: "DrizzleCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
		}
		rainObs := &domain.WeatherObservation{
			Provider:   domain.ProviderOpenMeteo,
			LocationID: "loc-rain3",
			Hourly:     []domain.WeatherHourlyPoint{{Time: now.Add(time.Hour), PrecipProb: &prob}},
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {rainCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: rainObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when probability below threshold")
		require.Empty(t, cityRepo.fired)
		require.Empty(t, cityRepo.latched)
	})

	t.Run("rain_alert: no hourly data (unevaluable) skips without persisting", func(t *testing.T) {
		t.Parallel()
		rainCity := domain.WeatherUserCity{
			ID: "rain-c4", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain4", DisplayName: "NoDataCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
		}
		rainObs := &domain.WeatherObservation{
			Provider:   domain.ProviderOpenMeteo,
			LocationID: "loc-rain4",
			Hourly:     nil, // no hourly data yet
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {rainCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: rainObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when hourly data is absent")
		require.Empty(t, cityRepo.fired)
		require.Empty(t, cityRepo.latched, "a data gap must not persist any latch change")
	})

	t.Run("alert_thaw: armed and met fires and marks fired", func(t *testing.T) {
		t.Parallel()
		tempMin := -3.0
		tempMax := 2.0
		thawCity := domain.WeatherUserCity{
			ID: "thaw-c1", UserType: domain.UserTypeTelegram, UserID: "u-thaw",
			LocationID: "loc-thaw", DisplayName: "ThawCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertThaw,
		}
		thawObs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   "loc-thaw",
			TempMin:      &tempMin,
			TempMax:      &tempMax,
			ForecastDate: "2026-01-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertThaw: {thawCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: thawObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "one thaw alert event must be queued")
		assert.Contains(t, eventRepo.retained[0].Message, "Thaw alert")
		require.Len(t, cityRepo.fired, 1)
		assert.Equal(t, "thaw-c1", cityRepo.fired[0].id)
	})

	t.Run("alert_thaw: already latched and still met does not re-fire or persist", func(t *testing.T) {
		t.Parallel()
		tempMin := -3.0
		tempMax := 2.0
		thawCity := domain.WeatherUserCity{
			ID: "thaw-c2", UserType: domain.UserTypeTelegram, UserID: "u-thaw",
			LocationID: "loc-thaw2", DisplayName: "ThawCity", Timezone: "UTC",
			NotifyKind:   domain.WeatherNotifyAlertThaw,
			AlertLatched: true, // already fired, condition still met
		}
		thawObs := &domain.WeatherObservation{
			Provider:   domain.ProviderOpenMeteo,
			LocationID: "loc-thaw2",
			TempMin:    &tempMin,
			TempMax:    &tempMax,
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertThaw: {thawCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: thawObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "a latched, still-met thaw row must not re-fire")
		require.Empty(t, cityRepo.fired)
		require.Empty(t, cityRepo.latched)
	})

	t.Run("evaluator error on bad condition_value skips bad city, good city still fires", func(t *testing.T) {
		t.Parallel()
		tempMax := 40.0 // above 35 threshold for good city
		badCity := domain.WeatherUserCity{
			ID: "bad-c", UserType: domain.UserTypeTelegram, UserID: "u-bad",
			LocationID: "loc-bad", DisplayName: "BadCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertHeat, ConditionValue: "notanumber",
		}
		goodCity := domain.WeatherUserCity{
			ID: "good-c", UserType: domain.UserTypeTelegram, UserID: "u-good",
			LocationID: "loc-good", DisplayName: "GoodCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertHeat, ConditionValue: "35",
		}
		sharedObs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			TempMax:      &tempMax,
			ForecastDate: "2026-01-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertHeat: {badCity, goodCity},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: sharedObs,
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		err := a.Run(t.Context())
		require.Error(t, err, "evaluator error for bad city must surface in the returned error")
		assert.Contains(t, err.Error(), "evaluate", "error must reference the evaluate step")
		require.Len(t, eventRepo.retained, 1, "good city must still queue an event")
		assert.Equal(t, "u-good", eventRepo.retained[0].UserID, "queued event must be for the good city's user")
		// bad city must not have been persisted at all.
		for _, f := range cityRepo.fired {
			assert.NotEqual(t, "bad-c", f.id, "bad city must not be marked fired after evaluator failure")
		}
		require.Len(t, cityRepo.fired, 1)
		assert.Equal(t, "good-c", cityRepo.fired[0].id)
	})

	// The following subtests exercise the crux of the feature: the prev→next latch
	// wiring and the per-forecast_date fire cap, threading persisted state from one
	// Run call into the next (mirroring how the real notifier ticks repeatedly).

	t.Run("fire once, then silent on the next tick with the same observation", func(t *testing.T) {
		t.Parallel()
		tempMin := -4.0
		frostCity := domain.WeatherUserCity{
			ID: "latch-once", UserType: domain.UserTypeTelegram, UserID: "u-once",
			LocationID: "loc-once", DisplayName: "OnceCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
			AlertLatched: false, // armed
		}
		obs := &domain.WeatherObservation{
			Provider: domain.ProviderOpenMeteo, LocationID: "loc-once",
			TempMin: &tempMin, ForecastDate: "2026-02-01",
		}

		firstRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
			domain.WeatherNotifyAlertFrost: {frostCity},
		}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: obs}}
		eventRepo := &mockCheckEventRepository{}
		a := &WeatherCheckAgent{cityRepo: firstRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "the first tick must fire")
		require.Len(t, firstRepo.fired, 1)
		wantKey, err := domain.ForecastDateKey("2026-02-01")
		require.NoError(t, err)
		assert.True(t, wantKey.Equal(firstRepo.fired[0].firedForDate))

		// Second tick: the row now carries AlertLatched=true (as persisted by the first
		// tick), same observation.
		frostCity.AlertLatched = true
		secondRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
			domain.WeatherNotifyAlertFrost: {frostCity},
		}}
		eventRepo2 := &mockCheckEventRepository{}
		a2 := &WeatherCheckAgent{cityRepo: secondRepo, obsRepo: obsRepo, eventRepo: eventRepo2, logger: io.Discard}
		require.NoError(t, a2.Run(t.Context()))
		require.Empty(t, eventRepo2.retained, "the second tick must be silent")
		require.Empty(t, secondRepo.fired)
		require.Empty(t, secondRepo.latched)
	})

	t.Run("intra-day jitter: three scrapes on one forecast_date fire exactly once", func(t *testing.T) {
		t.Parallel()
		const forecastDate = "2026-02-05"
		key, err := domain.ForecastDateKey(forecastDate)
		require.NoError(t, err)

		baseCity := domain.WeatherUserCity{
			ID: "jitter-c", UserType: domain.UserTypeTelegram, UserID: "u-jitter",
			LocationID: "loc-jitter", DisplayName: "JitterCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
		}
		runOnce := func(latched bool, lastNotified time.Time, tempMin float64) (*mockCheckEventRepository, *mockWeatherCheckCityRepo) {
			city := baseCity
			city.AlertLatched = latched
			city.LastNotifiedAt = lastNotified
			lower := tempMin
			obs := &domain.WeatherObservation{
				Provider: domain.ProviderOpenMeteo, LocationID: "loc-jitter",
				TempMin: &lower, ForecastDate: forecastDate,
			}
			cityRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertFrost: {city},
			}}
			obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: obs}}
			eventRepo := &mockCheckEventRepository{}
			a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
			require.NoError(t, a.Run(t.Context()))
			return eventRepo, cityRepo
		}

		// Scrape 1: min=-4 (met), armed, never fired → fires, MarkWeatherAlertFired(key).
		ev1, repo1 := runOnce(false, time.Time{}, -4)
		require.Len(t, ev1.retained, 1, "scrape 1 must fire")
		require.Len(t, repo1.fired, 1)
		assert.True(t, key.Equal(repo1.fired[0].firedForDate))

		// Scrape 2: min=+2 (notMet, same forecast_date) → re-arm, SetWeatherAlertLatched(false).
		ev2, repo2 := runOnce(true, key, 2)
		require.Empty(t, ev2.retained, "scrape 2 must not fire (re-arm only)")
		require.Empty(t, repo2.fired)
		require.Len(t, repo2.latched, 1)
		assert.False(t, repo2.latched[0].latched)

		// Scrape 3: min=-4 (met again, same forecast_date) → latch edge would fire, but
		// the forecast_date gate suppresses it: no event, latch edge recorded, no
		// MarkWeatherAlertFired call.
		ev3, repo3 := runOnce(false, key, -4)
		require.Empty(t, ev3.retained, "scrape 3 must be suppressed by the forecast_date gate")
		require.Empty(t, repo3.fired, "the gate must not record a new fire")
		require.Len(t, repo3.latched, 1, "the latch edge (armed→latched) must still be persisted")
		assert.True(t, repo3.latched[0].latched)

		totalEvents := len(ev1.retained) + len(ev2.retained) + len(ev3.retained)
		assert.Equal(t, 1, totalEvents, "exactly one event must be queued across the three scrapes")
	})

	t.Run("a new forecast_date re-fires: the cap is per-forecast_date, not global", func(t *testing.T) {
		t.Parallel()
		const dayD = "2026-02-05"
		const dayD1 = "2026-02-06"
		keyD, err := domain.ForecastDateKey(dayD)
		require.NoError(t, err)
		keyD1, err := domain.ForecastDateKey(dayD1)
		require.NoError(t, err)

		baseCity := domain.WeatherUserCity{
			ID: "newday-c", UserType: domain.UserTypeTelegram, UserID: "u-newday",
			LocationID: "loc-newday", DisplayName: "NewDayCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
		}
		runOnce := func(latched bool, lastNotified time.Time, forecastDate string, tempMin float64) (*mockCheckEventRepository, *mockWeatherCheckCityRepo) {
			city := baseCity
			city.AlertLatched = latched
			city.LastNotifiedAt = lastNotified
			lower := tempMin
			obs := &domain.WeatherObservation{
				Provider: domain.ProviderOpenMeteo, LocationID: "loc-newday",
				TempMin: &lower, ForecastDate: forecastDate,
			}
			cityRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertFrost: {city},
			}}
			obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: obs}}
			eventRepo := &mockCheckEventRepository{}
			a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
			require.NoError(t, a.Run(t.Context()))
			return eventRepo, cityRepo
		}

		// Row already latched from day D, still met on day D+1: no edge (still latched),
		// so no fire even though the forecast_date changed.
		evNoEdge, repoNoEdge := runOnce(true, keyD, dayD1, -1)
		require.Empty(t, evNoEdge.retained, "no latch edge occurred; still latched from day D")
		require.Empty(t, repoNoEdge.fired)
		require.Empty(t, repoNoEdge.latched)

		// The row re-arms on day D+1 (condition clears)...
		evRearm, repoRearm := runOnce(true, keyD, dayD1, 2)
		require.Empty(t, evRearm.retained)
		require.Len(t, repoRearm.latched, 1)
		assert.False(t, repoRearm.latched[0].latched)

		// ...then meets again on day D+1: a fresh edge, and the day D fire cursor does
		// not gate it (different forecast_date), so it fires and records key(D+1).
		evFire, repoFire := runOnce(false, keyD, dayD1, -1)
		require.Len(t, evFire.retained, 1, "day D+1 is a new forecast_date: the cap must not carry over")
		require.Len(t, repoFire.fired, 1)
		assert.True(t, keyD1.Equal(repoFire.fired[0].firedForDate))
	})

	t.Run("re-arm without notification persists the latch alone", func(t *testing.T) {
		t.Parallel()
		tempMin := 3.0 // above 0 threshold: condition clears
		frostCity := domain.WeatherUserCity{
			ID: "rearm-c", UserType: domain.UserTypeTelegram, UserID: "u-rearm",
			LocationID: "loc-rearm", DisplayName: "RearmCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
			AlertLatched: true, // was fired
		}
		obs := &domain.WeatherObservation{Provider: domain.ProviderOpenMeteo, LocationID: "loc-rearm", TempMin: &tempMin}
		cityRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
			domain.WeatherNotifyAlertFrost: {frostCity},
		}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: obs}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "a re-arm must not notify")
		require.Empty(t, cityRepo.fired)
		require.Len(t, cityRepo.latched, 1)
		assert.Equal(t, "rearm-c", cityRepo.latched[0].id)
		assert.False(t, cityRepo.latched[0].latched)
	})

	t.Run("a data gap does not re-arm a latched row", func(t *testing.T) {
		t.Parallel()
		frostCity := domain.WeatherUserCity{
			ID: "gap-c", UserType: domain.UserTypeTelegram, UserID: "u-gap",
			LocationID: "loc-gap", DisplayName: "GapCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
			AlertLatched: true,
		}
		obs := &domain.WeatherObservation{Provider: domain.ProviderOpenMeteo, LocationID: "loc-gap", TempMin: nil}
		cityRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
			domain.WeatherNotifyAlertFrost: {frostCity},
		}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: obs}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained)
		require.Empty(t, cityRepo.fired)
		require.Empty(t, cityRepo.latched, "a data gap must preserve the latch, not persist a change")
	})

	t.Run("queue failure persists neither the latch nor the fire cursor", func(t *testing.T) {
		t.Parallel()
		tempMin := -4.0
		frostCity := domain.WeatherUserCity{
			ID: "qfail-c", UserType: domain.UserTypeTelegram, UserID: "u-qfail",
			LocationID: "loc-qfail", DisplayName: "QFailCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
		}
		obs := &domain.WeatherObservation{
			Provider: domain.ProviderOpenMeteo, LocationID: "loc-qfail",
			TempMin: &tempMin, ForecastDate: "2026-02-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
			domain.WeatherNotifyAlertFrost: {frostCity},
		}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: obs}}
		eventRepo := &mockCheckEventRepository{err: errors.New("queue write fail")}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		err := a.Run(t.Context())
		require.Error(t, err)
		require.Empty(t, cityRepo.fired, "a queue failure must not mark the alert fired")
		require.Empty(t, cityRepo.latched, "a queue failure must not persist the latch either")
	})

	t.Run("an unparseable forecast_date still fires via the latch-only fallback", func(t *testing.T) {
		t.Parallel()
		tempMin := -4.0
		frostCity := domain.WeatherUserCity{
			ID: "badfd-c", UserType: domain.UserTypeTelegram, UserID: "u-badfd",
			LocationID: "loc-badfd", DisplayName: "BadForecastDateCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertFrost, ConditionValue: "0",
		}
		obs := &domain.WeatherObservation{
			Provider: domain.ProviderOpenMeteo, LocationID: "loc-badfd",
			TempMin: &tempMin, ForecastDate: "", // malformed/empty
		}
		cityRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
			domain.WeatherNotifyAlertFrost: {frostCity},
		}}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{domain.ProviderOpenMeteo: obs}}
		eventRepo := &mockCheckEventRepository{}
		var logBuf strings.Builder

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: &logBuf}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "an unparseable forecast_date must never drop the alert")
		require.Empty(t, cityRepo.fired, "the fire cursor cannot be recorded without a valid forecast_date")
		require.Len(t, cityRepo.latched, 1, "the latch-only fallback must still be persisted")
		assert.Equal(t, "badfd-c", cityRepo.latched[0].id)
		assert.True(t, cityRepo.latched[0].latched)
		assert.Contains(t, logBuf.String(), "unparseable forecast_date")
	})
}

// latchCall records one SetWeatherAlertLatched call.
// latchCall and firedCall record calls for assertion. They are not mocks — they
// implement no interface — and were named mock* until R1's check reasonably read the
// prefix as a promise of a compile-time assertion there is nothing to assert.
type latchCall struct {
	id      string
	latched bool
}

// firedCall records one MarkWeatherAlertFired call.
type firedCall struct {
	id           string
	firedForDate time.Time
}

// mockWeatherCheckCityRepo simulates ObtainDueWeatherUserCities, AdvanceLastNotifiedAt,
// SetWeatherAlertLatched, and MarkWeatherAlertFired. cities is returned for
// morning_summary lookups (backward compatible with existing subtests). citiesByKind
// allows alert subtests to configure per-kind return values; when set it takes
// precedence over cities for every kind.
type mockWeatherCheckCityRepo struct {
	cities       []domain.WeatherUserCity
	citiesByKind map[domain.WeatherNotifyKind][]domain.WeatherUserCity
	err          error
	// errByKind fails only the listed kinds, so a test can break one phase and leave
	// the other working — which is the whole subject of #75.
	errByKind map[domain.WeatherNotifyKind]error
	advanced  []string    // IDs passed to AdvanceLastNotifiedAt
	latched   []latchCall // SetWeatherAlertLatched calls, in call order
	fired     []firedCall // MarkWeatherAlertFired calls, in call order
	states    []stateCall // SetWeatherNotifyState calls, in call order
	stateErr  error       // when set, every SetWeatherNotifyState call fails
}

func (m *mockWeatherCheckCityRepo) ObtainDueWeatherUserCities(_ context.Context, kind domain.WeatherNotifyKind) ([]domain.WeatherUserCity, error) {
	if m.err != nil {
		return nil, m.err
	}
	if kindErr, ok := m.errByKind[kind]; ok {
		return nil, kindErr
	}
	if m.citiesByKind != nil {
		if cities, ok := m.citiesByKind[kind]; ok {
			return cities, nil
		}
		return []domain.WeatherUserCity{}, nil
	}
	// Default: return m.cities only for morning_summary so existing subtests are
	// unaffected by the alert phase (alert kinds return empty, no EvaluateAlert called).
	if kind == domain.WeatherNotifyMorningSummary {
		return m.cities, nil
	}
	return []domain.WeatherUserCity{}, nil
}

func (m *mockWeatherCheckCityRepo) AdvanceLastNotifiedAt(_ context.Context, id string, _ time.Time) error {
	m.advanced = append(m.advanced, id)
	return nil
}

func (m *mockWeatherCheckCityRepo) SetWeatherAlertLatched(_ context.Context, id string, latched bool) error {
	m.latched = append(m.latched, latchCall{id: id, latched: latched})
	return nil
}

func (m *mockWeatherCheckCityRepo) SetWeatherNotifyState(_ context.Context, id, state string) error {
	if m.stateErr != nil {
		return m.stateErr
	}
	m.states = append(m.states, stateCall{id: id, state: state})
	return nil
}

func (m *mockWeatherCheckCityRepo) MarkWeatherAlertFired(_ context.Context, id string, firedForDate time.Time) error {
	m.fired = append(m.fired, firedCall{id: id, firedForDate: firedForDate})
	return nil
}

// mockWeatherCheckObsRepo simulates ObtainLatestObservation for the check agent.
// Priority of lookups:
//  1. obsErrByLocation[locationID] — per-location error regardless of provider.
//  2. obsByProvider[provider] — provider-keyed observation.
//  3. globalErr — returned when none of the above match.
type mockWeatherCheckObsRepo struct {
	obsByProvider    map[string]*domain.WeatherObservation
	obsErrByLocation map[string]error
	globalErr        error // fallback when no entry found
}

func (m *mockWeatherCheckObsRepo) ObtainLatestObservation(_ context.Context, locationID, provider string) (*domain.WeatherObservation, error) {
	if m.obsErrByLocation != nil {
		if err, ok := m.obsErrByLocation[locationID]; ok {
			return nil, err
		}
	}
	if m.obsByProvider != nil {
		if obs, ok := m.obsByProvider[provider]; ok {
			cp := *obs
			return &cp, nil
		}
	}
	if m.globalErr != nil {
		return nil, m.globalErr
	}
	return nil, internal.ErrNotFound
}

// mockCountingObsRepo wraps a single observation and counts how many times
// ObtainLatestObservation is called for the Open-Meteo provider, so tests can
// verify the per-run observation cache prevents redundant DB reads.
type mockCountingObsRepo struct {
	obs   *domain.WeatherObservation
	count *int
}

var _ weatherCheckObsRepository = (*mockCountingObsRepo)(nil)

func (m *mockCountingObsRepo) ObtainLatestObservation(_ context.Context, _, provider string) (*domain.WeatherObservation, error) {
	if provider == domain.ProviderOpenMeteo {
		*m.count++
		if m.obs != nil {
			cp := *m.obs
			return &cp, nil
		}
		return nil, internal.ErrNotFound
	}
	// Any other provider token is not queried by the check agent.
	return nil, internal.ErrNotFound
}

// TestWeatherCheckAgent_RainBothEdges covers what issue #3 reported broken: the rain
// alert used to notify only on the way into the condition, and the per-forecast_date fire
// cap made a second notification on the same day impossible. Both directions must now
// reach the queue, within one forecast_date, and neither may touch the fire cursor.
// TestWeatherCheckAgent_MorningFailureDoesNotSuppressAlerts is the regression guard for
// #75. Run used to return on the first ObtainDueWeatherUserCities error, so a fault in the
// morning-summary read silently cancelled heat, frost, thunderstorm, rain and thaw for the
// whole tick — two phases that share no query and no state.
func TestWeatherCheckAgent_MorningFailureDoesNotSuppressAlerts(t *testing.T) {
	t.Parallel()

	morningErr := errors.New("morning read interrupted")

	newStormFixture := func() (*mockWeatherCheckCityRepo, *mockWeatherCheckObsRepo, *mockCheckEventRepository) {
		stormCode := 95 // thunderstorm band
		stormCity := domain.WeatherUserCity{
			ID:           "storm-c1",
			UserType:     domain.UserTypeTelegram,
			UserID:       "user-storm",
			LocationID:   "loc-storm",
			DisplayName:  "Storm City",
			Timezone:     "UTC",
			NotifyKind:   domain.WeatherNotifyAlertThunderstorm,
			AlertLatched: false, // armed
		}
		obs := &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   "loc-storm",
			WeatherCode:  &stormCode,
			ForecastDate: "2026-01-10",
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertThunderstorm: {stormCity},
			},
			errByKind: map[domain.WeatherNotifyKind]error{
				domain.WeatherNotifyMorningSummary: morningErr,
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: obs,
		}}
		return cityRepo, obsRepo, &mockCheckEventRepository{}
	}

	t.Run("the alert phase still runs and queues", func(t *testing.T) {
		t.Parallel()
		cityRepo, obsRepo, eventRepo := newStormFixture()

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		err := a.Run(t.Context())

		require.Error(t, err, "the morning failure must still be reported")
		require.ErrorIs(t, err, morningErr)
		require.Len(t, eventRepo.retained, 1,
			"a thunderstorm alert must fire even though the morning-summary read failed")
		require.Len(t, cityRepo.fired, 1)
		assert.Equal(t, "storm-c1", cityRepo.fired[0].id)
	})

	t.Run("a failure in each phase is reported, not just the first", func(t *testing.T) {
		t.Parallel()
		cityRepo, obsRepo, eventRepo := newStormFixture()
		frostErr := errors.New("frost read interrupted")
		cityRepo.errByKind[domain.WeatherNotifyAlertFrost] = frostErr

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		err := a.Run(t.Context())

		require.Error(t, err)
		require.ErrorIs(t, err, morningErr, "the morning failure must survive the join")
		require.ErrorIs(t, err, frostErr, "the alert-phase failure must survive the join")
		require.Len(t, eventRepo.retained, 1,
			"the kinds that did load must still fire")
	})
}

func TestWeatherCheckAgent_RainBothEdges(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	rainCityWith := func(id string, latched bool) domain.WeatherUserCity {
		return domain.WeatherUserCity{
			ID: id, UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-" + id, DisplayName: "RainCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
			AlertLatched: latched,
		}
	}
	obsWith := func(locationID string, prob int) *domain.WeatherObservation {
		p := prob
		return &domain.WeatherObservation{
			Provider:     domain.ProviderOpenMeteo,
			LocationID:   locationID,
			Hourly:       []domain.WeatherHourlyPoint{{Time: now.Add(time.Hour), PrecipProb: &p}},
			ForecastDate: "2026-08-04",
		}
	}

	t.Run("cleared edge queues a message and unlatches the row", func(t *testing.T) {
		t.Parallel()
		city := rainCityWith("rain-clear", true)
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {city},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: obsWith(city.LocationID, 20),
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))

		require.Len(t, eventRepo.retained, 1, "leaving the rain condition must notify, not just re-arm silently")
		assert.Contains(t, eventRepo.retained[0].Message, "Rain cleared")
		assert.Equal(t, "u-rain", eventRepo.retained[0].UserID)

		require.Len(t, cityRepo.latched, 1)
		assert.Equal(t, "rain-clear", cityRepo.latched[0].id)
		assert.False(t, cityRepo.latched[0].latched, "the clear edge must persist alert_latched = false")
		assert.Empty(t, cityRepo.fired, "rain_alert must never write the forecast_date fire cursor")
	})

	t.Run("both edges fire within one forecast_date", func(t *testing.T) {
		t.Parallel()
		// A row that already fired today: LastNotifiedAt holds this observation's
		// forecast_date, which is exactly what the cap keys on for the daily-metric kinds.
		firedToday, err := domain.ForecastDateKey("2026-08-04")
		require.NoError(t, err)

		city := rainCityWith("rain-both", true)
		city.LastNotifiedAt = firedToday
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {city},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: obsWith(city.LocationID, 10),
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))

		require.Len(t, eventRepo.retained, 1,
			"a same-forecast_date cursor must not suppress the rain clear message: that cap is for daily-metric kinds")
		assert.Contains(t, eventRepo.retained[0].Message, "Rain cleared")
	})

	t.Run("dead band emits nothing and writes nothing", func(t *testing.T) {
		t.Parallel()
		city := rainCityWith("rain-band", true)
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {city},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: obsWith(city.LocationID, 60), // between 50 and 70
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))

		assert.Empty(t, eventRepo.retained, "the hysteresis band must not produce a message")
		assert.Empty(t, cityRepo.latched, "an unchanged latch must cost zero writes")
		assert.Empty(t, cityRepo.fired)
	})

	t.Run("queue failure on the cleared edge leaves the latch set for a retry", func(t *testing.T) {
		t.Parallel()
		city := rainCityWith("rain-retry", true)
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertRain: {city},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: obsWith(city.LocationID, 5),
		}}
		eventRepo := &mockCheckEventRepository{err: errors.New("queue down")}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		err := a.Run(t.Context())
		require.Error(t, err)

		assert.Empty(t, cityRepo.latched,
			"a failed enqueue must not persist the unlatch, or the clear message would be lost forever")
		assert.Empty(t, cityRepo.fired)
	})

	t.Run("daily-metric kinds stay capped to one notification per forecast_date", func(t *testing.T) {
		t.Parallel()
		firedToday, err := domain.ForecastDateKey("2026-08-04")
		require.NoError(t, err)

		tempMax := 38.0
		heat := domain.WeatherUserCity{
			ID: "heat-capped", UserType: domain.UserTypeTelegram, UserID: "u-heat",
			LocationID: "loc-heat", DisplayName: "HeatCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertHeat, ConditionValue: "35",
			AlertLatched: false, LastNotifiedAt: firedToday,
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertHeat: {heat},
			},
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: {
				Provider: domain.ProviderOpenMeteo, LocationID: "loc-heat",
				TempMax: &tempMax, ForecastDate: "2026-08-04",
			},
		}}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, forecastRepo: &mockWeatherCheckForecastRepo{}, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))

		assert.Empty(t, eventRepo.retained, "the forecast_date cap must still hold for heat")
		require.Len(t, cityRepo.latched, 1, "the suppressed latch edge is still recorded")
		assert.True(t, cityRepo.latched[0].latched)
	})
}

// stateCall records one SetWeatherNotifyState invocation.
type stateCall struct {
	id    string
	state string
}

// mockWeatherCheckForecastRepo simulates the long-range forecast repository. days is keyed
// by location_id; a location absent from the map has nothing stored, which is the normal
// state of a city whose first long-range fetch has not completed.
type mockWeatherCheckForecastRepo struct {
	days map[string][]domain.WeatherForecastDay
	err  error
}

func (m *mockWeatherCheckForecastRepo) ObtainForecastDays(_ context.Context, locationID, _, fromDate string, limit int) ([]domain.WeatherForecastDay, error) {
	if m.err != nil {
		return nil, m.err
	}
	stored := m.days[locationID]
	out := make([]domain.WeatherForecastDay, 0, len(stored))
	for _, d := range stored {
		if d.ForecastDate >= fromDate && len(out) < limit {
			out = append(out, d)
		}
	}
	return out, nil
}

func TestWeatherCheckAgent_OutlookPhase(t *testing.T) {
	t.Parallel()

	// outlookCity returns a forecast_outlook subscription that IsMorningDue always
	// evaluates true for: UTC, NotifyHour 0, never notified.
	outlookCity := func(id, locationID, state string) domain.WeatherUserCity {
		return domain.WeatherUserCity{
			ID:          id,
			UserType:    domain.UserTypeTelegram,
			UserID:      "user1",
			LocationID:  locationID,
			DisplayName: "Astana",
			Timezone:    "UTC",
			NotifyKind:  domain.WeatherNotifyForecastOutlook,
			NotifyHour:  0,
			NotifyState: state,
		}
	}

	// window returns a stored forecast anchored on today: a dry baseline plus the given
	// offsets-from-today, each carrying the rain in millimetres named for it.
	window := func(rainByOffset map[int]float64) []domain.WeatherForecastDay {
		base := time.Now().UTC()
		days := make([]domain.WeatherForecastDay, 0, domain.WeatherOutlookHorizonDays)
		for offset := range domain.WeatherOutlookHorizonDays {
			maxTemp, minTemp := 21.5, 11.2
			day := domain.WeatherForecastDay{
				Provider:     domain.ProviderOpenMeteo,
				ForecastDate: base.AddDate(0, 0, offset).Format(time.DateOnly),
				CapturedAt:   base,
				TempMax:      &maxTemp,
				TempMin:      &minTemp,
			}
			if rain, ok := rainByOffset[offset]; ok {
				r := rain
				day.RainSum = &r
			}
			days = append(days, day)
		}
		return days
	}

	agentFor := func(cityRepo *mockWeatherCheckCityRepo, forecastRepo *mockWeatherCheckForecastRepo, eventRepo *mockCheckEventRepository) *WeatherCheckAgent {
		return &WeatherCheckAgent{
			cityRepo:     cityRepo,
			obsRepo:      &mockWeatherCheckObsRepo{},
			forecastRepo: forecastRepo,
			eventRepo:    eventRepo,
			logger:       io.Discard,
		}
	}

	outlookOnly := func(cities ...domain.WeatherUserCity) *mockWeatherCheckCityRepo {
		return &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
			domain.WeatherNotifyForecastOutlook: cities,
		}}
	}

	t.Run("a first digest with something to report is queued and both cursors move", func(t *testing.T) {
		t.Parallel()
		cityRepo := outlookOnly(outlookCity("o1", "loc1", ""))
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
			"loc1": window(map[int]float64{3: 4.2}),
		}}
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context()))

		require.Len(t, eventRepo.retained, 1)
		assert.Contains(t, eventRepo.retained[0].Message, "Outlook — Astana")
		assert.Contains(t, eventRepo.retained[0].Message, "4.2 mm")
		assert.NotContains(t, eventRepo.retained[0].Message, "🆕", "nothing is marked new when there is no previous digest")
		require.Len(t, cityRepo.states, 1)
		assert.Equal(t, "o1", cityRepo.states[0].id)
		assert.NotEmpty(t, cityRepo.states[0].state)
		assert.Equal(t, []string{"o1"}, cityRepo.advanced)
	})

	t.Run("a first evaluation with nothing to report stays silent but records the state", func(t *testing.T) {
		t.Parallel()
		cityRepo := outlookOnly(outlookCity("o1", "loc1", ""))
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
			"loc1": window(nil),
		}}
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context()))

		assert.Empty(t, eventRepo.retained, "an opening message that says nothing is noise")
		require.Len(t, cityRepo.states, 1)
		assert.Equal(t, "o1:", cityRepo.states[0].state, "an evaluated-but-empty outlook is not the same as never evaluated")
		assert.Equal(t, []string{"o1"}, cityRepo.advanced)
	})

	t.Run("an unchanged outlook sends nothing and still advances the cursor", func(t *testing.T) {
		t.Parallel()
		days := window(map[int]float64{3: 4.2})
		unchanged := domain.NewWeatherOutlook(days, time.Now().UTC().Format(time.DateOnly)).Signature()

		cityRepo := outlookOnly(outlookCity("o1", "loc1", unchanged))
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{"loc1": days}}
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context()))

		assert.Empty(t, eventRepo.retained)
		assert.Empty(t, cityRepo.states, "an unchanged signature needs no write")
		assert.Equal(t, []string{"o1"}, cityRepo.advanced, "the cursor still moves, so the digest stays once a day")
	})

	t.Run("a changed outlook marks what moved", func(t *testing.T) {
		t.Parallel()
		before := domain.NewWeatherOutlook(window(map[int]float64{3: 4.2}), time.Now().UTC().Format(time.DateOnly)).Signature()

		cityRepo := outlookOnly(outlookCity("o1", "loc1", before))
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
			"loc1": window(map[int]float64{3: 4.2, 7: 2.0}),
		}}
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context()))

		require.Len(t, eventRepo.retained, 1)
		assert.Contains(t, eventRepo.retained[0].Message, "🆕", "the day that appeared must be marked")
		assert.Equal(t, 1, strings.Count(eventRepo.retained[0].Message, "🆕"), "the day that did not change must not be")
		require.Len(t, cityRepo.states, 1)
		assert.NotEqual(t, before, cityRepo.states[0].state)
	})

	t.Run("an outlook that empties out reports what cleared", func(t *testing.T) {
		t.Parallel()
		before := domain.NewWeatherOutlook(window(map[int]float64{3: 4.2}), time.Now().UTC().Format(time.DateOnly)).Signature()

		cityRepo := outlookOnly(outlookCity("o1", "loc1", before))
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{"loc1": window(nil)}}
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context()))

		require.Len(t, eventRepo.retained, 1, "an outlook clearing is a change worth reporting")
		assert.Contains(t, eventRepo.retained[0].Message, "No rain, snow or freezing change")
		require.Len(t, cityRepo.states, 1)
		assert.Equal(t, "o1:", cityRepo.states[0].state)
	})

	t.Run("a location with no forecast yet is skipped without advancing", func(t *testing.T) {
		t.Parallel()
		cityRepo := outlookOnly(outlookCity("o1", "loc1", ""))
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, &mockWeatherCheckForecastRepo{}, eventRepo).Run(t.Context()))

		assert.Empty(t, eventRepo.retained)
		assert.Empty(t, cityRepo.states)
		assert.Empty(t, cityRepo.advanced, "the first digest must still fire once collection catches up")
	})

	t.Run("a city already notified today is not evaluated again", func(t *testing.T) {
		t.Parallel()
		city := outlookCity("o1", "loc1", "")
		city.LastNotifiedAt = time.Now().UTC()

		cityRepo := outlookOnly(city)
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
			"loc1": window(map[int]float64{3: 4.2}),
		}}
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context()))

		assert.Empty(t, eventRepo.retained, "one digest per city per local day, whatever the tick rate")
		assert.Empty(t, cityRepo.advanced)
	})

	t.Run("a queue failure leaves both cursors untouched so the next tick retries", func(t *testing.T) {
		t.Parallel()
		cityRepo := outlookOnly(outlookCity("o1", "loc1", ""))
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
			"loc1": window(map[int]float64{3: 4.2}),
		}}
		eventRepo := &mockCheckEventRepository{err: errors.New("pool is down")}

		err := agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context())

		require.Error(t, err)
		assert.Empty(t, cityRepo.states)
		assert.Empty(t, cityRepo.advanced)
	})

	t.Run("a forecast read failure is reported and does not advance", func(t *testing.T) {
		t.Parallel()
		cityRepo := outlookOnly(outlookCity("o1", "loc1", ""))
		forecastRepo := &mockWeatherCheckForecastRepo{err: errors.New("database is locked")}
		eventRepo := &mockCheckEventRepository{}

		err := agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "load forecast")
		assert.Empty(t, cityRepo.advanced)
	})

	t.Run("an unloadable timezone skips the city without failing the run", func(t *testing.T) {
		t.Parallel()
		city := outlookCity("o1", "loc1", "")
		city.Timezone = "Mars/Olympus_Mons"

		cityRepo := outlookOnly(city)
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
			"loc1": window(map[int]float64{3: 4.2}),
		}}
		eventRepo := &mockCheckEventRepository{}

		require.NoError(t, agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context()))
		assert.Empty(t, eventRepo.retained)
	})

	t.Run("the outlook phase runs even when the morning-summary read fails", func(t *testing.T) {
		t.Parallel()
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyForecastOutlook: {outlookCity("o1", "loc1", "")},
			},
			errByKind: map[domain.WeatherNotifyKind]error{
				domain.WeatherNotifyMorningSummary: errors.New("morning read failed"),
			},
		}
		forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
			"loc1": window(map[int]float64{3: 4.2}),
		}}
		eventRepo := &mockCheckEventRepository{}

		err := agentFor(cityRepo, forecastRepo, eventRepo).Run(t.Context())

		require.Error(t, err, "the morning failure must still be reported")
		require.Len(t, eventRepo.retained, 1, "one phase failing must not silence another")
	})
}

func TestWeatherCheckAgentOutlookDrainedWindow(t *testing.T) {
	t.Parallel()

	// A window that holds only the baseline day is what a location looks like after the
	// collector has been down long enough for retention to drain the front of it.
	city := domain.WeatherUserCity{
		ID:          "o1",
		UserType:    domain.UserTypeTelegram,
		UserID:      "user1",
		LocationID:  "loc1",
		DisplayName: "Astana",
		Timezone:    "UTC",
		NotifyKind:  domain.WeatherNotifyForecastOutlook,
		NotifyHour:  0,
		NotifyState: "o1:2026-08-23:R+",
	}
	cityRepo := &mockWeatherCheckCityRepo{citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
		domain.WeatherNotifyForecastOutlook: {city},
	}}
	forecastRepo := &mockWeatherCheckForecastRepo{days: map[string][]domain.WeatherForecastDay{
		"loc1": {{ForecastDate: time.Now().UTC().Format(time.DateOnly), Provider: domain.ProviderOpenMeteo}},
	}}
	eventRepo := &mockCheckEventRepository{}

	a := &WeatherCheckAgent{
		cityRepo:     cityRepo,
		obsRepo:      &mockWeatherCheckObsRepo{},
		forecastRepo: forecastRepo,
		eventRepo:    eventRepo,
		logger:       io.Discard,
	}
	require.NoError(t, a.Run(t.Context()))

	assert.Empty(t, eventRepo.retained, "a window with no future day is not an outlook that cleared")
	assert.Empty(t, cityRepo.states)
	assert.Empty(t, cityRepo.advanced, "the digest must resume when collection does")
}
