package repository

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/migrations"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backfillRainAlertFilename names the migration this test exercises.
const backfillRainAlertFilename = "202608.026.weather_user_cities.backfill_rain_alert.sql"

// backfillRainAlertSQL is the exact content of migration 202608.026, read from the
// canonical embedded FS so this test exercises the real migration text rather than a copy
// that could drift.
func backfillRainAlertSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.MigrationsFS.ReadFile(backfillRainAlertFilename)
	require.NoError(t, err)
	return string(raw)
}

// runBackfillRainAlert executes migration 202608.026 directly against db, outside the
// migrator's applied-file bookkeeping, so the test can invoke it more than once against
// the same schema to prove idempotency.
func runBackfillRainAlert(t *testing.T, db db) {
	t.Helper()
	tx, err := db.Transaction(t.Context())
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(), backfillRainAlertSQL(t))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

// rainRowsOf returns the rain_alert rows among cities.
func rainRowsOf(cities []domain.WeatherUserCity) []domain.WeatherUserCity {
	var out []domain.WeatherUserCity
	for _, c := range cities {
		if c.NotifyKind == domain.WeatherNotifyAlertRain {
			out = append(out, c)
		}
	}
	return out
}

// TestWeatherUserCityBackfillRainAlertMigration verifies the data back-fill in
// migrations/202608.026.weather_user_cities.backfill_rain_alert.sql: every distinct
// (user_type, user_id, location_id) that has at least one row but no rain_alert row gains
// exactly one, seeded armed, and the migration is idempotent.
//
// The armed (alert_latched = 0) seed is the load-bearing detail and the one this test
// exists to pin: rain_alert notifies on BOTH latch edges, so a pre-latched backfill row
// would emit a spurious "rain cleared" message to every user on the first post-deploy
// check tick — "no rain in the next 6 h" being the normal state.
func TestWeatherUserCityBackfillRainAlertMigration(t *testing.T) {
	t.Parallel()

	t.Run("one rain row per distinct city with multiple non-rain rows", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillRainAlertFilename)

		for _, kind := range []domain.WeatherNotifyKind{
			domain.WeatherNotifyMorningSummary,
			domain.WeatherNotifyAlertHeat,
			domain.WeatherNotifyAlertThaw,
		} {
			require.NoError(t, seedHistoricalWeatherUserCity(t.Context(), sqlDB, &domain.WeatherUserCity{
				UserType:    domain.UserTypeTelegram,
				UserID:      "ru1",
				LocationID:  "rloc1",
				DisplayName: "Almaty",
				Latitude:    43.25,
				Longitude:   76.94,
				Timezone:    "Asia/Almaty",
				Country:     "Kazakhstan",
				Admin1:      "Almaty",
				NotifyKind:  kind,
				NotifyHour:  7,
			}))
		}

		runBackfillRainAlert(t, sqlDB)

		all, err := obtainHistoricalWeatherUserCities(t.Context(), sqlDB, domain.UserTypeTelegram, "ru1")
		require.NoError(t, err)
		rainRows := rainRowsOf(all)
		require.Len(t, rainRows, 1, "a 3-non-rain-row city must yield exactly one rain row")

		rain := rainRows[0]
		assert.NotEmpty(t, rain.ID)
		assert.Equal(t, "rloc1", rain.LocationID)
		assert.Equal(t, "Almaty", rain.DisplayName)
		assert.InDelta(t, 43.25, rain.Latitude, 1e-6)
		assert.InDelta(t, 76.94, rain.Longitude, 1e-6)
		assert.Equal(t, "Asia/Almaty", rain.Timezone)
		assert.Equal(t, "Kazakhstan", rain.Country)
		assert.Equal(t, "Almaty", rain.Admin1)
		assert.Equal(t, "60", rain.ConditionValue, "the seeded threshold must match weatherDefaultRainThreshold")
		assert.False(t, rain.AlertLatched,
			"backfilled rain rows must seed armed: pre-latched would emit a spurious \"rain cleared\" to every user on the first post-deploy tick")
		assert.True(t, rain.LastNotifiedAt.IsZero(), "last_notified_at must be NULL")
		assert.Equal(t, 7, rain.NotifyHour)

		require.NoError(t, rain.Validate(), "the seeded threshold must satisfy domain validation")
	})

	t.Run("rerun is a no-op", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillRainAlertFilename)

		require.NoError(t, seedHistoricalWeatherUserCity(t.Context(), sqlDB, &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "ru2",
			LocationID: "rloc2",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}))

		runBackfillRainAlert(t, sqlDB)
		firstPass, err := obtainHistoricalWeatherUserCities(t.Context(), sqlDB, domain.UserTypeTelegram, "ru2")
		require.NoError(t, err)
		require.Len(t, firstPass, 2, "morning_summary + one backfilled rain row")

		runBackfillRainAlert(t, sqlDB)
		secondPass, err := obtainHistoricalWeatherUserCities(t.Context(), sqlDB, domain.UserTypeTelegram, "ru2")
		require.NoError(t, err)
		assert.Len(t, secondPass, 2, "rerunning the migration must insert nothing new")
	})

	t.Run("city that already has a rain row keeps its own threshold", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillRainAlertFilename)

		require.NoError(t, seedHistoricalWeatherUserCity(t.Context(), sqlDB, &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "ru3",
			LocationID: "rloc3",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}))
		existingRain := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "ru3",
			LocationID:     "rloc3",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertRain,
			ConditionValue: "85",
			AlertLatched:   true,
		}
		require.NoError(t, seedHistoricalWeatherUserCity(t.Context(), sqlDB, existingRain))

		runBackfillRainAlert(t, sqlDB)

		all, err := obtainHistoricalWeatherUserCities(t.Context(), sqlDB, domain.UserTypeTelegram, "ru3")
		require.NoError(t, err)
		rainRows := rainRowsOf(all)
		require.Len(t, rainRows, 1, "no duplicate rain row must be created")
		assert.Equal(t, existingRain.ID, rainRows[0].ID, "the pre-existing rain row's id must be unchanged")
		assert.Equal(t, "85", rainRows[0].ConditionValue, "a user-tuned threshold must survive the backfill")
		assert.True(t, rainRows[0].AlertLatched, "an existing latch must not be reset by the backfill")
	})

	t.Run("distinct users at the same location each get their own rain row", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillRainAlertFilename)

		for _, userID := range []string{"ru4", "ru5"} {
			require.NoError(t, seedHistoricalWeatherUserCity(t.Context(), sqlDB, &domain.WeatherUserCity{
				UserType:   domain.UserTypeTelegram,
				UserID:     userID,
				LocationID: "shared-rain-loc",
				Timezone:   "UTC",
				NotifyKind: domain.WeatherNotifyMorningSummary,
			}))
		}

		runBackfillRainAlert(t, sqlDB)

		for _, userID := range []string{"ru4", "ru5"} {
			all, err := obtainHistoricalWeatherUserCities(t.Context(), sqlDB, domain.UserTypeTelegram, userID)
			require.NoError(t, err)
			assert.Len(t, rainRowsOf(all), 1, "each user must get their own rain row at a shared location")
		}
	})
}
