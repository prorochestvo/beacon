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
var _ weatherGismeteoProvider = (*mockGismeteoProvider)(nil)

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
		require.Equal(t, DefaultGismeteoThrottleInterval, a.gismeteoThrottleInterval)
		require.Nil(t, a.gismeteo, "gismeteo must be nil when WithGismeteo option is not passed")
	})

	t.Run("WithGismeteo option wires provider and custom interval", func(t *testing.T) {
		t.Parallel()
		gm := &mockGismeteoProvider{}
		a, err := NewWeatherAgent(
			&mockWeatherProvider{},
			&mockWeatherCityRepo{},
			&mockWeatherObsRepo{},
			0,
			io.Discard,
			WithGismeteo(gm, 2*time.Hour),
		)
		require.NoError(t, err)
		require.Equal(t, gm, a.gismeteo)
		require.Equal(t, 2*time.Hour, a.gismeteoThrottleInterval)
	})

	t.Run("WithGismeteo with nil provider is a no-op", func(t *testing.T) {
		t.Parallel()
		a, err := NewWeatherAgent(
			&mockWeatherProvider{},
			&mockWeatherCityRepo{},
			&mockWeatherObsRepo{},
			0,
			io.Discard,
			WithGismeteo(nil, 0),
		)
		require.NoError(t, err)
		require.Nil(t, a.gismeteo)
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
			provider:                 provider,
			cityRepo:                 &mockWeatherCityRepo{locations: nil},
			obsRepo:                  &mockWeatherObsRepo{},
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Zero(t, provider.calls, "provider must not be called when there are no locations")
	})

	t.Run("one location stores one observation", func(t *testing.T) {
		t.Parallel()
		obsRepo := &mockWeatherObsRepo{latestErr: internal.ErrNotFound}
		provider := &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: "open-meteo"}}
		a := &WeatherAgent{
			provider:                 provider,
			cityRepo:                 &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:                  obsRepo,
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
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
			obsRepo:                  obsRepo,
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
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
			provider:                 provider,
			cityRepo:                 &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:                  obsRepo,
			throttleInterval:         time.Hour, // 1h throttle; observation is 30 min old → skip
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
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
			provider:                 provider,
			cityRepo:                 &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:                  obsRepo,
			throttleInterval:         time.Hour, // 1h throttle; observation is 2 h old → fetch
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 1, provider.calls)
	})

	t.Run("city repo error returns error immediately", func(t *testing.T) {
		t.Parallel()
		a := &WeatherAgent{
			provider:                 &mockWeatherProvider{},
			cityRepo:                 &mockWeatherCityRepo{err: errors.New("db down")},
			obsRepo:                  &mockWeatherObsRepo{},
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
		}
		require.Error(t, a.Run(t.Context()))
	})

	// Gismeteo-specific subtests — exercise the gismeteo phase in isolation.

	t.Run("nil gismeteo provider — behaviour identical to MVP", func(t *testing.T) {
		t.Parallel()
		obsRepo := &mockWeatherObsRepo{latestErr: internal.ErrNotFound}
		provider := &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: domain.ProviderOpenMeteo}}
		a := &WeatherAgent{
			provider:                 provider,
			cityRepo:                 &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:                  obsRepo,
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			gismeteo:                 nil, // gismeteo disabled
			logger:                   io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 1, provider.calls)
		require.Len(t, obsRepo.retained, 1)
		require.Equal(t, domain.ProviderOpenMeteo, obsRepo.retained[0].Provider)
	})

	t.Run("supported and due location stores gismeteo observation", func(t *testing.T) {
		t.Parallel()
		gm := &mockGismeteoProvider{
			supportedIDs: map[string]bool{"loc1": true},
			obs: &domain.WeatherObservation{
				Provider:   domain.ProviderGismeteo,
				LocationID: "loc1",
			},
		}
		obsRepo := &mockWeatherObsRepo{latestErr: internal.ErrNotFound}
		a := &WeatherAgent{
			provider:                 &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: domain.ProviderOpenMeteo}},
			cityRepo:                 &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:                  obsRepo,
			gismeteo:                 gm,
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 1, gm.forecastBatchCalls, "ForecastBatch must be called exactly once")
		// Retained: one Open-Meteo + one gismeteo.
		var gismeteoStored int
		for _, obs := range obsRepo.retained {
			if obs.Provider == domain.ProviderGismeteo {
				gismeteoStored++
				require.Equal(t, "loc1", obs.LocationID)
			}
		}
		require.Equal(t, 1, gismeteoStored, "exactly one gismeteo observation must be stored")
	})

	t.Run("unsupported location skips gismeteo with no ForecastBatch call", func(t *testing.T) {
		t.Parallel()
		gm := &mockGismeteoProvider{
			supportedIDs: map[string]bool{}, // nothing supported
		}
		obsRepo := &mockWeatherObsRepo{latestErr: internal.ErrNotFound}
		a := &WeatherAgent{
			provider:                 &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: domain.ProviderOpenMeteo}},
			cityRepo:                 &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1", Latitude: 43.0, Longitude: 76.0}}},
			obsRepo:                  obsRepo,
			gismeteo:                 gm,
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 0, gm.forecastBatchCalls, "ForecastBatch must not be called for unsupported location")
	})

	t.Run("fresh gismeteo observation throttles the location", func(t *testing.T) {
		t.Parallel()
		fresh := &domain.WeatherObservation{
			Provider:   domain.ProviderGismeteo,
			CapturedAt: time.Now().UTC().Add(-30 * time.Minute),
		}
		gm := &mockGismeteoProvider{supportedIDs: map[string]bool{"loc1": true}}
		obsRepo := &mockWeatherObsRepo{
			latestByProvider: map[string]*domain.WeatherObservation{
				domain.ProviderGismeteo:  fresh,
				domain.ProviderOpenMeteo: {CapturedAt: time.Now().UTC().Add(-30 * time.Minute)},
			},
		}
		a := &WeatherAgent{
			provider:                 &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: domain.ProviderOpenMeteo}},
			cityRepo:                 &mockWeatherCityRepo{locations: []domain.WeatherUserCity{{LocationID: "loc1"}}},
			obsRepo:                  obsRepo,
			gismeteo:                 gm,
			throttleInterval:         time.Minute, // open-meteo due check: not used since provider returns fresh
			gismeteoThrottleInterval: time.Hour,   // gismeteo: 30 min old < 1h → throttled
			logger:                   io.Discard,
		}
		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 0, gm.forecastBatchCalls, "ForecastBatch must not be called when gismeteo observation is fresh")
	})

	t.Run("gismeteo per-location error is joined, other locations still persist", func(t *testing.T) {
		t.Parallel()
		fetchErr := errors.New("gismeteo scrape failed")
		gm := &mockGismeteoProvider{
			supportedIDs: map[string]bool{"loc1": true, "loc2": true},
			errByID:      map[string]error{"loc1": fetchErr},
			obs: &domain.WeatherObservation{
				Provider: domain.ProviderGismeteo,
			},
		}
		obsRepo := &mockWeatherObsRepo{latestErr: internal.ErrNotFound}
		a := &WeatherAgent{
			provider: &mockWeatherProvider{obs: &domain.WeatherObservation{Provider: domain.ProviderOpenMeteo}},
			cityRepo: &mockWeatherCityRepo{locations: []domain.WeatherUserCity{
				{LocationID: "loc1"},
				{LocationID: "loc2"},
			}},
			obsRepo:                  obsRepo,
			gismeteo:                 gm,
			throttleInterval:         DefaultWeatherThrottleInterval,
			gismeteoThrottleInterval: DefaultGismeteoThrottleInterval,
			logger:                   io.Discard,
		}
		err := a.Run(t.Context())
		require.Error(t, err, "joined error must be returned for the failing gismeteo location")
		require.ErrorContains(t, err, "loc1")

		// loc2 gismeteo should still be stored.
		var gismeteoStored int
		for _, obs := range obsRepo.retained {
			if obs.Provider == domain.ProviderGismeteo {
				gismeteoStored++
			}
		}
		require.Equal(t, 1, gismeteoStored, "successful gismeteo location must still be persisted")
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
// When latestByProvider is set, ObtainLatestObservation dispatches by provider;
// otherwise it falls back to latest/latestErr (provider-agnostic).
type mockWeatherObsRepo struct {
	latest           *domain.WeatherObservation
	latestErr        error
	latestByProvider map[string]*domain.WeatherObservation
	retained         []*domain.WeatherObservation
	retainErr        error
}

func (m *mockWeatherObsRepo) ObtainLatestObservation(_ context.Context, _, provider string) (*domain.WeatherObservation, error) {
	if m.latestByProvider != nil {
		if obs, ok := m.latestByProvider[provider]; ok {
			return obs, nil
		}
		return nil, internal.ErrNotFound
	}
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

// mockGismeteoProvider simulates weatherGismeteoProvider for WeatherAgent tests.
type mockGismeteoProvider struct {
	supportedIDs       map[string]bool
	obs                *domain.WeatherObservation
	errByID            map[string]error
	forecastBatchCalls int
}

func (m *mockGismeteoProvider) Supports(locationID string) bool {
	return m.supportedIDs[locationID]
}

func (m *mockGismeteoProvider) ForecastBatch(_ context.Context, locationIDs []string) (map[string]*domain.WeatherObservation, map[string]error) {
	m.forecastBatchCalls++
	obsByLoc := make(map[string]*domain.WeatherObservation)
	errByLoc := make(map[string]error)
	for _, id := range locationIDs {
		if err, ok := m.errByID[id]; ok {
			errByLoc[id] = err
			continue
		}
		obs := m.obs
		if obs == nil {
			obs = &domain.WeatherObservation{Provider: domain.ProviderGismeteo}
		}
		cp := *obs
		cp.LocationID = id
		obsByLoc[id] = &cp
	}
	return obsByLoc, errByLoc
}
