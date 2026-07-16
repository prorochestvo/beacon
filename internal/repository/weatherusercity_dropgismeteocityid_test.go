package repository

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb/sqlitedbtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// productionShapedFixture inserts rows that mimic a real pre-release database:
// a morning_summary row, a pre-latched forced-thaw row carrying a legacy non-NULL
// gismeteo_city_id (proving the migration tolerates a stray value, not just NULL),
// and an already-fired alert_frost row exercising the last_notified_at
// forecast_date fire cursor. Bypasses the repository layer (which, post Task 5,
// no longer sets gismeteo_city_id) with a raw INSERT naming every column
// explicitly, and one weather_observations row per provider so migration 022's
// DELETE can be proven selective.
func productionShapedFixture(t *testing.T, sqlDB db) {
	t.Helper()

	tx, err := sqlDB.Transaction(t.Context())
	require.NoError(t, err)
	defer printRollbackError(tx)

	// Column names are literal here, deliberately: this fixture recreates the
	// legacy (pre-025) schema shape, including gismeteo_city_id, which no longer
	// has a live Go const after Task 5 removed it from the repository.
	insertCity := "INSERT INTO " + weatherUserCityTableName + " (" +
		"id, user_type, user_id, location_id, display_name, " +
		"latitude, longitude, timezone, country, admin1, " +
		"gismeteo_city_id, notify_kind, notify_hour, condition_value, " +
		"last_notified_at, alert_latched, updated_at, created_at" +
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);"

	gisID := 5205
	rows := []struct {
		id             string
		userID         string
		locationID     string
		gismeteoCityID *int
		notifyKind     domain.WeatherNotifyKind
		conditionValue string
		lastNotifiedAt *string
		alertLatched   int
		updatedAt      string
		createdAt      string
	}{
		{
			id: "WUCFIX001", userID: "userA", locationID: "locA",
			gismeteoCityID: nil, notifyKind: domain.WeatherNotifyMorningSummary,
			conditionValue: "", lastNotifiedAt: nil, alertLatched: 0,
			updatedAt: "2026-07-01T06:00:00Z", createdAt: "2026-01-01T06:00:00Z",
		},
		{
			id: "WUCFIX002", userID: "userA", locationID: "locA",
			gismeteoCityID: &gisID, notifyKind: domain.WeatherNotifyAlertThaw,
			conditionValue: "", lastNotifiedAt: nil, alertLatched: 1,
			updatedAt: "2026-07-01T06:00:00Z", createdAt: "2026-01-01T06:00:00Z",
		},
		{
			id: "WUCFIX003", userID: "userB", locationID: "locB",
			gismeteoCityID: nil, notifyKind: domain.WeatherNotifyAlertFrost,
			conditionValue: "-5", lastNotifiedAt: strPtr("2026-01-15T00:00:00Z"), alertLatched: 1,
			updatedAt: "2026-01-10T06:00:00Z", createdAt: "2025-12-01T06:00:00Z",
		},
	}
	for _, r := range rows {
		_, err = tx.ExecContext(t.Context(), insertCity,
			r.id, domain.UserTypeTelegram, r.userID, r.locationID, "Fixture City",
			43.25, 76.91, "Asia/Almaty", "Kazakhstan", "Almaty",
			r.gismeteoCityID, r.notifyKind, 7, r.conditionValue,
			r.lastNotifiedAt, r.alertLatched, r.updatedAt, r.createdAt,
		)
		require.NoError(t, err, "insert fixture row %s", r.id)
	}

	insertObs := "INSERT INTO " + weatherObservationTableName + " (" +
		"id, location_id, provider, latitude, longitude, captured_at, forecast_date" +
		") VALUES (?, ?, ?, ?, ?, ?, ?);"
	_, err = tx.ExecContext(t.Context(), insertObs,
		"WOFIX-OM", "locA", domain.ProviderOpenMeteo, 43.25, 76.91,
		"2026-07-01T05:00:00Z", "2026-07-01")
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(), insertObs,
		"WOFIX-GM", "locA", "gismeteo", 43.25, 76.91,
		"2026-07-01T05:00:00Z", "2026-07-01")
	require.NoError(t, err)

	require.NoError(t, tx.Commit())
}

func strPtr(s string) *string { return &s }

// tableExists reports whether name is a real table in the sqlite_master catalog.
func tableExists(t *testing.T, sqlDB db, name string) bool {
	t.Helper()
	tx, err := sqlDB.ReadOnlyTransaction(t.Context())
	require.NoError(t, err)
	defer printRollbackError(tx)

	var count int
	err = tx.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?;", name,
	).Scan(&count)
	require.NoError(t, err)
	return count > 0
}

// tableColumns returns the column names of table via PRAGMA table_info.
func tableColumns(t *testing.T, sqlDB db, table string) []string {
	t.Helper()
	tx, err := sqlDB.ReadOnlyTransaction(t.Context())
	require.NoError(t, err)
	defer printRollbackError(tx)

	rows, err := tx.QueryContext(t.Context(), "PRAGMA table_info("+table+");")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var cols []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		require.NoError(t, rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk))
		cols = append(cols, name)
	}
	require.NoError(t, rows.Err())
	return cols
}

// indexNames returns the index names attached to table via PRAGMA index_list.
func indexNames(t *testing.T, sqlDB db, table string) []string {
	t.Helper()
	tx, err := sqlDB.ReadOnlyTransaction(t.Context())
	require.NoError(t, err)
	defer printRollbackError(tx)

	rows, err := tx.QueryContext(t.Context(), "PRAGMA index_list("+table+");")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var names []string
	for rows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		require.NoError(t, rows.Scan(&seq, &name, &unique, &origin, &partial))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	return names
}

// TestWeatherGismeteoRemovalMigrations verifies migrations 202607.022-025 against
// a production-shaped fixture (rows in weather_user_cities and weather_observations,
// plus the seeded weather_sources / weather_gismeteo_cities rows from migrations
// 016/017) — the highest-risk task in plans/264-remove-gismeteo.md: a wrong or
// missing column in 025's rebuild silently drops live user subscriptions.
//
// Each subtest builds its DB via stubSQLiteDBThrough(t, backfillAlertThawFilename)
// (schema frozen right after 021, still carrying weather_sources /
// weather_gismeteo_cities / gismeteo_city_id), inserts the fixture, then calls
// sqlitedbtest.Apply again — which applies only the still-pending 022-025 because
// __schema_migrations already records 001-021 — mirroring a real production
// upgrade: migrate to 021 today, ship 022-025 in the next release.
func TestWeatherGismeteoRemovalMigrations(t *testing.T) {
	t.Parallel()

	t.Run("weather_sources and weather_gismeteo_cities are dropped; weather_user_cities loses gismeteo_city_id but keeps its indexes", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillAlertThawFilename)
		productionShapedFixture(t, sqlDB)

		sqlitedbtest.Apply(t, sqlDB)

		// weather_sources and weather_gismeteo_cities are gone as *tables* now, so
		// (unlike weatherUserCityTableName above) neither has a live Go const any
		// more — the repositories that owned them were deleted in Task 4.
		assert.False(t, tableExists(t, sqlDB, "weather_sources"), "weather_sources must no longer exist")
		assert.False(t, tableExists(t, sqlDB, "weather_gismeteo_cities"), "weather_gismeteo_cities must no longer exist")

		cols := tableColumns(t, sqlDB, weatherUserCityTableName)
		assert.NotContains(t, cols, "gismeteo_city_id", "gismeteo_city_id must be dropped")
		assert.ElementsMatch(t, []string{
			"id", "user_type", "user_id", "location_id", "display_name",
			"latitude", "longitude", "timezone", "country", "admin1",
			"notify_kind", "notify_hour", "condition_value", "last_notified_at",
			"alert_latched", "updated_at", "created_at",
		}, cols, "every surviving column must be carried forward, nothing else lost or added")

		// SQLite gives a TEXT PRIMARY KEY its own implicit autoindex (origin "pk"),
		// separate from the UNIQUE(user_type,...) constraint's autoindex (origin
		// "u") — verified against migration 011's original DDL, so this pair
		// predates the rebuild and is not a defect it introduced. The rebuild must
		// preserve both, plus the three explicitly named indexes: 5 total.
		idx := indexNames(t, sqlDB, weatherUserCityTableName)
		assert.Contains(t, idx, "idx_weather_user_cities_user")
		assert.Contains(t, idx, "idx_weather_user_cities_location")
		assert.Contains(t, idx, "idx_weather_user_cities_notify_kind")
		assert.Len(t, idx, 5, "3 named indexes + the PRIMARY KEY autoindex + the UNIQUE(user_type,user_id,location_id,notify_kind) autoindex")
	})

	t.Run("every pre-existing weather_user_cities row survives with identical fields", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillAlertThawFilename)
		productionShapedFixture(t, sqlDB)

		sqlitedbtest.Apply(t, sqlDB)

		repo, err := NewWeatherUserCityRepository(sqlDB)
		require.NoError(t, err)

		morning, err := repo.ObtainWeatherUserCityByID(t.Context(), "WUCFIX001")
		require.NoError(t, err)
		assert.Equal(t, domain.WeatherNotifyMorningSummary, morning.NotifyKind)
		assert.Equal(t, "", morning.ConditionValue)
		assert.False(t, morning.AlertLatched)
		assert.True(t, morning.LastNotifiedAt.IsZero())
		assert.Equal(t, "2026-07-01T06:00:00Z", morning.UpdatedAt.Format(time.RFC3339))
		assert.Equal(t, "2026-01-01T06:00:00Z", morning.CreatedAt.Format(time.RFC3339))

		thaw, err := repo.ObtainWeatherUserCityByID(t.Context(), "WUCFIX002")
		require.NoError(t, err)
		assert.Equal(t, domain.WeatherNotifyAlertThaw, thaw.NotifyKind)
		assert.True(t, thaw.AlertLatched, "the forced-thaw row's pre-latched state must survive the rebuild")

		frost, err := repo.ObtainWeatherUserCityByID(t.Context(), "WUCFIX003")
		require.NoError(t, err)
		assert.Equal(t, domain.WeatherNotifyAlertFrost, frost.NotifyKind)
		assert.Equal(t, "-5", frost.ConditionValue)
		assert.True(t, frost.AlertLatched)
		wantFireCursor, parseErr := time.Parse(time.RFC3339, "2026-01-15T00:00:00Z")
		require.NoError(t, parseErr)
		assert.True(t, wantFireCursor.Equal(frost.LastNotifiedAt), "the alert fire-date cursor in last_notified_at must survive the rebuild")
	})

	t.Run("gismeteo observations are purged, open-meteo observations are untouched", func(t *testing.T) {
		t.Parallel()
		sqlDB := stubSQLiteDBThrough(t, backfillAlertThawFilename)
		productionShapedFixture(t, sqlDB)

		sqlitedbtest.Apply(t, sqlDB)

		obsRepo, err := NewWeatherObservationRepository(sqlDB)
		require.NoError(t, err)

		_, err = obsRepo.ObtainLatestObservation(t.Context(), "locA", "gismeteo")
		require.True(t, errors.Is(err, internal.ErrNotFound), "the gismeteo row must be deleted by migration 022")

		got, err := obsRepo.ObtainLatestObservation(t.Context(), "locA", domain.ProviderOpenMeteo)
		require.NoError(t, err)
		assert.Equal(t, "WOFIX-OM", got.ID)
		assert.Equal(t, "2026-07-01", got.ForecastDate)
	})
}
