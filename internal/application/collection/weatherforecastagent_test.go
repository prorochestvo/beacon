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
		a, err := NewWeatherForecastAgent(&mockWeatherRangeProvider{}, &mockWeatherCityRepo{}, &mockWeatherForecastDayRepo{}, newFakeMetaRepo(), io.Discard)
		require.NoError(t, err)
		require.NotNil(t, a)
	})

	t.Run("nil provider returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherForecastAgent(nil, &mockWeatherCityRepo{}, &mockWeatherForecastDayRepo{}, newFakeMetaRepo(), io.Discard)
		require.Error(t, err)
	})

	t.Run("nil cityRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherForecastAgent(&mockWeatherRangeProvider{}, nil, &mockWeatherForecastDayRepo{}, newFakeMetaRepo(), io.Discard)
		require.Error(t, err)
	})

	t.Run("nil dayRepo returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherForecastAgent(&mockWeatherRangeProvider{}, &mockWeatherCityRepo{}, nil, newFakeMetaRepo(), io.Discard)
		require.Error(t, err)
	})

	t.Run("nil meta returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewWeatherForecastAgent(&mockWeatherRangeProvider{}, &mockWeatherCityRepo{}, &mockWeatherForecastDayRepo{}, nil, io.Discard)
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
			newFakeMetaRepo(),
			&log,
		)
		require.NoError(t, err)
		require.NoError(t, a.Run(t.Context()))
		assert.Contains(t, log.String(), "weather forecast: fetched=1 skipped=0 deferred=0 failed=0 total=1")
	})
}

func TestWeatherForecastAgentAttemptBudget(t *testing.T) {
	t.Parallel()

	key := forecastAttemptKey("loc1")

	// agentWith builds an agent over one always-failing location and the given marker state.
	agentWith := func(t *testing.T, meta *fakeMetaRepo, log io.Writer) (*WeatherForecastAgent, *mockWeatherRangeProvider) {
		t.Helper()
		provider := &mockWeatherRangeProvider{failOnLat: 1}
		a, err := NewWeatherForecastAgent(
			provider,
			&mockWeatherCityRepo{locations: locations("loc1")},
			&mockWeatherForecastDayRepo{captureErr: internal.ErrNotFound},
			meta,
			log,
		)
		require.NoError(t, err)
		return a, provider
	}

	t.Run("a failed fetch records the attempt", func(t *testing.T) {
		t.Parallel()
		meta := newFakeMetaRepo()
		a, provider := agentWith(t, meta, io.Discard)

		require.Error(t, a.Run(t.Context()))
		assert.Equal(t, 1, provider.calls)

		marker, err := parseForecastAttempt(meta.values[key])
		require.NoError(t, err, "the failure must leave a parseable marker")
		assert.Equal(t, time.Now().UTC().Format(time.DateOnly), marker.day)
		assert.Equal(t, 1, marker.count)
	})

	t.Run("a retain failure records the attempt too", func(t *testing.T) {
		t.Parallel()
		// The fetch was paid for either way; only the store failed.
		meta := newFakeMetaRepo()
		a, err := NewWeatherForecastAgent(
			&mockWeatherRangeProvider{days: []domain.WeatherForecastDay{{ForecastDate: "2026-08-21"}}},
			&mockWeatherCityRepo{locations: locations("loc1")},
			&mockWeatherForecastDayRepo{captureErr: internal.ErrNotFound, retainErr: errors.New("disk on fire")},
			meta,
			io.Discard,
		)
		require.NoError(t, err)

		require.Error(t, a.Run(t.Context()))
		assert.NotEmpty(t, meta.values[key])
	})

	t.Run("a spent budget defers the location instead of refetching", func(t *testing.T) {
		t.Parallel()
		// The whole point: before this, a location that could not be fetched stayed due and
		// was retried on every tick for the rest of the day.
		var log strings.Builder
		meta := newFakeMetaRepo()
		meta.values[key] = formatForecastAttempt(forecastAttemptMarker{
			day:    time.Now().UTC().Format(time.DateOnly),
			count:  weatherForecastMaxDailyAttempts,
			lastAt: time.Now().UTC().Add(-24 * time.Hour),
		})
		a, provider := agentWith(t, meta, &log)

		require.NoError(t, a.Run(t.Context()))
		assert.Zero(t, provider.calls, "the budget is spent; nothing may reach the provider")
		assert.Contains(t, log.String(), "deferred=1")
		assert.Contains(t, log.String(), "skipped=0", "backing off is not the same state as already fetched")
	})

	t.Run("a marker from another day is a fresh budget", func(t *testing.T) {
		t.Parallel()
		meta := newFakeMetaRepo()
		meta.values[key] = formatForecastAttempt(forecastAttemptMarker{
			day:    time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly),
			count:  weatherForecastMaxDailyAttempts,
			lastAt: time.Now().UTC().Add(-24 * time.Hour),
		})
		a, provider := agentWith(t, meta, io.Discard)

		require.Error(t, a.Run(t.Context()))
		assert.Equal(t, 1, provider.calls)
	})

	t.Run("a marker that cannot be read or parsed never blocks collection", func(t *testing.T) {
		t.Parallel()
		for name, meta := range map[string]*fakeMetaRepo{
			"read fails": {values: map[string]string{}, readErr: errors.New("meta unavailable")},
			"garbage":    {values: map[string]string{key: "last tuesday"}},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				a, provider := agentWith(t, meta, io.Discard)
				require.Error(t, a.Run(t.Context()))
				assert.Equal(t, 1, provider.calls, "bookkeeping must not be what stops a fetch")
			})
		}
	})

	t.Run("today's stored forecast still wins over any marker", func(t *testing.T) {
		t.Parallel()
		meta := newFakeMetaRepo()
		provider := &mockWeatherRangeProvider{}
		a, err := NewWeatherForecastAgent(
			provider,
			&mockWeatherCityRepo{locations: locations("loc1")},
			&mockWeatherForecastDayRepo{capture: time.Now().UTC()},
			meta,
			io.Discard,
		)
		require.NoError(t, err)

		require.NoError(t, a.Run(t.Context()))
		assert.Zero(t, provider.calls)
		assert.Empty(t, meta.values[key], "a location that never failed writes no marker")
	})
}

func TestWeatherForecastAgentRetryWindow(t *testing.T) {
	t.Parallel()

	// retryWindowOpen takes now explicitly, so the timing cases need no clock seam.
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	key := forecastAttemptKey("loc1")

	agentWithMarker := func(t *testing.T, marker forecastAttemptMarker) *WeatherForecastAgent {
		t.Helper()
		meta := newFakeMetaRepo()
		meta.values[key] = formatForecastAttempt(marker)
		a, err := NewWeatherForecastAgent(
			&mockWeatherRangeProvider{}, &mockWeatherCityRepo{}, &mockWeatherForecastDayRepo{}, meta, io.Discard)
		require.NoError(t, err)
		return a
	}

	cases := []struct {
		name  string
		count int
		since time.Duration // how long ago the last attempt was
		open  bool
	}{
		{"one failure, half an hour ago", 1, 30 * time.Minute, false},
		{"one failure, an hour ago", 1, time.Hour, true},
		{"two failures, an hour ago", 2, time.Hour, false},
		{"two failures, two hours ago", 2, 2 * time.Hour, true},
		{"three failures, three hours ago", 3, 3 * time.Hour, false},
		{"three failures, four hours ago", 3, 4 * time.Hour, true},
		{"budget spent, however long ago", weatherForecastMaxDailyAttempts, 24 * time.Hour, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a := agentWithMarker(t, forecastAttemptMarker{
				day:    now.Format(time.DateOnly),
				count:  c.count,
				lastAt: now.Add(-c.since),
			})
			assert.Equal(t, c.open, a.retryWindowOpen(t.Context(), "loc1", now))
		})
	}
}

func TestForecastAttemptMarker(t *testing.T) {
	t.Parallel()

	t.Run("a marker round-trips", func(t *testing.T) {
		t.Parallel()
		want := forecastAttemptMarker{
			day:    "2026-08-23",
			count:  3,
			lastAt: time.Date(2026, 8, 23, 7, 15, 0, 0, time.UTC),
		}
		got, err := parseForecastAttempt(formatForecastAttempt(want))
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	for _, raw := range []string{"", "2026-08-23", "2026-08-23|two|2026-08-23T07:15:00Z", "2026-08-23|1|yesterday"} {
		t.Run("malformed: "+raw, func(t *testing.T) {
			t.Parallel()
			_, err := parseForecastAttempt(raw)
			require.Error(t, err)
		})
	}
}

func TestForecastRetryWait(t *testing.T) {
	t.Parallel()

	// Doubling from the base, so the day's attempts land at roughly 0, 1, 3, 7 and 15 hours.
	assert.Zero(t, forecastRetryWait(0))
	assert.Equal(t, weatherForecastRetryBase, forecastRetryWait(1))
	assert.Equal(t, 2*weatherForecastRetryBase, forecastRetryWait(2))
	assert.Equal(t, 4*weatherForecastRetryBase, forecastRetryWait(3))
	assert.Equal(t, 8*weatherForecastRetryBase, forecastRetryWait(4))
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
	a, err := NewWeatherForecastAgent(provider, cityRepo, dayRepo, newFakeMetaRepo(), io.Discard)
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
