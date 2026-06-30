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

	tempMax := 25.0
	tempMin := 15.0
	goodObs := &domain.WeatherObservation{
		Provider:   "open-meteo",
		LocationID: "loc1",
		TempMax:    &tempMax,
		TempMin:    &tempMin,
	}

	t.Run("due city with observation queues one event and advances", func(t *testing.T) {
		t.Parallel()
		city := dueCity("c1", "user1", "loc1")
		cityRepo := &mockWeatherCheckCityRepo{cities: []domain.WeatherUserCity{city}}
		obsRepo := &mockWeatherCheckObsRepo{obs: goodObs}
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
		obsRepo := &mockWeatherCheckObsRepo{obs: goodObs}
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
		obsRepo := &mockWeatherCheckObsRepo{obsErr: internal.ErrNotFound}
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
		obsRepo := &mockWeatherCheckObsRepo{obs: goodObs}
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
		obsRepo := &mockWeatherCheckObsRepo{obs: goodObs}
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

	t.Run("one city fails render, other cities still processed", func(t *testing.T) {
		t.Parallel()
		// The observation repo is configured to return an error for loc1 and succeed
		// for loc2. The per-location obs-load error is accumulated (not fatal) so loc2
		// still gets its event queued and last_notified_at advanced.
		city1 := dueCity("c1", "user1", "loc1")
		city2 := dueCity("c2", "user2", "loc2")
		// obs repo returns error for loc1, ok for loc2
		obsRepo := &mockWeatherCheckObsRepo{obsErrByLocation: map[string]error{
			"loc1": errors.New("transient fail"),
		}, obs: goodObs}
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
}

// mockWeatherCheckCityRepo simulates ObtainDueWeatherUserCities and AdvanceLastNotifiedAt.
type mockWeatherCheckCityRepo struct {
	cities   []domain.WeatherUserCity
	err      error
	advanced []string // IDs passed to AdvanceLastNotifiedAt
}

func (m *mockWeatherCheckCityRepo) ObtainDueWeatherUserCities(_ context.Context, _ domain.WeatherNotifyKind) ([]domain.WeatherUserCity, error) {
	return m.cities, m.err
}

func (m *mockWeatherCheckCityRepo) AdvanceLastNotifiedAt(_ context.Context, id string, _ time.Time) error {
	m.advanced = append(m.advanced, id)
	return nil
}

// mockWeatherCheckObsRepo simulates ObtainLatestObservation for the check agent.
type mockWeatherCheckObsRepo struct {
	obs              *domain.WeatherObservation
	obsErr           error
	obsErrByLocation map[string]error
}

func (m *mockWeatherCheckObsRepo) ObtainLatestObservation(_ context.Context, locationID, _ string) (*domain.WeatherObservation, error) {
	if m.obsErrByLocation != nil {
		if err, ok := m.obsErrByLocation[locationID]; ok {
			return nil, err
		}
	}
	if m.obsErr != nil {
		return nil, m.obsErr
	}
	if m.obs == nil {
		return nil, internal.ErrNotFound
	}
	cp := *m.obs
	return &cp, nil
}
