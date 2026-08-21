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

func TestWeatherForecastDayRepository_RetainWeatherForecastDays(t *testing.T) {
	t.Parallel()

	t.Run("inserts a fetch and round-trips every measurement", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		captured := time.Now().UTC().Truncate(time.Second)
		days := []domain.WeatherForecastDay{
			forecastDay("loc1", "2026-08-21", captured, 21.5, 11.2, 0, 0),
			forecastDay("loc1", "2026-08-22", captured, 24.3, 12.6, 1.3, 0),
		}
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), days))
		require.NotEmpty(t, days[0].ID)
		require.NotEmpty(t, days[1].ID)

		got, err := repo.ObtainForecastDays(t.Context(), "loc1", domain.ProviderOpenMeteo, "2026-08-21", 0)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "2026-08-21", got[0].ForecastDate)
		assert.Equal(t, "2026-08-22", got[1].ForecastDate)
		require.NotNil(t, got[1].TempMax)
		assert.InDelta(t, 24.3, *got[1].TempMax, 1e-6)
		require.NotNil(t, got[1].RainSum)
		assert.InDelta(t, 1.3, *got[1].RainSum, 1e-6)
		assert.Equal(t, captured.Format(time.RFC3339), got[0].CapturedAt.Format(time.RFC3339))
	})

	t.Run("stores NULL for absent measurements rather than zero", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), []domain.WeatherForecastDay{{
			LocationID:   "loc1",
			Provider:     domain.ProviderOpenMeteo,
			ForecastDate: "2026-08-21",
			CapturedAt:   time.Now().UTC(),
		}}))

		got, err := repo.ObtainForecastDays(t.Context(), "loc1", domain.ProviderOpenMeteo, "2026-08-21", 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Nil(t, got[0].TempMax)
		assert.Nil(t, got[0].RainSum)
		assert.Nil(t, got[0].SnowfallSum)
		assert.Nil(t, got[0].PrecipProbMax)
		assert.Nil(t, got[0].WeatherCode)
	})

	t.Run("a second fetch of the same day updates in place and keeps the row count", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		first := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
		days := []domain.WeatherForecastDay{forecastDay("loc1", "2026-08-25", first, 20, 10, 0, 0)}
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), days))
		originalID := days[0].ID

		second := time.Now().UTC().Truncate(time.Second)
		revised := []domain.WeatherForecastDay{forecastDay("loc1", "2026-08-25", second, 18, 6, 4.2, 1.5)}
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), revised))

		got, err := repo.ObtainForecastDays(t.Context(), "loc1", domain.ProviderOpenMeteo, "2026-08-25", 0)
		require.NoError(t, err)
		require.Len(t, got, 1, "an upserted day must not accumulate revisions")
		assert.Equal(t, originalID, got[0].ID, "the row keeps the identifier it was created with")
		require.NotNil(t, got[0].RainSum)
		assert.InDelta(t, 4.2, *got[0].RainSum, 1e-6)
		require.NotNil(t, got[0].SnowfallSum)
		assert.InDelta(t, 1.5, *got[0].SnowfallSum, 1e-6)
		assert.Equal(t, second.Format(time.RFC3339), got[0].CapturedAt.Format(time.RFC3339))
	})

	t.Run("an empty fetch is a no-op, not an error", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), nil))
	})
}

func TestWeatherForecastDayRepository_ObtainForecastDays(t *testing.T) {
	t.Parallel()

	t.Run("windows from the requested date and orders ascending", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		captured := time.Now().UTC()
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), []domain.WeatherForecastDay{
			forecastDay("loc1", "2026-08-23", captured, 20, 10, 0, 0),
			forecastDay("loc1", "2026-08-21", captured, 20, 10, 0, 0),
			forecastDay("loc1", "2026-08-22", captured, 20, 10, 0, 0),
		}))

		got, err := repo.ObtainForecastDays(t.Context(), "loc1", domain.ProviderOpenMeteo, "2026-08-22", 0)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "2026-08-22", got[0].ForecastDate)
		assert.Equal(t, "2026-08-23", got[1].ForecastDate)
	})

	t.Run("does not leak another location or another provider", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		captured := time.Now().UTC()
		other := forecastDay("loc2", "2026-08-21", captured, 20, 10, 0, 0)
		foreign := forecastDay("loc1", "2026-08-21", captured, 20, 10, 0, 0)
		foreign.Provider = "some-other-provider"
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), []domain.WeatherForecastDay{
			forecastDay("loc1", "2026-08-21", captured, 20, 10, 0, 0), other, foreign,
		}))

		got, err := repo.ObtainForecastDays(t.Context(), "loc1", domain.ProviderOpenMeteo, "2026-08-21", 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "loc1", got[0].LocationID)
		assert.Equal(t, domain.ProviderOpenMeteo, got[0].Provider)
	})

	t.Run("honours the limit", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		captured := time.Now().UTC()
		stored := []domain.WeatherForecastDay{
			forecastDay("loc1", "2026-08-21", captured, 20, 10, 0, 0),
			forecastDay("loc1", "2026-08-22", captured, 20, 10, 0, 0),
			forecastDay("loc1", "2026-08-23", captured, 20, 10, 0, 0),
		}
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), stored))

		got, err := repo.ObtainForecastDays(t.Context(), "loc1", domain.ProviderOpenMeteo, "2026-08-21", 2)
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("a location with no fetch yet returns an empty slice, not an error", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		got, err := repo.ObtainForecastDays(t.Context(), "nobody", domain.ProviderOpenMeteo, "2026-08-21", 0)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestWeatherForecastDayRepository_ObtainLatestForecastCapture(t *testing.T) {
	t.Parallel()

	t.Run("returns the newest captured_at across the stored window", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		older := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
		newer := time.Now().UTC().Truncate(time.Second)
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), []domain.WeatherForecastDay{
			forecastDay("loc1", "2026-08-21", older, 20, 10, 0, 0),
			forecastDay("loc1", "2026-08-22", newer, 20, 10, 0, 0),
		}))

		got, err := repo.ObtainLatestForecastCapture(t.Context(), "loc1", domain.ProviderOpenMeteo)
		require.NoError(t, err)
		assert.Equal(t, newer.Format(time.RFC3339), got.Format(time.RFC3339))
	})

	t.Run("a location never fetched is ErrNotFound, not a zero time", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		_, err := repo.ObtainLatestForecastCapture(t.Context(), "nobody", domain.ProviderOpenMeteo)
		require.Error(t, err)
		assert.True(t, errors.Is(err, internal.ErrNotFound))
	})
}

func TestWeatherForecastDayRepository_RemoveForecastDaysBefore(t *testing.T) {
	t.Parallel()

	t.Run("drops past days and keeps the cutoff day itself", func(t *testing.T) {
		t.Parallel()
		repo := stubForecastDayRepo(t)

		captured := time.Now().UTC()
		require.NoError(t, repo.RetainWeatherForecastDays(t.Context(), []domain.WeatherForecastDay{
			forecastDay("loc1", "2026-08-19", captured, 20, 10, 0, 0),
			forecastDay("loc1", "2026-08-20", captured, 20, 10, 0, 0),
			forecastDay("loc1", "2026-08-21", captured, 20, 10, 0, 0),
		}))

		require.NoError(t, repo.RemoveForecastDaysBefore(t.Context(), "2026-08-20"))

		got, err := repo.ObtainForecastDays(t.Context(), "loc1", domain.ProviderOpenMeteo, "2026-01-01", 0)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "2026-08-20", got[0].ForecastDate)
		assert.Equal(t, "2026-08-21", got[1].ForecastDate)
	})
}

// forecastDay builds one Open-Meteo forecast row. Rain and snow are passed as plain
// float64 and stored as pointers so a case can express a real zero.
func forecastDay(locationID, date string, captured time.Time, maxTemp, minTemp, rain, snow float64) domain.WeatherForecastDay {
	return domain.WeatherForecastDay{
		LocationID:   locationID,
		Provider:     domain.ProviderOpenMeteo,
		ForecastDate: date,
		CapturedAt:   captured,
		TempMax:      &maxTemp,
		TempMin:      &minTemp,
		RainSum:      &rain,
		SnowfallSum:  &snow,
	}
}

// stubForecastDayRepo returns a repository over a fresh migrated in-memory database.
func stubForecastDayRepo(t *testing.T) *WeatherForecastDayRepository {
	t.Helper()
	repo, err := NewWeatherForecastDayRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	return repo
}
