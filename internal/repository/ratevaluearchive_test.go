package repository

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/migrations"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// stubArchiveDB opens an in-memory database with the archive tier's own migration
// chain applied — deliberately not stubSQLiteDB's chain. The two databases share a
// table name and nothing else: the archive's rate_values carries no foreign key to
// rate_sources, which is what lets it outlive the sources that produced its rows.
func stubArchiveDB(t *testing.T) *sqlitedb.SQLiteClient {
	t.Helper()

	mu.Lock()
	defer mu.Unlock()

	mem, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	mem.SetMaxOpenConns(1)

	db, err := sqlitedb.NewSQLiteClientEx(mem, os.Stdout)
	require.NoError(t, err)

	m, err := sqlitedb.NewMigrator(db, migrations.ArchiveMigrationsFS)
	require.NoError(t, err)
	require.NoError(t, m.Run(t.Context()))
	require.Positive(t, m.Applied(), "the archive chain must contain at least one migration")

	return db
}

// archiveValue builds a rate value with an explicit id and timestamp, since the
// archive is keyed on the id the hot tier assigned rather than minting its own.
func archiveValue(id, source string, ts time.Time, price float64) domain.RateValue {
	return domain.RateValue{
		ID:            id,
		SourceName:    source,
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		Price:         price,
		Timestamp:     ts,
	}
}

func TestRateValueArchiveRepository_RetainRateValues(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("inserts new rows and reports the count", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		inserted, err := repo.RetainRateValues(t.Context(), []domain.RateValue{
			archiveValue("rv1", "src-a", base, 1.5),
			archiveValue("rv2", "src-a", base.Add(time.Minute), 1.6),
		})
		require.NoError(t, err)
		assert.Equal(t, 2, inserted)

		total, err := repo.CountRateValues(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(2), total)
	})

	t.Run("re-copying an overlapping window inserts nothing and changes nothing", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		original := archiveValue("rv1", "src-a", base, 1.5)
		_, err = repo.RetainRateValues(t.Context(), []domain.RateValue{original})
		require.NoError(t, err)

		// Same id, different payload: the archive must ignore it rather than update.
		// Idempotence is what makes the reconciliation pass safe to re-run, and
		// immutability is what makes the archive trustworthy as history.
		mutated := archiveValue("rv1", "src-b", base.Add(time.Hour), 99.9)
		inserted, err := repo.RetainRateValues(t.Context(), []domain.RateValue{
			mutated,
			archiveValue("rv2", "src-a", base.Add(time.Minute), 1.6),
		})
		require.NoError(t, err)
		assert.Equal(t, 1, inserted, "only the genuinely new row counts")

		total, err := repo.CountRateValues(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(2), total)

		ts, id, ok, err := repo.ObtainArchiveWatermark(t.Context())
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "rv2", id, "the ignored row must not have moved the watermark forward")
		assert.True(t, ts.Equal(base.Add(time.Minute)))
	})

	t.Run("an empty batch is a no-op", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		inserted, err := repo.RetainRateValues(t.Context(), nil)
		require.NoError(t, err)
		assert.Zero(t, inserted)
	})

	t.Run("a record without an id is rejected", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		_, err = repo.RetainRateValues(t.Context(), []domain.RateValue{
			archiveValue("", "src-a", base, 1.5),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no id")

		total, err := repo.CountRateValues(t.Context())
		require.NoError(t, err)
		assert.Zero(t, total, "the whole batch must roll back")
	})
}

func TestRateValueArchiveRepository_ObtainArchiveWatermark(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("an empty archive reports no watermark", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		ts, id, ok, err := repo.ObtainArchiveWatermark(t.Context())
		require.NoError(t, err)
		assert.False(t, ok, "an empty archive must start the pass from the beginning")
		assert.True(t, ts.IsZero())
		assert.Empty(t, id)
	})

	t.Run("ties on timestamp break on the id", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		// One collector tick writes many rows sharing a second-precision timestamp.
		// The watermark must name a specific row within that tick, or the next pass
		// cannot tell what it has already copied.
		_, err = repo.RetainRateValues(t.Context(), []domain.RateValue{
			archiveValue("rv-a", "src-a", base, 1),
			archiveValue("rv-c", "src-c", base, 3),
			archiveValue("rv-b", "src-b", base, 2),
		})
		require.NoError(t, err)

		ts, id, ok, err := repo.ObtainArchiveWatermark(t.Context())
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, ts.Equal(base))
		assert.Equal(t, "rv-c", id, "the highest id within the newest timestamp wins")
	})

	t.Run("the newest timestamp wins over the highest id", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		_, err = repo.RetainRateValues(t.Context(), []domain.RateValue{
			archiveValue("rv-z", "src-a", base, 1),
			archiveValue("rv-a", "src-a", base.Add(time.Hour), 2),
		})
		require.NoError(t, err)

		ts, id, ok, err := repo.ObtainArchiveWatermark(t.Context())
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, ts.Equal(base.Add(time.Hour)))
		assert.Equal(t, "rv-a", id)
	})
}

func TestRateValueArchiveRepository_CheckUP(t *testing.T) {
	t.Parallel()

	repo, err := NewRateValueArchiveRepository(stubArchiveDB(t))
	require.NoError(t, err)
	require.NoError(t, repo.CheckUP(t.Context()))
	assert.Equal(t, "rate_values_archive", repo.Name())
}
