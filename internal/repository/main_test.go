package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/domain/identity"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb/sqlitedbtest"
	"github.com/seilbekskindirov/beacon/migrations"
	_ "modernc.org/sqlite"
)

var _ sqlitedb.Committer = (*mockFailDB)(nil)

// stubSQLiteDB opens an in-memory SQLite DB, applies the canonical migrations,
// and returns a ready-to-use SQLiteClient. The DB is closed via t.Cleanup.
//
// Optional sourceNames are pre-seeded into rate_sources via seedRateSources so
// dependent rows (rate_values, rate_user_subscriptions, rate_user_events) can
// satisfy the FK on rate_user_subscriptions.source_name. Tests using custom
// source names outside the canonical seed should pass them here.
//
// The shared mutex guards only the sql.Open + PRAGMA + migrate phase; seeding
// proceeds without it so parallel tests don't serialise behind each other's
// N source inserts.
func stubSQLiteDB(tb testing.TB, sourceNames ...string) *sqlitedb.SQLiteClient {
	tb.Helper()

	sqliteDB := func() *sqlitedb.SQLiteClient {
		mu.Lock()
		defer mu.Unlock()

		mem, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			panic(err)
		}
		tb.Cleanup(func() { _ = mem.Close() })

		mem.SetMaxOpenConns(1)

		db, err := sqlitedb.NewSQLiteClientEx(mem)
		if err != nil {
			panic(err)
		}
		if db == nil {
			panic("failed to create SQLite client")
		}

		sqlitedbtest.Apply(tb, db)
		return db
	}()

	if len(sourceNames) > 0 {
		seedRateSources(tb, sqliteDB, sourceNames...)
	}

	return sqliteDB
}

// stubSQLiteDBThrough opens an in-memory SQLite DB with every embedded migration
// up to and including throughFilename applied — a schema snapshot frozen at that
// point in migration history, not the full current chain stubSQLiteDB applies.
//
// Two uses: (1) replaying an old, immutable migration's own committed SQL text,
// which may name a column a later migration has since dropped — the replay must
// run against the schema shape from the moment that migration was written, not a
// future one; (2) seeding a "before this migration" fixture and then applying the
// remaining pending migrations via sqlitedbtest.Apply (which only executes files
// __schema_migrations doesn't already record), to prove a later migration carries
// existing rows forward correctly — mirroring a real production upgrade.
func stubSQLiteDBThrough(t *testing.T, throughFilename string) *sqlitedb.SQLiteClient {
	t.Helper()

	entries, err := migrations.MigrationsFS.ReadDir(".")
	if err != nil {
		t.Fatalf("stubSQLiteDBThrough: read migrations dir: %v", err)
	}

	subset := fstest.MapFS{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || name > throughFilename {
			continue
		}
		data, readErr := migrations.MigrationsFS.ReadFile(name)
		if readErr != nil {
			t.Fatalf("stubSQLiteDBThrough: read %s: %v", name, readErr)
		}
		subset[name] = &fstest.MapFile{Data: data}
	}

	mu.Lock()
	defer mu.Unlock()

	mem, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	mem.SetMaxOpenConns(1)

	db, err := sqlitedb.NewSQLiteClientEx(mem)
	if err != nil {
		panic(err)
	}

	m, err := sqlitedb.NewMigrator(db, subset)
	if err != nil {
		t.Fatalf("stubSQLiteDBThrough: new migrator: %v", err)
	}
	if err = m.Run(t.Context()); err != nil {
		t.Fatalf("stubSQLiteDBThrough: run migrator: %v", err)
	}

	return db
}

// seedRateSources inserts a minimal rate_source row for each provided name so
// dependent rows (rate_values, rate_user_subscriptions, rate_user_events) can
// reference them without violating the FK on rate_user_subscriptions.source_name.
// Tests that pick arbitrary source names (not from the canonical seed) should
// call this immediately after stubSQLiteDB.
func seedRateSources(tb testing.TB, db *sqlitedb.SQLiteClient, names ...string) {
	tb.Helper()
	r, err := NewRateSourceRepository(db)
	if err != nil {
		tb.Fatalf("seedRateSources: NewRateSourceRepository: %v", err)
	}
	for _, name := range names {
		src := &domain.RateSource{
			Name:          name,
			Title:         "test fixture " + name,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			URL:           "https://example.invalid/" + name,
			Interval:      "10m",
			Kind:          "BID",
			Active:        true,
		}
		if err := r.RetainRateSource(tb.Context(), src); err != nil {
			tb.Fatalf("seedRateSources(%q): %v", name, err)
		}
	}
}

// mockFailDB implements the db interface but always returns an error from
// Transaction and ReadOnlyTransaction. Use it to test error-handling branches
// that fire when the DB is unavailable.
type mockFailDB struct{ err error }

func (m *mockFailDB) Transaction(_ context.Context) (*sql.Tx, error) {
	return nil, errors.New(m.err.Error())
}

func (m *mockFailDB) ReadOnlyTransaction(_ context.Context) (*sql.Tx, error) {
	return nil, errors.New(m.err.Error())
}

var mu sync.Mutex

// weatherUserCityEraColumns lists the weather_user_cities columns present at every
// historical snapshot the backfill-migration tests run against (migrations 020 to 026).
//
// Those tests must not reach the database through WeatherUserCityRepository. Its SQL names
// the columns of the CURRENT schema, so seeding or reading a frozen historical snapshot
// through it fails on every column added afterwards — a defect in the test, not in the
// column. gismeteo_city_id is omitted deliberately: it is nullable while it exists and gone
// from migration 025 onwards, so leaving it out is valid across the whole range.
const weatherUserCityEraColumns = "id, user_type, user_id, location_id, display_name, " +
	"latitude, longitude, timezone, country, admin1, notify_kind, notify_hour, " +
	"condition_value, last_notified_at, alert_latched, updated_at, created_at"

// seedHistoricalWeatherUserCity inserts one row into a historical weather_user_cities
// snapshot, using only the columns weatherUserCityEraColumns names. It mints an ID and
// timestamps the way the repository would, so a test reads back what it wrote.
func seedHistoricalWeatherUserCity(ctx context.Context, sqlDB db, record *domain.WeatherUserCity) error {
	if record.ID == "" {
		record.ID = identity.New(identity.KindWeatherUserCity)
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	tx, err := sqlDB.Transaction(ctx)
	if err != nil {
		return err
	}
	defer printRollbackError(tx)

	var lastNotifiedAt *string
	if !record.LastNotifiedAt.IsZero() {
		s := record.LastNotifiedAt.Format(time.RFC3339)
		lastNotifiedAt = &s
	}
	var alertLatched int
	if record.AlertLatched {
		alertLatched = 1
	}

	cmd := "INSERT INTO weather_user_cities (" + weatherUserCityEraColumns +
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);"
	if _, err := tx.ExecContext(ctx, cmd,
		record.ID, record.UserType, record.UserID, record.LocationID, record.DisplayName,
		record.Latitude, record.Longitude, record.Timezone, record.Country, record.Admin1,
		record.NotifyKind, record.NotifyHour, record.ConditionValue, lastNotifiedAt,
		alertLatched, record.UpdatedAt.Format(time.RFC3339), record.CreatedAt.Format(time.RFC3339),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// obtainHistoricalWeatherUserCities reads one user's rows out of a historical
// weather_user_cities snapshot, using only the columns weatherUserCityEraColumns names.
func obtainHistoricalWeatherUserCities(ctx context.Context, sqlDB db, userType domain.UserType, userID string) ([]domain.WeatherUserCity, error) {
	tx, err := sqlDB.ReadOnlyTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer printRollbackError(tx)

	query := "SELECT " + weatherUserCityEraColumns +
		" FROM weather_user_cities WHERE user_type = ? AND user_id = ?;"
	rows, err := tx.QueryContext(ctx, query, userType, userID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	items := []domain.WeatherUserCity{}
	for rows.Next() {
		var item domain.WeatherUserCity
		var lastNotifiedAt *string
		var alertLatched int
		var updatedAt, createdAt string
		if scanErr := rows.Scan(
			&item.ID, &item.UserType, &item.UserID, &item.LocationID, &item.DisplayName,
			&item.Latitude, &item.Longitude, &item.Timezone, &item.Country, &item.Admin1,
			&item.NotifyKind, &item.NotifyHour, &item.ConditionValue, &lastNotifiedAt,
			&alertLatched, &updatedAt, &createdAt,
		); scanErr != nil {
			return nil, scanErr
		}
		item.AlertLatched = alertLatched != 0
		if item.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt); err != nil {
			return nil, err
		}
		if item.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
			return nil, err
		}
		if lastNotifiedAt != nil && *lastNotifiedAt != "" {
			if item.LastNotifiedAt, err = time.Parse(time.RFC3339, *lastNotifiedAt); err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, iterErr
	}
	return items, nil
}
