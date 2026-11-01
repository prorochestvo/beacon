package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb/sqlitedbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// errIterInterrupted stands in for the failure sql.Rows.Err exists to report: a
// connection dropped, a context cancelled, or a driver decode error part-way through
// a result set. The driver signals it by returning a non-io.EOF error from Next, which
// database/sql stores and surfaces only via Err — Next itself just returns false, exactly
// as it does on a genuinely exhausted result set.
var errIterInterrupted = errors.New("iteration interrupted")

var (
	_ driver.Connector          = (*iterFailConnector)(nil)
	_ driver.Conn               = (*iterFailConn)(nil)
	_ driver.ConnBeginTx        = (*iterFailConn)(nil)
	_ driver.ConnPrepareContext = (*iterFailConn)(nil)
	_ driver.QueryerContext     = (*iterFailConn)(nil)
	_ driver.ExecerContext      = (*iterFailConn)(nil)
	_ driver.Stmt               = (*iterFailStmt)(nil)
	_ driver.StmtQueryContext   = (*iterFailStmt)(nil)
	_ driver.StmtExecContext    = (*iterFailStmt)(nil)
	_ driver.Rows               = (*iterFailRows)(nil)
)

// iterFailConnector opens real modernc SQLite connections and truncates every result
// set after failAfter rows once armed. Arming is deferred so schema migration and
// fixture seeding run against an undisturbed driver.
//
// A connector rather than a registered driver name: sql.Register is global and panics
// on a duplicate, so each test would have to invent a name and leak it for the life of
// the process. sql.OpenDB takes the connector directly, which keeps the armed flag
// per-test.
type iterFailConnector struct {
	base      driver.Driver
	dsn       string
	armed     *atomic.Bool
	failAfter int
}

func (c *iterFailConnector) Connect(_ context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &iterFailConn{Conn: conn, owner: c}, nil
}

func (c *iterFailConnector) Driver() driver.Driver { return c.base }

type iterFailConn struct {
	driver.Conn
	owner *iterFailConnector
}

func (c *iterFailConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	// Forwarding this is not optional: ReadOnlyTransaction passes
	// sql.TxOptions{ReadOnly: true}, and database/sql rejects that outright unless the
	// driver implements ConnBeginTx.
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // fallback for a driver without ConnBeginTx
}

func (c *iterFailConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		s, err := p.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &iterFailStmt{Stmt: s, owner: c.owner}, nil
	}
	s, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &iterFailStmt{Stmt: s, owner: c.owner}, nil
}

func (c *iterFailConn) Prepare(query string) (driver.Stmt, error) {
	s, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &iterFailStmt{Stmt: s, owner: c.owner}, nil
}

func (c *iterFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		// ErrSkip sends database/sql down the PrepareContext path, which is wrapped too.
		return nil, driver.ErrSkip
	}
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &iterFailRows{Rows: rows, owner: c.owner}, nil
}

func (c *iterFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, query, args)
}

type iterFailStmt struct {
	driver.Stmt
	owner *iterFailConnector
}

// Statement-level fallbacks go through the non-context methods rather than returning
// driver.ErrSkip. database/sql handles ErrSkip only on the two conn-level fast paths;
// ctxDriverStmtQuery and ctxDriverStmtExec pass it straight back, so a statement wrapper
// answering ErrSkip would surface as a hard "driver: skip fast-path" query error instead
// of a fallback. Unreachable with modernc, which implements both context interfaces.
func (s *iterFailStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	var (
		rows driver.Rows
		err  error
	)
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err = q.QueryContext(ctx, args)
	} else {
		rows, err = s.Stmt.Query(positionalArgs(args)) //nolint:staticcheck // fallback for a driver without StmtQueryContext
	}
	if err != nil {
		return nil, err
	}
	return &iterFailRows{Rows: rows, owner: s.owner}, nil
}

func (s *iterFailStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := s.Stmt.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, args)
	}
	return s.Stmt.Exec(positionalArgs(args)) //nolint:staticcheck // fallback for a driver without StmtExecContext
}

// positionalArgs drops the names, which is lossless here: every query in this repository
// binds with "?" placeholders.
func positionalArgs(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, a := range args {
		values[i] = a.Value
	}
	return values
}

type iterFailRows struct {
	driver.Rows
	owner     *iterFailConnector
	delivered int
}

func (r *iterFailRows) Next(dest []driver.Value) error {
	if r.owner.armed.Load() && r.delivered >= r.owner.failAfter {
		return errIterInterrupted
	}
	if err := r.Rows.Next(dest); err != nil {
		return err
	}
	r.delivered++
	return nil
}

// stubTruncatingDB returns a migrated in-memory database whose result sets stop after
// failAfter rows, plus the arming switch. Nothing truncates until the switch is set, so
// callers can migrate and seed normally and then arm immediately before the call under
// test.
//
// failAfter must be at least 1: the *Count helpers that precede every paged read use
// QueryRowContext, which needs one row to survive.
func stubTruncatingDB(t *testing.T, failAfter int) (*sqlitedb.SQLiteClient, *atomic.Bool) {
	t.Helper()
	require.GreaterOrEqual(t, failAfter, 1, "failAfter below 1 breaks the count queries, not the iteration")

	armed := &atomic.Bool{}

	// Same mutex, same phase as stubSQLiteDB: this helper repeats that open + PRAGMA +
	// migrate sequence, so it has to serialise where the original does. Arming happens
	// after, and deliberately outside the lock.
	mu.Lock()
	defer mu.Unlock()

	probe, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	base := probe.Driver()
	require.NoError(t, probe.Close())

	mem := sql.OpenDB(&iterFailConnector{
		base:      base,
		dsn:       ":memory:",
		armed:     armed,
		failAfter: failAfter,
	})
	t.Cleanup(func() { _ = mem.Close() })
	mem.SetMaxOpenConns(1)

	db, err := sqlitedb.NewSQLiteClientEx(mem)
	require.NoError(t, err)
	sqlitedbtest.Apply(t, db)

	return db, armed
}

// TestRepositoryReadsReportInterruptedIteration is the regression guard for #71. Both
// subtests seed more rows than the driver will deliver, so the pre-fix behaviour is a
// short slice with a nil error — a truncated read indistinguishable from a small one.
func TestRepositoryReadsReportInterruptedIteration(t *testing.T) {
	t.Parallel()

	// weatherUserCityQueryContext: an unnamed-return read on the path that feeds the
	// notifier's due-city sweep, where a silent truncation skips users and reports nothing.
	t.Run("weather user cities", func(t *testing.T) {
		t.Parallel()
		db, armed := stubTruncatingDB(t, 1)

		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		const userID = "1001"
		for _, name := range []string{"Almaty", "Astana", "Shymkent"} {
			city := &domain.WeatherUserCity{
				UserType:    domain.UserTypeTelegram,
				UserID:      userID,
				LocationID:  name,
				DisplayName: name,
				Latitude:    43.2,
				Longitude:   76.9,
				Timezone:    "Asia/Almaty",
				NotifyKind:  domain.WeatherNotifyMorningSummary,
				NotifyHour:  7,
			}
			require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))
		}

		// Sanity: undisturbed, the read returns everything.
		before, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, userID)
		require.NoError(t, err)
		require.Len(t, before, 3)

		armed.Store(true)
		got, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, userID)

		require.Error(t, err, "an interrupted read must not pass for a complete one")
		require.ErrorIs(t, err, errIterInterrupted)
		assert.Nil(t, got, "no caller should be handed the rows that did arrive")
	})

	// rateValueQueryContext: the named-return helper behind history and chart reads,
	// where a short result set renders as a real gap in the series.
	t.Run("rate values", func(t *testing.T) {
		t.Parallel()
		db, armed := stubTruncatingDB(t, 1)

		const sourceName = "iterfail-source"
		seedRateSources(t, db, sourceName)

		repo, err := NewRateValueRepository(db)
		require.NoError(t, err)

		for i := range 3 {
			require.NoError(t, repo.RetainRateValue(t.Context(), &domain.RateValue{
				SourceName:    sourceName,
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Price:         float64(500 + i),
			}))
		}

		before, err := repo.ObtainLastNRateValuesBySourceName(t.Context(), sourceName, 10)
		require.NoError(t, err)
		require.Len(t, before, 3)

		armed.Store(true)
		got, err := repo.ObtainLastNRateValuesBySourceName(t.Context(), sourceName, 10)

		require.Error(t, err, "an interrupted read must not pass for a complete one")
		require.ErrorIs(t, err, errIterInterrupted)
		assert.Nil(t, got, "no caller should be handed the rows that did arrive")
	})
}
