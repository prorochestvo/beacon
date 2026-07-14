package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherObservationRepository_RetainWeatherObservation(t *testing.T) {
	t.Parallel()

	t.Run("inserts and round-trips all non-nullable fields", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		capturedAt := time.Now().UTC().Truncate(time.Second)
		obs := &domain.WeatherObservation{
			LocationID:   "loc1",
			Provider:     "open-meteo",
			Latitude:     43.2525,
			Longitude:    76.9115,
			CapturedAt:   capturedAt,
			ForecastDate: "2026-06-30",
		}
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), obs))
		require.NotEmpty(t, obs.ID)

		got, err := repo.ObtainLatestObservation(t.Context(), "loc1", "open-meteo")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, obs.ID, got.ID)
		assert.Equal(t, "loc1", got.LocationID)
		assert.Equal(t, "open-meteo", got.Provider)
		assert.InDelta(t, 43.2525, got.Latitude, 1e-4)
		assert.InDelta(t, 76.9115, got.Longitude, 1e-4)
		assert.Equal(t, capturedAt.Format(time.RFC3339), got.CapturedAt.Format(time.RFC3339))
		assert.Equal(t, "2026-06-30", got.ForecastDate)
		assert.Nil(t, got.TempMax)
		assert.Nil(t, got.Sunrise)
	})

	t.Run("round-trips nullable forecast fields", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		tempMax := 31.6
		tempMin := 20.8
		precipSum := 1.1
		precipProb := 69
		wcode := 53
		humidity := 61
		windSpeed := 1.7
		windDir := 212
		tempCurrent := 21.3
		tempFeels := 22.1
		prec := 0.0
		cloud := 4
		sunriseT := time.Date(2026, 6, 30, 4, 15, 0, 0, time.UTC)
		sunsetT := time.Date(2026, 6, 30, 19, 36, 0, 0, time.UTC)

		obs := &domain.WeatherObservation{
			LocationID:    "loc2",
			Provider:      "open-meteo",
			Latitude:      43.25,
			Longitude:     76.91,
			CapturedAt:    time.Now().UTC().Truncate(time.Second),
			ForecastDate:  "2026-06-30",
			TempMax:       &tempMax,
			TempMin:       &tempMin,
			PrecipSum:     &precipSum,
			PrecipProbMax: &precipProb,
			WeatherCode:   &wcode,
			Sunrise:       &sunriseT,
			Sunset:        &sunsetT,
			TempCurrent:   &tempCurrent,
			TempFeels:     &tempFeels,
			Humidity:      &humidity,
			WindSpeed:     &windSpeed,
			WindDir:       &windDir,
			Precip:        &prec,
			CloudCover:    &cloud,
		}
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), obs))

		got, err := repo.ObtainLatestObservation(t.Context(), "loc2", "open-meteo")
		require.NoError(t, err)
		require.NotNil(t, got.TempMax)
		assert.InDelta(t, tempMax, *got.TempMax, 1e-4)
		require.NotNil(t, got.TempMin)
		assert.InDelta(t, tempMin, *got.TempMin, 1e-4)
		require.NotNil(t, got.PrecipProbMax)
		assert.Equal(t, precipProb, *got.PrecipProbMax)
		require.NotNil(t, got.WeatherCode)
		assert.Equal(t, wcode, *got.WeatherCode)
		require.NotNil(t, got.Sunrise)
		assert.Equal(t, sunriseT.Format(time.RFC3339), got.Sunrise.Format(time.RFC3339))
		require.NotNil(t, got.Sunset)
		assert.Equal(t, sunsetT.Format(time.RFC3339), got.Sunset.Format(time.RFC3339))
		require.NotNil(t, got.TempCurrent)
		assert.InDelta(t, tempCurrent, *got.TempCurrent, 1e-4)
		require.NotNil(t, got.Humidity)
		assert.Equal(t, humidity, *got.Humidity)

		// precip=0.0 is a real value, not NULL — confirm it round-trips as non-nil.
		require.NotNil(t, got.Precip)
		assert.InDelta(t, prec, *got.Precip, 1e-6)
	})

	t.Run("non-nil Hourly round-trips via hourly_json", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		prob := 75
		temp := 28.5
		ts1 := time.Date(2026, 6, 30, 7, 0, 0, 0, time.UTC)
		ts2 := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)

		obs := &domain.WeatherObservation{
			LocationID:   "loc-hourly",
			Provider:     "open-meteo",
			CapturedAt:   time.Now().UTC().Truncate(time.Second),
			ForecastDate: "2026-06-30",
			Hourly: []domain.WeatherHourlyPoint{
				{Time: ts1, PrecipProb: &prob, Temp: &temp},
				{Time: ts2, PrecipProb: nil},
			},
		}
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), obs))

		got, err := repo.ObtainLatestObservation(t.Context(), "loc-hourly", "open-meteo")
		require.NoError(t, err)
		require.Len(t, got.Hourly, 2)

		assert.Equal(t, ts1.UTC(), got.Hourly[0].Time.UTC())
		require.NotNil(t, got.Hourly[0].PrecipProb)
		assert.Equal(t, 75, *got.Hourly[0].PrecipProb)
		require.NotNil(t, got.Hourly[0].Temp)
		assert.InDelta(t, 28.5, *got.Hourly[0].Temp, 1e-4)

		assert.Equal(t, ts2.UTC(), got.Hourly[1].Time.UTC())
		assert.Nil(t, got.Hourly[1].PrecipProb, "nil PrecipProb must survive the round-trip")
	})

	t.Run("nil Hourly stores NULL and reads back empty non-nil slice", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		obs := &domain.WeatherObservation{
			LocationID:   "loc-no-hourly",
			Provider:     "open-meteo",
			CapturedAt:   time.Now().UTC().Truncate(time.Second),
			ForecastDate: "2026-06-30",
			Hourly:       nil,
		}
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), obs))

		got, err := repo.ObtainLatestObservation(t.Context(), "loc-no-hourly", "open-meteo")
		require.NoError(t, err)
		assert.NotNil(t, got.Hourly, "Hourly must be non-nil so callers can use len() without a nil guard")
		assert.Empty(t, got.Hourly)
	})

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)
		require.Error(t, repo.RetainWeatherObservation(t.Context(), nil))
	})
}

func TestWeatherObservationRepository_ObtainLatestObservation(t *testing.T) {
	t.Parallel()

	t.Run("returns ErrNotFound when no row exists", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		got, err := repo.ObtainLatestObservation(t.Context(), "missing", "open-meteo")
		require.Nil(t, got)
		require.True(t, errors.Is(err, internal.ErrNotFound))
	})

	t.Run("returns most recent observation by captured_at", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		base := time.Now().UTC().Truncate(time.Second)
		older := &domain.WeatherObservation{
			LocationID:   "loc-latest",
			Provider:     "open-meteo",
			CapturedAt:   base.Add(-2 * time.Hour),
			ForecastDate: "2026-06-30",
		}
		newer := &domain.WeatherObservation{
			LocationID:   "loc-latest",
			Provider:     "open-meteo",
			CapturedAt:   base,
			ForecastDate: "2026-06-30",
		}
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), older))
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), newer))

		got, err := repo.ObtainLatestObservation(t.Context(), "loc-latest", "open-meteo")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, newer.ID, got.ID, "must return the most recent observation")
	})

	t.Run("provider isolation: open-meteo and gismeteo are independent", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		for _, prov := range []string{"open-meteo", "gismeteo"} {
			require.NoError(t, repo.RetainWeatherObservation(t.Context(), &domain.WeatherObservation{
				LocationID:   "loc-prov",
				Provider:     prov,
				CapturedAt:   time.Now().UTC(),
				ForecastDate: "2026-06-30",
			}))
		}

		got, err := repo.ObtainLatestObservation(t.Context(), "loc-prov", "gismeteo")
		require.NoError(t, err)
		assert.Equal(t, "gismeteo", got.Provider)
	})
}

func TestWeatherObservationRepository_RemoveWeatherObservationsOlderThan(t *testing.T) {
	t.Parallel()

	t.Run("removes old observations, keeps recent ones", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)

		now := time.Now().UTC().Truncate(time.Second)
		old := &domain.WeatherObservation{
			LocationID:   "loc-vacuum",
			Provider:     "open-meteo",
			CapturedAt:   now.Add(-48 * time.Hour),
			ForecastDate: "2026-06-28",
		}
		fresh := &domain.WeatherObservation{
			LocationID:   "loc-vacuum",
			Provider:     "open-meteo",
			CapturedAt:   now,
			ForecastDate: "2026-06-30",
		}
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), old))
		require.NoError(t, repo.RetainWeatherObservation(t.Context(), fresh))

		require.NoError(t, repo.RemoveWeatherObservationsOlderThan(t.Context(), 24*time.Hour))

		got, err := repo.ObtainLatestObservation(t.Context(), "loc-vacuum", "open-meteo")
		require.NoError(t, err)
		assert.Equal(t, fresh.ID, got.ID, "fresh observation must survive vacuum")
	})

	t.Run("vacuum on empty table is a no-op", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)
		require.NoError(t, repo.RemoveWeatherObservationsOlderThan(t.Context(), 24*time.Hour))
	})
}

func TestWeatherObservationRepository_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on migrated db", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherObservationRepository(db)
		require.NoError(t, err)
		require.NoError(t, repo.CheckUP(t.Context()))
	})

	t.Run("fails on db error", func(t *testing.T) {
		t.Parallel()
		repo := &WeatherObservationRepository{db: &mockFailDB{err: errors.New("db unavailable")}}
		require.Error(t, repo.CheckUP(t.Context()))
	})
}
