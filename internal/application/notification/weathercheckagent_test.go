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

// Compile-time assertions that the concrete repository types satisfy the interfaces.
var _ weatherCheckCityRepository = &repository.WeatherUserCityRepository{}
var _ weatherCheckObsRepository = &repository.WeatherObservationRepository{}

func TestNewWeatherCheckAgent(t *testing.T) {
	t.Parallel()

	t.Run("valid construction succeeds", func(t *testing.T) {
		t.Parallel()
		a, err := NewWeatherCheckAgent(
			&mockWeatherCheckCityRepo{},
			&mockWeatherCheckObsRepo{},
			&mockCheckEventRepository{},
			io.Discard,
		)
		require.NoError(t, err)
		require.NotNil(t, a)
	})

	t.Run("nil cityRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherCheckAgent(nil, &mockWeatherCheckObsRepo{}, &mockCheckEventRepository{}, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil obsRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherCheckAgent(&mockWeatherCheckCityRepo{}, nil, &mockCheckEventRepository{}, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil eventRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherCheckAgent(&mockWeatherCheckCityRepo{}, &mockWeatherCheckObsRepo{}, nil, io.Discard)
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
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))

		require.Len(t, eventRepo.retained, 1)
		ev := eventRepo.retained[0]
		assert.NotEmpty(t, ev.Message, "queued event must have a non-empty message")
		assert.Equal(t, "", ev.SourceName, "SourceName must be empty so it stores as NULL")
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
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
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
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
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
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    &logBuf,
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
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
		}
		err := a.Run(t.Context())
		require.Error(t, err)
		require.Empty(t, cityRepo.advanced, "advance must not be called after a retain failure")
	})

	t.Run("city repo error is returned immediately", func(t *testing.T) {
		t.Parallel()
		a := &WeatherCheckAgent{
			cityRepo:  &mockWeatherCheckCityRepo{err: errors.New("db down")},
			obsRepo:   &mockWeatherCheckObsRepo{},
			eventRepo: &mockCheckEventRepository{},
			logger:    io.Discard,
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
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
		}
		err := a.Run(t.Context())
		require.Error(t, err, "joined error must contain the failing location")
		assert.Len(t, eventRepo.retained, 1, "city2 must still be queued")
		assert.Len(t, cityRepo.advanced, 1, "only city2 must be advanced")
		assert.Equal(t, "c2", cityRepo.advanced[0])
	})

	// Gismeteo cross-provider subtests.

	t.Run("due city with fresh gismeteo observation renders both providers", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		gismeteoObs := &domain.WeatherObservation{
			Provider:     domain.ProviderGismeteo,
			LocationID:   "loc1",
			ForecastDate: today,
			CapturedAt:   time.Now().UTC().Add(-1 * time.Hour), // recent
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
			domain.ProviderGismeteo:  gismeteoObs,
		}}
		eventRepo := &mockCheckEventRepository{}
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}

		a := &WeatherCheckAgent{
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		assert.Contains(t, eventRepo.retained[0].Message, "Gismeteo",
			"message must contain the Gismeteo provider label when a fresh gismeteo observation is available")
	})

	t.Run("due city with only Open-Meteo renders single-provider summary", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
			// no gismeteo entry → ErrNotFound for gismeteo lookup
		}}
		eventRepo := &mockCheckEventRepository{}
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}

		a := &WeatherCheckAgent{
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		assert.NotContains(t, eventRepo.retained[0].Message, "Gismeteo",
			"message must not mention Gismeteo when no gismeteo observation exists")
	})

	t.Run("stale gismeteo observation (old forecast_date) yields single-provider summary", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
		staleGismeteoObs := &domain.WeatherObservation{
			Provider:     domain.ProviderGismeteo,
			LocationID:   "loc1",
			ForecastDate: yesterday, // yesterday's date → not fresh for today's summary
			CapturedAt:   time.Now().UTC().Add(-2 * time.Hour),
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
			domain.ProviderGismeteo:  staleGismeteoObs,
		}}
		eventRepo := &mockCheckEventRepository{}
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}

		a := &WeatherCheckAgent{
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		assert.NotContains(t, eventRepo.retained[0].Message, "Gismeteo",
			"stale gismeteo (wrong forecast_date) must not appear in summary")
	})

	t.Run("gismeteo observation beyond CapturedAt age limit yields single-provider summary", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		tooOldGismeteoObs := &domain.WeatherObservation{
			Provider:     domain.ProviderGismeteo,
			LocationID:   "loc1",
			ForecastDate: today,                                 // same date
			CapturedAt:   time.Now().UTC().Add(-25 * time.Hour), // older than 24 h limit
		}
		obsRepo := &mockWeatherCheckObsRepo{obsByProvider: map[string]*domain.WeatherObservation{
			domain.ProviderOpenMeteo: goodObs,
			domain.ProviderGismeteo:  tooOldGismeteoObs,
		}}
		eventRepo := &mockCheckEventRepository{}
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}

		a := &WeatherCheckAgent{
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		assert.NotContains(t, eventRepo.retained[0].Message, "Gismeteo",
			"a gismeteo observation older than weatherGismeteoMaxAge must be treated as absent")
	})

	t.Run("non-ErrNotFound gismeteo error is logged but does not block Open-Meteo summary", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		gismeteoErr := errors.New("gismeteo db corrupted")
		obsRepo := &mockWeatherCheckObsRepo{
			obsByProvider: map[string]*domain.WeatherObservation{
				domain.ProviderOpenMeteo: goodObs,
			},
			gismeteoErr: gismeteoErr, // returned for gismeteo lookup only
		}
		eventRepo := &mockCheckEventRepository{}
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}
		var logBuf strings.Builder

		a := &WeatherCheckAgent{
			cityRepo:  cityRepo,
			obsRepo:   obsRepo,
			eventRepo: eventRepo,
			logger:    &logBuf,
		}
		// The Run must NOT return an error: gismeteo never blocks the summary.
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "Open-Meteo summary must still be queued")
		assert.NotContains(t, eventRepo.retained[0].Message, "Gismeteo",
			"message must not mention Gismeteo when lookup errored")
		assert.Contains(t, logBuf.String(), "gismeteo",
			"error must be logged for observability")
	})

	// Alert phase subtests.

	t.Run("alert_heat: condition met with no prior cooldown queues event and advances", func(t *testing.T) {
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
			LastNotifiedAt: time.Time{}, // never alerted
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "one heat alert event must be queued")
		assert.Contains(t, eventRepo.retained[0].Message, "Heat alert")
		require.Len(t, cityRepo.advanced, 1, "last_notified_at must be advanced after queuing")
		assert.Equal(t, "alert-c1", cityRepo.advanced[0])
	})

	t.Run("alert_heat: within cooldown window suppresses re-alert", func(t *testing.T) {
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
			LastNotifiedAt: time.Now().UTC().Add(-1 * time.Hour), // alerted 1 h ago (< 20 h cooldown)
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "alert within cooldown must be suppressed")
		require.Empty(t, cityRepo.advanced, "last_notified_at must not advance when suppressed by cooldown")
	})

	t.Run("alert_heat: condition not met produces no event", func(t *testing.T) {
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
			LastNotifiedAt: time.Time{},
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when condition is not met")
		require.Empty(t, cityRepo.advanced)
	})

	t.Run("alert: no observation for location skips without advancing", func(t *testing.T) {
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
			LastNotifiedAt: time.Time{},
		}
		cityRepo := &mockWeatherCheckCityRepo{
			citiesByKind: map[domain.WeatherNotifyKind][]domain.WeatherUserCity{
				domain.WeatherNotifyAlertFrost: {alertCity},
			},
		}
		// globalErr = ErrNotFound → no observation for any location
		obsRepo := &mockWeatherCheckObsRepo{globalErr: internal.ErrNotFound}
		eventRepo := &mockCheckEventRepository{}

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when observation is absent")
		require.Empty(t, cityRepo.advanced, "must not advance when observation absent")
	})

	t.Run("alert: observation is cached per location_id across multiple alert kinds", func(t *testing.T) {
		t.Parallel()
		tempMax := 36.0
		tempMin := -2.0
		alertObs := &domain.WeatherObservation{
			Provider:   domain.ProviderOpenMeteo,
			LocationID: "loc-shared",
			TempMax:    &tempMax,
			TempMin:    &tempMin,
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

	t.Run("alert_thunderstorm: fires when weather code in thunderstorm band", func(t *testing.T) {
		t.Parallel()
		code := 95
		alertCity := domain.WeatherUserCity{
			ID: "thunder-c", UserType: domain.UserTypeTelegram, UserID: "u1",
			LocationID: "loc-storm", DisplayName: "StormCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertThunderstorm,
		}
		alertObs := &domain.WeatherObservation{
			Provider:    domain.ProviderOpenMeteo,
			LocationID:  "loc-storm",
			WeatherCode: &code,
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1)
		assert.Contains(t, eventRepo.retained[0].Message, "Thunderstorm alert")
		require.Len(t, cityRepo.advanced, 1)
	})

	t.Run("rain_alert: condition met past cooldown queues event and advances", func(t *testing.T) {
		t.Parallel()
		// Hourly point 1 h from now falls within the 6 h window for any real clock value.
		now := time.Now().UTC()
		prob := 85
		rainCity := domain.WeatherUserCity{
			ID: "rain-c1", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain1", DisplayName: "RainCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
			LastNotifiedAt: time.Time{},
		}
		rainObs := &domain.WeatherObservation{
			Provider:   domain.ProviderOpenMeteo,
			LocationID: "loc-rain1",
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Len(t, eventRepo.retained, 1, "one rain alert event must be queued")
		assert.Contains(t, eventRepo.retained[0].Message, "Rain alert")
		require.Len(t, cityRepo.advanced, 1)
		assert.Equal(t, "rain-c1", cityRepo.advanced[0])
	})

	t.Run("rain_alert: within cooldown window suppresses re-alert", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		prob := 85
		rainCity := domain.WeatherUserCity{
			ID: "rain-c2", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain2", DisplayName: "RainCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
			LastNotifiedAt: now.Add(-1 * time.Hour), // alerted 1 h ago, cooldown is 6 h
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "rain alert within 6 h cooldown must be suppressed")
		require.Empty(t, cityRepo.advanced)
	})

	t.Run("rain_alert: probability below threshold produces no event", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		prob := 50 // below 70% threshold
		rainCity := domain.WeatherUserCity{
			ID: "rain-c3", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain3", DisplayName: "DrizzleCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
			LastNotifiedAt: time.Time{},
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when probability below threshold")
		require.Empty(t, cityRepo.advanced)
	})

	t.Run("rain_alert: no hourly data skips without advancing", func(t *testing.T) {
		t.Parallel()
		rainCity := domain.WeatherUserCity{
			ID: "rain-c4", UserType: domain.UserTypeTelegram, UserID: "u-rain",
			LocationID: "loc-rain4", DisplayName: "NoDataCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "70",
			LastNotifiedAt: time.Time{},
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		require.NoError(t, a.Run(t.Context()))
		require.Empty(t, eventRepo.retained, "no event when hourly data is absent")
		require.Empty(t, cityRepo.advanced, "last_notified_at must not advance when condition not met")
	})

	t.Run("evaluator error on bad condition_value skips bad city, good city still fires", func(t *testing.T) {
		t.Parallel()
		tempMax := 40.0 // above 35 threshold for good city
		badCity := domain.WeatherUserCity{
			ID: "bad-c", UserType: domain.UserTypeTelegram, UserID: "u-bad",
			LocationID: "loc-bad", DisplayName: "BadCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertHeat, ConditionValue: "notanumber",
			LastNotifiedAt: time.Time{},
		}
		goodCity := domain.WeatherUserCity{
			ID: "good-c", UserType: domain.UserTypeTelegram, UserID: "u-good",
			LocationID: "loc-good", DisplayName: "GoodCity", Timezone: "UTC",
			NotifyKind: domain.WeatherNotifyAlertHeat, ConditionValue: "35",
			LastNotifiedAt: time.Time{},
		}
		sharedObs := &domain.WeatherObservation{
			Provider: domain.ProviderOpenMeteo,
			TempMax:  &tempMax,
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

		a := &WeatherCheckAgent{cityRepo: cityRepo, obsRepo: obsRepo, eventRepo: eventRepo, logger: io.Discard}
		err := a.Run(t.Context())
		require.Error(t, err, "evaluator error for bad city must surface in the returned error")
		assert.Contains(t, err.Error(), "evaluate", "error must reference the evaluate step")
		require.Len(t, eventRepo.retained, 1, "good city must still queue an event")
		assert.Equal(t, "u-good", eventRepo.retained[0].UserID, "queued event must be for the good city's user")
		// bad city must not be advanced
		for _, id := range cityRepo.advanced {
			assert.NotEqual(t, "bad-c", id, "bad city must not be advanced after evaluator failure")
		}
		// good city must be advanced
		require.Len(t, cityRepo.advanced, 1)
		assert.Equal(t, "good-c", cityRepo.advanced[0])
	})
}

// mockWeatherCheckCityRepo simulates ObtainDueWeatherUserCities and AdvanceLastNotifiedAt.
// cities is returned for morning_summary lookups (backward compatible with existing
// subtests). citiesByKind allows alert subtests to configure per-kind return values;
// when set it takes precedence over cities for every kind.
type mockWeatherCheckCityRepo struct {
	cities       []domain.WeatherUserCity
	citiesByKind map[domain.WeatherNotifyKind][]domain.WeatherUserCity
	err          error
	advanced     []string // IDs passed to AdvanceLastNotifiedAt
}

func (m *mockWeatherCheckCityRepo) ObtainDueWeatherUserCities(_ context.Context, kind domain.WeatherNotifyKind) ([]domain.WeatherUserCity, error) {
	if m.err != nil {
		return nil, m.err
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

// mockWeatherCheckObsRepo simulates ObtainLatestObservation for the check agent.
// Priority of lookups:
//  1. obsErrByLocation[locationID] — per-location error regardless of provider.
//  2. gismeteoErr — returned for any gismeteo lookup when set.
//  3. obsByProvider[provider] — provider-keyed observation.
//  4. globalErr — returned when none of the above match.
type mockWeatherCheckObsRepo struct {
	obsByProvider    map[string]*domain.WeatherObservation
	obsErrByLocation map[string]error
	gismeteoErr      error // non-nil → returned for gismeteo lookups only
	globalErr        error // fallback when no entry found
}

func (m *mockWeatherCheckObsRepo) ObtainLatestObservation(_ context.Context, locationID, provider string) (*domain.WeatherObservation, error) {
	if m.obsErrByLocation != nil {
		if err, ok := m.obsErrByLocation[locationID]; ok {
			return nil, err
		}
	}
	if provider == domain.ProviderGismeteo && m.gismeteoErr != nil {
		return nil, m.gismeteoErr
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
	// Non-Open-Meteo providers (gismeteo) are not queried in the alert phase.
	return nil, internal.ErrNotFound
}
