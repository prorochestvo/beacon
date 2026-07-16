package collection

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/stretchr/testify/require"
)

var _ weatherForecastProvider = (*mockWeatherProvider)(nil)
var _ weatherCollectionCityRepo = (*mockWeatherCityRepo)(nil)
var _ weatherCollectionObsRepo = (*mockWeatherObsRepo)(nil)

// Compile-time assertions that the concrete repository types satisfy the interfaces.
var _ weatherCollectionCityRepo = &repository.WeatherUserCityRepository{}
var _ weatherCollectionObsRepo = &repository.WeatherObservationRepository{}

func TestNewWeatherAgent(t *testing.T) {
	t.Parallel()

	t.Run("valid construction uses defaults", func(t *testing.T) {
		t.Parallel()
		a, err := NewWeatherAgent(
			&mockWeatherProvider{},
			&mockWeatherCityRepo{},
			&mockWeatherObsRepo{},
			0,
			io.Discard,
		)
		require.NoError(t, err)
		require.NotNil(t, a)
		require.Equal(t, DefaultWeatherThrottleInterval, a.throttleInterval)
	})

	t.Run("nil provider returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherAgent(nil, &mockWeatherCityRepo{}, &mockWeatherObsRepo{}, 0, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil cityRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherAgent(&mockWeatherProvider{}, nil, &mockWeatherObsRepo{}, 0, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil obsRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherAgent(&mockWeatherProvider{}, &mockWeatherCityRepo{}, nil, 0, io.Discard)
		require.Error(t, err)
	})
}

func TestWeatherAgent_Run(t *testing.T) {
	t.Parallel()

	t.Run("zero locations — no-op, no error", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherProvider{}
		a := &WeatherAgent{
			provider:         provider,
			cityRepo:         &mockWeatherCityRepo{locations: nil},
			obsRepo:          &mockWeatherObsRepo{},
			throttleInterval: DefaultWeatherThrottleInterval,
			logger:           io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Zero(t, provider.calls, "provider must not be called when there are no locations")
	})

	t.Run("one location stores one observation", func(t *testing.T) {
		t.Parallel()
		obsRepo := &mockWeatherObsRepo{latestErr: internal.ErrNotFound}
		provider := &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: "open-meteo"}}
		a := &WeatherAgent{
			provider:         provider,
			cityRepo:         &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:          obsRepo,
			throttleInterval: DefaultWeatherThrottleInterval,
			logger:           io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 1, provider.calls)
		require.Len(t, obsRepo.retained, 1)
		require.Equal(t, "loc1", obsRepo.retained[0].LocationID, "location_id must be set on the stored observation")
	})

	t.Run("provider error on one location is joined, others still stored", func(t *testing.T) {
		t.Parallel()
		obsRepo := &mockWeatherObsRepo{latestErr: internal.ErrNotFound}
		// provider fails for loc1, succeeds for loc2
		provider := &mockWeatherProvider{errByLocation: map[string]error{
			// key by call-order index: first call (loc1) → error, second call (loc2) → nil
		}}
		provider.errSeq = []error{errors.New("provider down"), nil}
		a := &WeatherAgent{
			provider: provider,
			cityRepo: &mockWeatherCityRepo{locations: []domain.WeatherUserCity{
				{LocationID: "loc1", Latitude: 1.0, Longitude: 1.0},
				{LocationID: "loc2", Latitude: 2.0, Longitude: 2.0},
			}},
			obsRepo:          obsRepo,
			throttleInterval: DefaultWeatherThrottleInterval,
			logger:           io.Discard,
		}
		err := a.Run(t.Context())
		require.Error(t, err, "joined error must be returned for the failing location")
		require.ErrorContains(t, err, "location loc1")
		// loc2 should still be stored
		require.Len(t, obsRepo.retained, 1, "successful location must still be stored")
		require.Equal(t, "loc2", obsRepo.retained[0].LocationID)
	})

	t.Run("throttle skips location with fresh observation", func(t *testing.T) {
		t.Parallel()
		freshObs := &domain.WeatherObservation{
			CapturedAt: time.Now().UTC().Add(-30 * time.Minute), // 30 min ago
		}
		obsRepo := &mockWeatherObsRepo{latest: freshObs}
		provider := &mockWeatherProvider{}
		a := &WeatherAgent{
			provider:         provider,
			cityRepo:         &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:          obsRepo,
			throttleInterval: time.Hour, // 1h throttle; observation is 30 min old → skip
			logger:           io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Zero(t, provider.calls, "provider must not be called for a fresh location")
	})

	t.Run("throttle fetches when observation is stale", func(t *testing.T) {
		t.Parallel()
		staleObs := &domain.WeatherObservation{
			CapturedAt: time.Now().UTC().Add(-2 * time.Hour), // 2 h ago
		}
		obsRepo := &mockWeatherObsRepo{latest: staleObs}
		provider := &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: "open-meteo"}}
		a := &WeatherAgent{
			provider:         provider,
			cityRepo:         &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:          obsRepo,
			throttleInterval: time.Hour, // 1h throttle; observation is 2 h old → fetch
			logger:           io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 1, provider.calls)
	})

	t.Run("city repo error returns error immediately", func(t *testing.T) {
		t.Parallel()
		a := &WeatherAgent{
			provider:         &mockWeatherProvider{},
			cityRepo:         &mockWeatherCityRepo{err: errors.New("db down")},
			obsRepo:          &mockWeatherObsRepo{},
			throttleInterval: DefaultWeatherThrottleInterval,
			logger:           io.Discard,
		}
		require.Error(t, a.Run(t.Context()))
	})
}

// mockWeatherProvider simulates a weather forecast provider.
// errSeq is consumed sequentially; once exhausted all remaining calls return nil error.
type mockWeatherProvider struct {
	obs           *domain.WeatherObservation
	errByLocation map[string]error
	errSeq        []error
	calls         int
}

func (m *mockWeatherProvider) Forecast(_ context.Context, _, _ float64) (*domain.WeatherObservation, error) {
	idx := m.calls
	m.calls++

	if len(m.errSeq) > idx {
		if m.errSeq[idx] != nil {
			return nil, m.errSeq[idx]
		}
	}

	obs := m.obs
	if obs == nil {
		obs = &domain.WeatherObservation{Provider: "open-meteo"}
	}
	// return a copy so tests that mutate LocationID don't corrupt the template
	cp := *obs
	return &cp, nil
}

// mockWeatherCityRepo simulates ObtainDistinctWeatherLocations.
type mockWeatherCityRepo struct {
	locations []domain.WeatherUserCity
	err       error
}

func (m *mockWeatherCityRepo) ObtainDistinctWeatherLocations(_ context.Context) ([]domain.WeatherUserCity, error) {
	return m.locations, m.err
}

// mockWeatherObsRepo simulates the observation repository for the collector.
type mockWeatherObsRepo struct {
	latest    *domain.WeatherObservation
	latestErr error
	retained  []*domain.WeatherObservation
	retainErr error
}

func (m *mockWeatherObsRepo) ObtainLatestObservation(_ context.Context, _, _ string) (*domain.WeatherObservation, error) {
	return m.latest, m.latestErr
}

func (m *mockWeatherObsRepo) RetainWeatherObservation(_ context.Context, record *domain.WeatherObservation) error {
	if m.retainErr != nil {
		return m.retainErr
	}
	cp := *record
	m.retained = append(m.retained, &cp)
	return nil
}
