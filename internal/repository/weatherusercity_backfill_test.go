package repository

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/migrations"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backfillAlertThawFilename names the immutable, already-applied-to-production
// migration this test exercises.
const backfillAlertThawFilename = "202607.021.weather_user_cities.backfill_alert_thaw.sql"

// backfillAlertThawSQL is the exact content of migration 202607.021, read from
// the canonical embedded FS so this test exercises the real migration text
// rather than a copy that could drift.
func backfillAlertThawSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.MigrationsFS.ReadFile(backfillAlertThawFilename)
	require.NoError(t, err)
	return string(raw)
}

// runBackfillAlertThaw executes migration 202607.021 directly against db,
// outside the migrator's applied-file bookkeeping, so the test can invoke it
// more than once against the same schema to prove idempotency.
func runBackfillAlertThaw(t *testing.T, db db) {
	t.Helper()
	tx, err := db.Transaction(t.Context())
	require.NoError(t, err)
	_, err = tx.Exec(backfillAlertThawSQL(t))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

// TestWeatherUserCityBackfillAlertThawMigration verifies the data back-fill in
// migrations/202607.021.weather_user_cities.backfill_alert_thaw.sql: every
// distinct (user_type, user_id, location_id) that has at least one row but no
// alert_thaw row gains exactly one, and the migration is idempotent.
//
// Every subtest builds its DB via stubSQLiteDBThrough(t, backfillAlertThawFilename)
// rather than the usual stubSQLiteDB — deliberately NOT the full migration chain.
// Migration 202607.025 (later in the real chain) rebuilds weather_user_cities
// without gismeteo_city_id, but 202607.021's committed, immutable SQL text
// explicitly names gismeteo_city_id in its INSERT column list. Replaying that
// exact text (runBackfillAlertThaw) against the fully-migrated schema fails with
// "no such column: gismeteo_city_id": 021 is a historical migration frozen
// against the schema shape at the moment it was written, so it must be exercised
// against a DB snapshot from that same moment, not a future one where the column
// no longer exists.
func TestWeatherUserCityBackfillAlertThawMigration(t *testing.T) {
	t.Parallel()

	t.Run("one thaw row per distinct city with multiple non-thaw rows", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillAlertThawFilename)
		repo, err := NewWeatherUserCityRepository(sqlDB)
		require.NoError(t, err)

		// Three non-thaw rows for the same (user, location) — the backfill must
		// collapse them to exactly one thaw row (GROUP BY), not three.
		for _, kind := range []domain.WeatherNotifyKind{
			domain.WeatherNotifyMorningSummary,
			domain.WeatherNotifyAlertHeat,
			domain.WeatherNotifyAlertFrost,
		} {
			require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
				UserType:    domain.UserTypeTelegram,
				UserID:      "u1",
				LocationID:  "loc1",
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

		runBackfillAlertThaw(t, sqlDB)

		all, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "u1")
		require.NoError(t, err)

		var thawRows []domain.WeatherUserCity
		for _, c := range all {
			if c.NotifyKind == domain.WeatherNotifyAlertThaw {
				thawRows = append(thawRows, c)
			}
		}
		require.Len(t, thawRows, 1, "a 3-non-thaw-row city must yield exactly one thaw row")

		thaw := thawRows[0]
		assert.NotEmpty(t, thaw.ID)
		assert.Equal(t, "loc1", thaw.LocationID)
		assert.Equal(t, "Almaty", thaw.DisplayName)
		assert.InDelta(t, 43.25, thaw.Latitude, 1e-6)
		assert.InDelta(t, 76.94, thaw.Longitude, 1e-6)
		assert.Equal(t, "Asia/Almaty", thaw.Timezone)
		assert.Equal(t, "Kazakhstan", thaw.Country)
		assert.Equal(t, "Almaty", thaw.Admin1)
		assert.Equal(t, "", thaw.ConditionValue)
		assert.True(t, thaw.AlertLatched, "backfilled rows must seed pre-latched: alert_latched=0 would fire a spurious mass notification on the first post-deploy tick since thaw fires on TempMax > 0, the default warm-season state")
		assert.True(t, thaw.LastNotifiedAt.IsZero(), "last_notified_at must be NULL (armed)")
		assert.Equal(t, 7, thaw.NotifyHour)
	})

	t.Run("rerun is a no-op", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillAlertThawFilename)
		repo, err := NewWeatherUserCityRepository(sqlDB)
		require.NoError(t, err)

		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u2",
			LocationID: "loc2",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}))

		runBackfillAlertThaw(t, sqlDB)
		firstPass, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "u2")
		require.NoError(t, err)
		require.Len(t, firstPass, 2, "morning_summary + one backfilled thaw row")

		runBackfillAlertThaw(t, sqlDB)
		secondPass, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "u2")
		require.NoError(t, err)
		assert.Len(t, secondPass, 2, "rerunning the migration must insert nothing new")
	})

	t.Run("city that already has a thaw row is left untouched", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillAlertThawFilename)
		repo, err := NewWeatherUserCityRepository(sqlDB)
		require.NoError(t, err)

		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u3",
			LocationID: "loc3",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}))
		existingThaw := &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u3",
			LocationID: "loc3",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyAlertThaw,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), existingThaw))

		runBackfillAlertThaw(t, sqlDB)

		all, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "u3")
		require.NoError(t, err)

		var thawRows []domain.WeatherUserCity
		for _, c := range all {
			if c.NotifyKind == domain.WeatherNotifyAlertThaw {
				thawRows = append(thawRows, c)
			}
		}
		require.Len(t, thawRows, 1, "no duplicate thaw row must be created")
		assert.Equal(t, existingThaw.ID, thawRows[0].ID, "the pre-existing thaw row's id must be unchanged")
	})

	t.Run("distinct users at the same location each get their own thaw row", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillAlertThawFilename)
		repo, err := NewWeatherUserCityRepository(sqlDB)
		require.NoError(t, err)

		for _, userID := range []string{"u4", "u5"} {
			require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
				UserType:   domain.UserTypeTelegram,
				UserID:     userID,
				LocationID: "shared-loc",
				Timezone:   "UTC",
				NotifyKind: domain.WeatherNotifyMorningSummary,
			}))
		}

		runBackfillAlertThaw(t, sqlDB)

		for _, userID := range []string{"u4", "u5"} {
			all, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, userID)
			require.NoError(t, err)
			var thawCount int
			for _, c := range all {
				if c.NotifyKind == domain.WeatherNotifyAlertThaw {
					thawCount++
				}
			}
			assert.Equal(t, 1, thawCount, "each user must get their own thaw row at a shared location")
		}
	})
}
