package collection

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

var _ weatherRangeProvider = (*mockWeatherRangeProvider)(nil)
var _ weatherForecastDayRepo = (*mockWeatherForecastDayRepo)(nil)

// Compile-time assertion that the concrete repository satisfies the narrow interface.
var _ weatherForecastDayRepo = &repository.WeatherForecastDayRepository{}

func TestNewWeatherForecastAgent(t *testing.T) {
	t.Parallel()

	t.Run("valid construction", func(t *testing.T) {
		t.Parallel()
		a, err := NewWeatherForecastAgent(&mockWeatherRangeProvider{}, &mockWeatherCityRepo{}, &mockWeatherForecastDayRepo{}, io.Discard)
		require.NoError(t, err)
		require.NotNil(t, a)
	})

	t.Run("nil provider returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherForecastAgent(nil, &mockWeatherCityRepo{}, &mockWeatherForecastDayRepo{}, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil cityRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherForecastAgent(&mockWeatherRangeProvider{}, nil, &mockWeatherForecastDayRepo{}, io.Discard)
		require.Error(t, err)
	})

	t.Run("nil dayRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherForecastAgent(&mockWeatherRangeProvider{}, &mockWeatherCityRepo{}, nil, io.Discard)
		require.Error(t, err)
	})
}

func TestWeatherForecastAgent_Run(t *testing.T) {
	t.Parallel()

	t.Run("no locations means no fetch", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{}, &mockWeatherForecastDayRepo{})
		require.NoError(t, a.Run(t.Context()))
		assert.Zero(t, provider.calls)
	})

	t.Run("a location never fetched is fetched and stored under its location key", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{days: []domain.WeatherForecastDay{
			{ForecastDate: "2026-08-21"}, {ForecastDate: "2026-08-22"},
		}}
		dayRepo := &mockWeatherForecastDayRepo{captureErr: internal.ErrNotFound}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{locations: locations("loc1")}, dayRepo)

		require.NoError(t, a.Run(t.Context()))
		assert.Equal(t, 1, provider.calls)
		require.Len(t, dayRepo.retained, 2)
		assert.Equal(t, "loc1", dayRepo.retained[0].LocationID)
		assert.Equal(t, "loc1", dayRepo.retained[1].LocationID)
	})

	t.Run("a location already fetched today is skipped", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{days: []domain.WeatherForecastDay{{ForecastDate: "2026-08-21"}}}
		dayRepo := &mockWeatherForecastDayRepo{capture: time.Now().UTC()}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{locations: locations("loc1")}, dayRepo)

		require.NoError(t, a.Run(t.Context()))
		assert.Zero(t, provider.calls, "one fetch per UTC day is the whole point of the gate")
		assert.Empty(t, dayRepo.retained)
	})

	t.Run("a location last fetched on an earlier UTC day is due again", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{days: []domain.WeatherForecastDay{{ForecastDate: "2026-08-21"}}}
		dayRepo := &mockWeatherForecastDayRepo{capture: time.Now().UTC().AddDate(0, 0, -1)}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{locations: locations("loc1")}, dayRepo)

		require.NoError(t, a.Run(t.Context()))
		assert.Equal(t, 1, provider.calls)
	})

	t.Run("a capture read failure counts as due rather than skipping the location forever", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{days: []domain.WeatherForecastDay{{ForecastDate: "2026-08-21"}}}
		dayRepo := &mockWeatherForecastDayRepo{captureErr: errors.New("database is busy")}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{locations: locations("loc1")}, dayRepo)

		require.NoError(t, a.Run(t.Context()))
		assert.Equal(t, 1, provider.calls)
	})

	t.Run("one failing location does not stop the others", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{
			days:      []domain.WeatherForecastDay{{ForecastDate: "2026-08-21"}},
			failOnLat: 2,
		}
		dayRepo := &mockWeatherForecastDayRepo{captureErr: internal.ErrNotFound}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{locations: locations("loc1", "loc2", "loc3")}, dayRepo)

		err := a.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loc2", "the joined error must name the location that failed")
		require.Len(t, dayRepo.retained, 2, "the other two locations must still be stored")
	})

	t.Run("a retain failure is reported and does not stop the run", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{days: []domain.WeatherForecastDay{{ForecastDate: "2026-08-21"}}}
		dayRepo := &mockWeatherForecastDayRepo{captureErr: internal.ErrNotFound, retainErr: errors.New("disk full")}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{locations: locations("loc1", "loc2")}, dayRepo)

		err := a.Run(t.Context())
		require.Error(t, err)
		assert.Equal(t, 2, provider.calls)
	})

	t.Run("retention runs with a day of slack, even when every fetch was skipped", func(t *testing.T) {
		t.Parallel()
		dayRepo := &mockWeatherForecastDayRepo{capture: time.Now().UTC()}
		a := newForecastAgent(t, &mockWeatherRangeProvider{}, &mockWeatherCityRepo{locations: locations("loc1")}, dayRepo)

		require.NoError(t, a.Run(t.Context()))
		require.Len(t, dayRepo.prunedBefore, 1)
		want := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
		assert.Equal(t, want, dayRepo.prunedBefore[0], "yesterday is kept so no city's local today is deleted early")
	})

	t.Run("a retention failure is reported, not swallowed", func(t *testing.T) {
		t.Parallel()
		dayRepo := &mockWeatherForecastDayRepo{capture: time.Now().UTC(), pruneErr: errors.New("locked")}
		a := newForecastAgent(t, &mockWeatherRangeProvider{}, &mockWeatherCityRepo{locations: locations("loc1")}, dayRepo)

		err := a.Run(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retention")
	})

	t.Run("a city-repository failure aborts before any fetch", func(t *testing.T) {
		t.Parallel()
		provider := &mockWeatherRangeProvider{}
		a := newForecastAgent(t, provider, &mockWeatherCityRepo{err: errors.New("no database")}, &mockWeatherForecastDayRepo{})

		require.Error(t, a.Run(t.Context()))
		assert.Zero(t, provider.calls)
	})

	t.Run("logs a proof-of-execution line", func(t *testing.T) {
		t.Parallel()
		var log strings.Builder
		dayRepo := &mockWeatherForecastDayRepo{captureErr: internal.ErrNotFound}
		a, err := NewWeatherForecastAgent(
			&mockWeatherRangeProvider{days: []domain.WeatherForecastDay{{ForecastDate: "2026-08-21"}}},
			&mockWeatherCityRepo{locations: locations("loc1")},
			dayRepo,
			&log,
		)
		require.NoError(t, err)
		require.NoError(t, a.Run(t.Context()))
		assert.Contains(t, log.String(), "weather forecast: fetched=1 skipped=0 failed=0 total=1")
	})
}

// locations builds the distinct-location rows the collector iterates, one per id, each with
// distinct coordinates so a provider stub can fail a chosen one.
func locations(ids ...string) []domain.WeatherUserCity {
	out := make([]domain.WeatherUserCity, 0, len(ids))
	for i, id := range ids {
		out = append(out, domain.WeatherUserCity{
			LocationID: id,
			Latitude:   float64(i + 1),
			Longitude:  float64(i + 1),
		})
	}
	return out
}

// newForecastAgent constructs an agent with a discarding logger.
func newForecastAgent(t *testing.T, provider weatherRangeProvider, cityRepo weatherCollectionCityRepo, dayRepo weatherForecastDayRepo) *WeatherForecastAgent {
	t.Helper()
	a, err := NewWeatherForecastAgent(provider, cityRepo, dayRepo, io.Discard)
	require.NoError(t, err)
	return a
}

// mockWeatherRangeProvider simulates the long-range Open-Meteo endpoint. failOnLat names the
// latitude whose fetch fails, so a test can single out one location of several.
type mockWeatherRangeProvider struct {
	days      []domain.WeatherForecastDay
	calls     int
	failOnLat float64
}

func (m *mockWeatherRangeProvider) ForecastRange(_ context.Context, lat, _ float64) ([]domain.WeatherForecastDay, error) {
	m.calls++
	if m.failOnLat != 0 && lat == m.failOnLat {
		return nil, errors.New("upstream refused")
	}
	// A copy per call: the agent stamps LocationID onto the returned slice, and two
	// locations sharing one backing array would each overwrite the other's key.
	out := make([]domain.WeatherForecastDay, len(m.days))
	copy(out, m.days)
	return out, nil
}

// mockWeatherForecastDayRepo simulates the forecast-day repository for the collector.
type mockWeatherForecastDayRepo struct {
	capture      time.Time
	captureErr   error
	retained     []domain.WeatherForecastDay
	retainErr    error
	prunedBefore []string
	pruneErr     error
}

func (m *mockWeatherForecastDayRepo) ObtainLatestForecastCapture(_ context.Context, _, _ string) (time.Time, error) {
	return m.capture, m.captureErr
}

func (m *mockWeatherForecastDayRepo) RemoveForecastDaysBefore(_ context.Context, date string) error {
	m.prunedBefore = append(m.prunedBefore, date)
	return m.pruneErr
}

func (m *mockWeatherForecastDayRepo) RetainWeatherForecastDays(_ context.Context, records []domain.WeatherForecastDay) error {
	if m.retainErr != nil {
		return m.retainErr
	}
	m.retained = append(m.retained, records...)
	return nil
}
