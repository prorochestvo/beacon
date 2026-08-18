package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var _ committer = (*mockFailCommitter)(nil)
var _ Committer = (*SQLiteClient)(nil)

// newTestClient opens an in-memory SQLite DB, applies the migration table, and
// returns a ready-to-use *SQLiteClient. The DB is closed automatically when the
// test finishes.
func newTestClient(t *testing.T) *SQLiteClient {
	t.Helper()

	mem, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	mem.SetMaxOpenConns(1)

	c, err := NewSQLiteClientEx(mem, os.Stdout)
	require.NoError(t, err)

	// Bootstrap the migration table so Ping works.
	bootstrapFS := fstest.MapFS{
		"stub_init.sql": {Data: []byte("CREATE TABLE IF NOT EXISTS stub_init (id INTEGER PRIMARY KEY);")},
	}
	m, err := NewMigrator(c, bootstrapFS)
	require.NoError(t, err)
	require.NoError(t, m.Run(t.Context()))

	return c
}

func TestNewSQLiteClientEx(t *testing.T) {
	t.Parallel()

	t.Run("returns error when db is already closed", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		require.NoError(t, mem.Close())
		_, err = NewSQLiteClientEx(mem, os.Stdout)
		require.Error(t, err)
	})
}

func TestNewSQLiteClient(t *testing.T) {
	t.Parallel()

	t.Run("opens a file-based sqlite database", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "test.db")
		dsn := &stubDataSource{path: dbPath}

		c, err := NewSQLiteClient(dsn, os.Stdout)
		require.NoError(t, err)
		require.NotNil(t, c)
		t.Cleanup(func() { _ = c.Close() })

		// Bootstrap migration table so Ping (which queries it) works.
		bootstrapFS := fstest.MapFS{
			"stub_init.sql": {Data: []byte("CREATE TABLE IF NOT EXISTS stub_init (id INTEGER PRIMARY KEY);")},
		}
		m, err := NewMigrator(c, bootstrapFS)
		require.NoError(t, err)
		require.NoError(t, m.Run(t.Context()))

		require.NoError(t, c.Ping(t.Context()))
	})
	t.Run("returns error when database path is inaccessible", func(t *testing.T) {
		t.Parallel()
		// A path under a non-existent directory forces the SQLite driver to fail
		// when executing the first statement (WAL/foreign-key pragmas inside
		// NewSQLiteClientEx), exercising the constructor error path.
		dsn := &stubDataSource{path: "/nonexistent/path/that/cannot/be/created/test.db"}
		_, err := NewSQLiteClient(dsn, os.Stdout)
		require.Error(t, err)
	})
}

func TestSQLiteClient_Ping(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on valid client", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		require.NoError(t, c.Ping(t.Context()))
	})
	t.Run("returns error when migration table is absent", func(t *testing.T) {
		t.Parallel()
		// A freshly-created client without running the migrator has no
		// __schema_migrations table. Ping queries it, so it must fail.
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = mem.Close() })
		mem.SetMaxOpenConns(1)
		c, err := NewSQLiteClientEx(mem, os.Stdout)
		require.NoError(t, err)
		require.Error(t, c.Ping(t.Context()))
	})
	t.Run("returns error on closed db", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		mem.SetMaxOpenConns(1)
		c, err := NewSQLiteClientEx(mem, os.Stdout)
		require.NoError(t, err)
		require.NoError(t, mem.Close())
		require.Error(t, c.Ping(t.Context()))
	})
}

func TestSQLiteClient_Transaction(t *testing.T) {
	t.Parallel()

	t.Run("returns valid transaction", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		require.NotNil(t, tx)
		require.NoError(t, tx.Rollback())
	})
}

func TestSQLiteClient_Commit(t *testing.T) {
	t.Parallel()

	setupTable := func(t *testing.T, c *SQLiteClient, tableName string) {
		t.Helper()
		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), "CREATE TABLE IF NOT EXISTS "+tableName+" (id INTEGER PRIMARY KEY, val TEXT);")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}

	t.Run("single action is committed", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		setupTable(t, c, "test_commit_single")

		action := &execAction{sql: "INSERT INTO test_commit_single (val) VALUES ('hello');"}
		require.NoError(t, c.Commit(t.Context(), action))

		tx2, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx2.Rollback() }()

		var count int
		require.NoError(t, tx2.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM test_commit_single WHERE val = 'hello';").Scan(&count))
		require.Equal(t, 1, count)
	})
	t.Run("extra action is also committed", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		setupTable(t, c, "test_commit_extra")

		a1 := &execAction{sql: "INSERT INTO test_commit_extra (val) VALUES ('first');"}
		a2 := &execAction{sql: "INSERT INTO test_commit_extra (val) VALUES ('second');"}
		require.NoError(t, c.Commit(t.Context(), a1, a2))

		tx2, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx2.Rollback() }()

		var count int
		require.NoError(t, tx2.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM test_commit_extra;").Scan(&count))
		require.Equal(t, 2, count)
	})
	t.Run("primary action failure returns error", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		require.Error(t, c.Commit(t.Context(), &errAction{err: errors.New("primary failed")}))
	})
	t.Run("extra action failure returns error", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		setupTable(t, c, "test_commit_fail")

		a1 := &execAction{sql: "INSERT INTO test_commit_fail (val) VALUES ('first');"}
		a2 := &errAction{err: errors.New("action failed")}
		require.Error(t, c.Commit(t.Context(), a1, a2))

		tx2, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx2.Rollback() }()

		var count int
		require.NoError(t, tx2.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM test_commit_fail;").Scan(&count))
		require.Equal(t, 0, count)
	})
	t.Run("returns error when db is closed", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		mem.SetMaxOpenConns(1)
		c, err := NewSQLiteClientEx(mem, os.Stdout)
		require.NoError(t, err)
		require.NoError(t, mem.Close())
		require.Error(t, c.Commit(t.Context(), &execAction{sql: "SELECT 1;"}))
	})
}

func TestSQLiteClient_Rollback(t *testing.T) {
	t.Parallel()

	setupTable := func(t *testing.T, c *SQLiteClient, tableName string) {
		t.Helper()
		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), "CREATE TABLE IF NOT EXISTS "+tableName+" (id INTEGER PRIMARY KEY, val TEXT);")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}

	t.Run("single action is not persisted", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		setupTable(t, c, "test_rollback_single")

		action := &execAction{sql: "INSERT INTO test_rollback_single (val) VALUES ('world');"}
		require.NoError(t, c.Rollback(t.Context(), action))

		tx2, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx2.Rollback() }()

		var count int
		require.NoError(t, tx2.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM test_rollback_single WHERE val = 'world';").Scan(&count))
		require.Equal(t, 0, count)
	})
	t.Run("extra action is also not persisted", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		setupTable(t, c, "test_rollback_extra")

		a1 := &execAction{sql: "INSERT INTO test_rollback_extra (val) VALUES ('first');"}
		a2 := &execAction{sql: "INSERT INTO test_rollback_extra (val) VALUES ('second');"}
		require.NoError(t, c.Rollback(t.Context(), a1, a2))

		tx2, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx2.Rollback() }()

		var count int
		require.NoError(t, tx2.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM test_rollback_extra;").Scan(&count))
		require.Equal(t, 0, count)
	})
	t.Run("primary action failure returns error", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		require.Error(t, c.Rollback(t.Context(), &errAction{err: errors.New("primary failed")}))
	})
	t.Run("returns error when db is closed", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		mem.SetMaxOpenConns(1)
		c, err := NewSQLiteClientEx(mem, os.Stdout)
		require.NoError(t, err)
		require.NoError(t, mem.Close())
		require.Error(t, c.Rollback(t.Context(), &execAction{sql: "SELECT 1;"}))
	})
}

func TestSQLiteClient_Vacuum(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on valid client", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		require.NoError(t, c.Vacuum(t.Context()))
	})
}

func TestSQLiteClient_Close(t *testing.T) {
	t.Parallel()

	t.Run("closes successfully and makes db unusable", func(t *testing.T) {
		t.Parallel()
		mem, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		mem.SetMaxOpenConns(1)

		c, err := NewSQLiteClientEx(mem, os.Stdout)
		require.NoError(t, err)

		require.NoError(t, c.Close())
		require.Error(t, mem.PingContext(t.Context()))
	})
}

// execAction is a minimal sqlAction for tests.
type execAction struct{ sql string }

func (a *execAction) Run(tx *sql.Tx, ctx context.Context) error {
	_, err := tx.ExecContext(ctx, a.sql)
	return err
}

// errAction is a sqlAction that always returns the configured error.
type errAction struct{ err error }

func (a *errAction) Run(_ *sql.Tx, _ context.Context) error {
	return a.err
}

// mockFailCommitter simulates a committer whose Transaction call always fails.
type mockFailCommitter struct{ err error }

func (m *mockFailCommitter) Transaction(_ context.Context) (*sql.Tx, error) {
	return nil, m.err
}

// stubDataSource implements dsninjector.DataSource for testing by returning a
// fixed file path from Database().
var _ dsninjector.DataSource = (*stubDataSource)(nil)

type stubDataSource struct{ path string }

func (s *stubDataSource) Database() string                    { return s.path }
func (s *stubDataSource) Addr(_ ...int) string                { return "" }
func (s *stubDataSource) AuthBasicBase64() string             { return "" }
func (s *stubDataSource) Driver() string                      { return "sqlite" }
func (s *stubDataSource) Host() string                        { return "" }
func (s *stubDataSource) Login() string                       { return "" }
func (s *stubDataSource) Option(_ string, _ ...string) string { return "" }
func (s *stubDataSource) OptionsNames() []string              { return nil }
func (s *stubDataSource) Password() string                    { return "" }
func (s *stubDataSource) Port() int                           { return 0 }

// TestSQLitePoolPerConnectionPragmas guards against the pre-fix wiring where
// PRAGMA foreign_keys and busy_timeout were issued via db.Exec on a single pool
// connection, leaving the other six in production with the SQLite defaults. With
// DSN-based wiring every new pool connection picks up both PRAGMAs in the
// driver's Open hook. The test uses SetMaxOpenConns(N>1) so a regression surfaces
// here rather than as a non-deterministic production failure.
func TestSQLitePoolPerConnectionPragmas(t *testing.T) {
	t.Parallel()

	const poolSize = 4

	openPoolDB := func(t *testing.T) *sql.DB {
		t.Helper()
		// Shared-cache in-memory DB so every pool connection sees the same
		// database; the per-test name keeps parallel subtests from sharing
		// state.
		safeName := strings.ReplaceAll(t.Name(), "/", "_")
		dsn := connectionOptions(
			fmt.Sprintf("file:%s?mode=memory&cache=shared", safeName))
		db, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		db.SetMaxOpenConns(poolSize)
		return db
	}

	t.Run("foreign_keys and busy_timeout apply to every pool connection", func(t *testing.T) {
		t.Parallel()
		db := openPoolDB(t)
		ctx := t.Context()

		// Reserve every slot before reading PRAGMA values so each db.Conn opens a
		// fresh connection. With the pre-fix wiring all but one would report
		// foreign_keys=0 / busy_timeout=0.
		conns := make([]*sql.Conn, poolSize)
		for i := range poolSize {
			c, err := db.Conn(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })
			conns[i] = c
		}
		for i, c := range conns {
			var fk int
			require.NoError(t, c.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&fk))
			require.Equalf(t, 1, fk, "pool connection %d: foreign_keys not enabled", i)
			var bt int
			require.NoError(t, c.QueryRowContext(ctx, "PRAGMA busy_timeout;").Scan(&bt))
			require.Equalf(t, 5000, bt, "pool connection %d: busy_timeout not 5000ms", i)
		}
	})

	t.Run("orphan FK INSERT is rejected on every pool connection", func(t *testing.T) {
		t.Parallel()
		db := openPoolDB(t)
		ctx := t.Context()

		// Minimal schema. Two ExecContext calls because the modernc driver
		// can stop at the first terminator when multiple statements are
		// batched in one Exec — the production migrator splits per file
		// for the same reason.
		_, err := db.ExecContext(ctx, `CREATE TABLE rate_sources (name TEXT PRIMARY KEY);`)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `CREATE TABLE rate_values (
			id          TEXT PRIMARY KEY,
			source_name TEXT NOT NULL REFERENCES rate_sources(name) ON DELETE CASCADE,
			price       REAL NOT NULL
		);`)
		require.NoError(t, err)

		// Reserve every pool slot before writing anything, exactly as the sibling
		// subtest does, so each INSERT provably runs on its own connection —
		// without per-connection foreign_keys=ON, most would succeed silently.
		//
		// The reservation replaces an earlier barrier that held poolSize write
		// transactions open at once and raced their INSERTs. That shape stopped
		// being expressible with _txlock=immediate: the write lock is taken at
		// BEGIN, so only the first of those transactions can exist and the rest
		// block until the barrier they are themselves supposed to release. Holding
		// concurrent write transactions is not something the repository layer does
		// — every write opens, writes and commits inside one function — and it is
		// not what this subtest is about.
		conns := make([]*sql.Conn, poolSize)
		for i := range poolSize {
			c, err := db.Conn(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })
			conns[i] = c
		}
		for i, c := range conns {
			_, err := c.ExecContext(ctx,
				`INSERT INTO rate_values (id, source_name, price) VALUES (?, ?, ?);`,
				fmt.Sprintf("rv-%d-%d", i, time.Now().UnixNano()),
				"DOES_NOT_EXIST",
				100.0,
			)
			require.Errorf(t, err, "pool connection %d: INSERT with orphan source_name must be rejected", i)
			require.Containsf(t, strings.ToLower(err.Error()), "foreign key",
				"pool connection %d: expected FOREIGN KEY constraint failure, got: %v", i, err)
		}
	})
}

// TestWriteTransactionsWaitInsteadOfFailing pins _txlock=immediate, the reason
// busy_timeout reaches write transactions at all.
//
// The database is file-backed rather than the ":memory:" the other tests use:
// SetMaxOpenConns(1) cannot express two connections contending, and the assertions
// would pass vacuously.
//
// The first subtest is the regression: with the parameter removed it fails with
// SQLITE_BUSY in about a millisecond, which is both the error and the timing the
// production logs carried. The second does not fail today — reads are deferred
// either way — and is here as the guard for the other direction, so that widening
// immediate mode to every transaction shows up as a blocked reader rather than as
// a quiet loss of read concurrency.
func TestWriteTransactionsWaitInsteadOfFailing(t *testing.T) {
	t.Parallel()

	const holdFor = 200 * time.Millisecond

	newContendedClient := func(t *testing.T) *SQLiteClient {
		t.Helper()
		db, err := sql.Open("sqlite", connectionOptions(filepath.Join(t.TempDir(), "contended.db")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		db.SetMaxOpenConns(4)

		c, err := NewSQLiteClientEx(db, os.Stdout)
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), "CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT);")
		require.NoError(t, err)
		return c
	}

	// holdWriteLock opens a write transaction, writes, and signals once the WAL
	// write lock is actually held — after the INSERT, not after BEGIN, or the
	// contending side would be racing an unlocked database. release commits and
	// returns the outcome rather than asserting on it: one caller invokes it from
	// a timer goroutine, and require.NoError there would land FailNow off the test
	// goroutine.
	holdWriteLock := func(t *testing.T, c *SQLiteClient) (held <-chan struct{}, release func() error) {
		t.Helper()
		gate := make(chan struct{})
		unblock := make(chan struct{})
		outcome := make(chan error, 1)
		go func() {
			tx, err := c.Transaction(t.Context())
			if err != nil {
				close(gate)
				outcome <- err
				return
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO t (val) VALUES ('holder');"); err != nil {
				close(gate)
				outcome <- errors.Join(err, tx.Rollback())
				return
			}
			close(gate)
			<-unblock
			outcome <- tx.Commit()
		}()
		return gate, func() error {
			close(unblock)
			return <-outcome
		}
	}

	t.Run("contended write waits for the lock and commits", func(t *testing.T) {
		t.Parallel()
		c := newContendedClient(t)

		held, release := holdWriteLock(t, c)
		<-held
		// Released on a timer rather than on a handshake: this subtest asserts the
		// contending BEGIN blocks and then succeeds, so the lock has to go away on
		// its own while that BEGIN is already waiting.
		released := make(chan error, 1)
		go func() {
			time.Sleep(holdFor)
			released <- release()
		}()

		// The shape of every Retain* path: read to decide INSERT vs UPDATE, then
		// write. Under DEFERRED that write is a promotion and fails on the spot.
		tx, err := c.Transaction(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		var count int
		require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM t;").Scan(&count))
		_, err = tx.ExecContext(t.Context(), "INSERT INTO t (val) VALUES ('contender');")
		require.NoError(t, err, "write under contention must wait for the lock, not fail")
		require.NoError(t, tx.Commit())
		require.NoError(t, <-released)
	})

	t.Run("read-only transaction is not blocked by an open writer", func(t *testing.T) {
		t.Parallel()
		c := newContendedClient(t)

		held, release := holdWriteLock(t, c)
		<-held

		done := make(chan error, 1)
		go func() {
			tx, err := c.ReadOnlyTransaction(t.Context())
			if err != nil {
				done <- err
				return
			}
			var count int
			err = tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM t;").Scan(&count)
			if err == nil && count != 0 {
				err = fmt.Errorf("reader saw %d row(s); an uncommitted write must not be visible", count)
			}
			done <- errors.Join(err, tx.Rollback())
		}()

		// The writer is still open, so a reader that had taken the write lock could
		// only finish after release() — which does not run until this select does.
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("read-only transaction blocked behind an open writer")
		}
		require.NoError(t, release())
	})
}

// TestConnectionOptions covers the DSN assembly that carries the pragmas and the
// begin mode.
func TestConnectionOptions(t *testing.T) {
	t.Parallel()

	t.Run("carries the pragmas and immediate begin mode", func(t *testing.T) {
		t.Parallel()
		dsn := connectionOptions("/var/lib/beacon/beacon.sqlite")
		require.Contains(t, dsn, "_pragma=busy_timeout(5000)")
		require.Contains(t, dsn, "_pragma=foreign_keys(1)")
		require.Contains(t, dsn, "_txlock=immediate")
	})
	t.Run("appends to a path that already carries a query", func(t *testing.T) {
		t.Parallel()
		dsn := connectionOptions("file:beacon?mode=memory&cache=shared")
		require.Equal(t, 1, strings.Count(dsn, "?"))
		require.Contains(t, dsn, "cache=shared&_pragma=busy_timeout(5000)")
	})
}
