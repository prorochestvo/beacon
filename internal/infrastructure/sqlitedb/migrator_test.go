package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var (
	_ source    = (*stubMigrationSource)(nil)
	_ committer = (*nilTxCommitter)(nil)
)

// minimalFS is a one-file fstest.MapFS suitable for tests that need a valid FS
// but do not care about the schema applied.
func minimalFS() fstest.MapFS {
	return fstest.MapFS{
		"stub_init.sql": {
			Data: []byte("CREATE TABLE IF NOT EXISTS stub_init (id INTEGER PRIMARY KEY);"),
		},
	}
}

func TestNewMigrator(t *testing.T) {
	t.Parallel()

	t.Run("applies fs migrations", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)
		require.NotNil(t, m)
	})
	t.Run("with custom source applies extra migration", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		src := &stubMigrationSource{
			migrations: map[string]string{
				"custom_001.sql": "CREATE TABLE IF NOT EXISTS" + " custom_test " + "(id INTEGER PRIMARY KEY);",
			},
		}
		m, err := NewMigrator(c, minimalFS(), src)
		require.NoError(t, err)
		require.NotNil(t, m)
		require.NoError(t, m.Run(t.Context()))

		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		var count int
		require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM"+" custom_test;").Scan(&count))
		require.Equal(t, 0, count)
	})
	t.Run("source returning error propagates", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		src := &stubMigrationSource{err: errors.New("source unavailable")}
		_, err := NewMigrator(c, minimalFS(), src)
		require.Error(t, err)
		require.ErrorContains(t, err, "source unavailable")
	})
	t.Run("nil transaction with no error returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewMigrator(&nilTxCommitter{}, minimalFS())
		require.Error(t, err)
		require.ErrorContains(t, err, "transaction is nil")
	})
	t.Run("transaction error from committer propagates", func(t *testing.T) {
		t.Parallel()
		_, err := NewMigrator(&mockFailCommitter{err: errors.New("connection refused")}, minimalFS())
		require.Error(t, err)
	})
	t.Run("empty source map is skipped without error", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		src := &stubMigrationSource{migrations: map[string]string{}}
		m, err := NewMigrator(c, minimalFS(), src)
		require.NoError(t, err)
		require.NotNil(t, m)
	})
}

func TestMigrator_Run(t *testing.T) {
	t.Parallel()

	t.Run("applies fs migrations", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)
		require.NotNil(t, m)

		require.NoError(t, m.Run(t.Context()))

		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		var count int
		require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM"+" "+migrationTableName+";").Scan(&count))
		require.GreaterOrEqual(t, count, 1)
	})
	t.Run("idempotent on second run", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)

		require.NoError(t, m.Run(t.Context()))
		require.NoError(t, m.Run(t.Context()))
	})
	t.Run("applied count is zero on second run", func(t *testing.T) {
		t.Parallel()
		// Use a fresh in-memory DB so the FS migration has not been applied yet.
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = mem.Close() })
		mem.SetMaxOpenConns(1)

		c, err := NewSQLiteClientEx(mem)
		require.NoError(t, err)

		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)

		require.NoError(t, m.Run(t.Context()))
		require.Equal(t, 1, m.Applied())

		require.NoError(t, m.Run(t.Context()))
		require.Equal(t, 0, m.Applied())
	})
	t.Run("empty content migration is skipped", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		emptyFS := fstest.MapFS{
			"zzz_empty.sql": {Data: []byte{}},
		}
		src := &stubMigrationSource{
			migrations: map[string]string{
				"zzz_extra_empty.sql": "",
			},
		}
		m, err := NewMigrator(c, emptyFS, src)
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))
	})
	t.Run("transaction error is propagated", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)

		m.db = &mockFailCommitter{err: errors.New("db unavailable")}
		require.Error(t, m.Run(t.Context()))
	})
	t.Run("nil transaction in Run returns error", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)

		m.db = &nilTxCommitter{}
		require.Error(t, m.Run(t.Context()))
	})
	t.Run("scan error when migration table is dropped between runs", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = mem.Close() })
		mem.SetMaxOpenConns(1)

		c, err := NewSQLiteClientEx(mem)
		require.NoError(t, err)

		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))

		tx, txErr := c.Transaction(t.Context())
		require.NoError(t, txErr)
		_, txErr = tx.ExecContext(t.Context(), "DROP TABLE"+" "+migrationTableName)
		require.NoError(t, txErr)
		require.NoError(t, tx.Commit())

		require.Error(t, m.Run(t.Context()))
	})
	t.Run("invalid migration sql returns error", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = mem.Close() })
		mem.SetMaxOpenConns(1)

		c, err := NewSQLiteClientEx(mem)
		require.NoError(t, err)

		badFS := fstest.MapFS{
			"zzz_invalid.sql": {Data: []byte("THIS IS NOT VALID SQL !!!")},
		}
		m, err := NewMigrator(c, badFS)
		require.NoError(t, err)

		require.Error(t, m.Run(t.Context()))
	})
}

func TestRequireMigratedSchema(t *testing.T) {
	t.Parallel()

	// newTestClient applies exactly this one file, so it is the set a migrated test
	// database is expected to carry.
	appliedFS := func() fstest.MapFS {
		return fstest.MapFS{
			"stub_init.sql": {Data: []byte("CREATE TABLE IF NOT EXISTS stub_init (id INTEGER PRIMARY KEY);")},
		}
	}

	t.Run("returns nil when every migration is recorded", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		require.NoError(t, RequireMigratedSchema(t.Context(), c, appliedFS()))
	})

	t.Run("returns error when schema is unmigrated", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = mem.Close() })
		mem.SetMaxOpenConns(1)

		c, err := NewSQLiteClientEx(mem)
		require.NoError(t, err)

		err = RequireMigratedSchema(t.Context(), c, appliedFS())
		require.Error(t, err)
		require.ErrorContains(t, err, "schema not initialised")
	})

	t.Run("returns error when the build expects a migration the database lacks", func(t *testing.T) {
		t.Parallel()
		// The deploy window: the channel symlink already points at a build newer than the
		// schema. Before the set was compared, a non-empty table was enough to start, and
		// the binary died on the first query naming a new column instead.
		c := newTestClient(t)
		ahead := appliedFS()
		ahead["stub_later.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE IF NOT EXISTS stub_later (id INTEGER PRIMARY KEY);")}

		err := RequireMigratedSchema(t.Context(), c, ahead)
		require.Error(t, err)
		require.ErrorContains(t, err, "schema is behind this build")
		require.ErrorContains(t, err, "stub_later.sql")
	})

	t.Run("a database ahead of the build is accepted", func(t *testing.T) {
		t.Parallel()
		// What a rollback to the previous artifact looks like. Refusing here would turn a
		// rollback into an outage, so the check is one-directional on purpose.
		c := newTestClient(t)
		require.NoError(t, RequireMigratedSchema(t.Context(), c, fstest.MapFS{}))
	})

	t.Run("an empty migration file is not treated as missing", func(t *testing.T) {
		t.Parallel()
		// It applies nothing and is therefore never recorded. That is a defect for
		// Migrator.Verify to report, not a reason to keep a service down.
		c := newTestClient(t)
		withEmpty := appliedFS()
		withEmpty["stub_empty.sql"] = &fstest.MapFile{Data: []byte("")}

		require.NoError(t, RequireMigratedSchema(t.Context(), c, withEmpty))
	})
}

func TestSummariseMigrations(t *testing.T) {
	t.Parallel()

	t.Run("a short list is returned whole", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, []string{"a", "b"}, summarise([]string{"a", "b"}, 5))
	})

	t.Run("a long list keeps the limit and counts the tail", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, []string{"a", "b", "and 2 more"}, summarise([]string{"a", "b", "c", "d"}, 2))
	})

	t.Run("trimming does not scribble on the caller's slice", func(t *testing.T) {
		t.Parallel()
		items := []string{"a", "b", "c", "d"}
		summarise(items, 2)
		assert.Equal(t, []string{"a", "b", "c", "d"}, items)
	})
}

// stubMigrationSource implements source for testing.
type stubMigrationSource struct {
	migrations map[string]string
	err        error
}

func (s *stubMigrationSource) Migration() (map[string]string, error) {
	return s.migrations, s.err
}

// nilTxCommitter is a committer that returns a nil *sql.Tx with no error,
// exercising the "tx == nil && err == nil" guard in NewMigrator and Run.
type nilTxCommitter struct{}

func (n *nilTxCommitter) Transaction(_ context.Context) (*sql.Tx, error) {
	//nolint:nilnil // returning both nil is the defect this double exists to simulate
	return nil, nil
}

// TestMigrator_Verify covers the deploy gate from issue #5: after the migrate unit runs,
// the ledger must account for every migration the binary ships, and a migration that can
// never apply must be loud rather than silent.
func TestMigrator_Verify(t *testing.T) {
	t.Parallel()

	t.Run("passes after a successful run", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))

		require.NoError(t, m.Verify(t.Context()))
	})

	t.Run("fails when a migration was never recorded", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))

		// Simulate the ledger disagreeing with the shipped set: a database restored
		// from an older snapshot, or one a stale DSN pointed the migrator away from.
		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), "DELETE FROM "+migrationTableName+" WHERE filename = 'stub_init.sql';")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		err = m.Verify(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "stub_init.sql")
		require.Contains(t, err.Error(), migrationTableName)
	})

	t.Run("fails on an empty migration file", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		emptyFS := fstest.MapFS{
			"001_real.sql":  {Data: []byte("CREATE TABLE IF NOT EXISTS verify_real (id INTEGER PRIMARY KEY);")},
			"002_blank.sql": {Data: []byte{}},
		}
		m, err := NewMigrator(c, emptyFS)
		require.NoError(t, err)
		// Run tolerates the blank file — that silent tolerance is exactly what makes an
		// accidentally truncated migration invisible without Verify.
		require.NoError(t, m.Run(t.Context()))

		err = m.Verify(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "002_blank.sql")
		require.Contains(t, err.Error(), "empty migration file")
		require.NotContains(t, err.Error(), "001_real.sql", "the applied migration must not be reported")
	})

	t.Run("reports every offending file, not just the first", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		mixedFS := fstest.MapFS{
			"001_real.sql":   {Data: []byte("CREATE TABLE IF NOT EXISTS verify_multi (id INTEGER PRIMARY KEY);")},
			"002_blank.sql":  {Data: []byte{}},
			"003_blank2.sql": {Data: []byte{}},
		}
		m, err := NewMigrator(c, mixedFS)
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))

		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), "DELETE FROM "+migrationTableName+" WHERE filename = '001_real.sql';")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		err = m.Verify(t.Context())
		require.Error(t, err)
		for _, want := range []string{"001_real.sql", "002_blank.sql", "003_blank2.sql"} {
			require.Containsf(t, err.Error(), want, "one run must surface the whole picture, missing %s", want)
		}
	})

	t.Run("propagates a transaction failure", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))

		m.db = &mockFailCommitter{err: errors.New("db unavailable")}
		require.Error(t, m.Verify(t.Context()))

		m.db = &nilTxCommitter{}
		require.Error(t, m.Verify(t.Context()))
	})

	t.Run("fails when the ledger table is gone", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		m, err := NewMigrator(c, minimalFS())
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))

		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), "DROP TABLE "+migrationTableName+";")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		require.Error(t, m.Verify(t.Context()))
	})
}
