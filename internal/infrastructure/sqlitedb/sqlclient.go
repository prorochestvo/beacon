// Package sqlitedb provides a SQLite client and migration runner for the application.
// The client wraps *sql.DB with explicit transaction helpers (Commit/Rollback) and
// enforces WAL journal mode and foreign-key checks via PRAGMA on first open.
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
	_ "modernc.org/sqlite"
)

// NewSQLiteClient opens a SQLite database at the path encoded in sqlDSN,
// applies WAL mode and foreign-key PRAGMAs, sets the connection pool to seven
// connections, and sets a one-minute query timeout.
// Close must be called on the returned client when it is no longer needed.
func NewSQLiteClient(sqlDSN dsninjector.DataSource, logger io.Writer) (*SQLiteClient, error) {
	db, err := sql.Open("sqlite", sqlDSN.Database())
	if err != nil {
		err = fmt.Errorf("open sqlite: %w", err)
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	db.SetMaxOpenConns(7)

	c, err := NewSQLiteClientEx(db, logger)
	if err != nil {
		err = errors.Join(err, db.Close())
		err = errors.Join(err, fmt.Errorf("initialize sqlite client: %w", err))
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}

	c.Timeout = time.Minute

	return c, nil
}

// NewSQLiteClientEx initialises a SQLiteClient from an already-open *sql.DB.
// It applies WAL mode and foreign-key PRAGMAs and sets a 30-second default timeout.
// Use this in tests or when the caller controls the *sql.DB lifecycle.
func NewSQLiteClientEx(db *sql.DB, logger io.Writer) (*SQLiteClient, error) {
	const pragma = "PRAGMA"
	if _, err := db.Exec(pragma + " foreign_keys=ON;\n" + pragma + " journal_mode=WAL;"); err != nil {
		err = fmt.Errorf("set pragmas: %w", err)
		err = errors.Join(err, db.Close())
		err = errors.Join(err, internal.NewStackTraceError())
		return nil, err
	}
	return &SQLiteClient{
		db:      db,
		Timeout: 30 * time.Second,
		logger:  logger,
	}, nil
}

// SQLiteClient wraps a *sql.DB and provides a managed SQLite connection.
type SQLiteClient struct {
	db      *sql.DB
	logger  io.Writer
	Timeout time.Duration
}

// Ping verifies the database connection is still alive.
func (sqlite *SQLiteClient) Ping(ctx context.Context) error {
	if err := sqlite.db.PingContext(ctx); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	if err := sqlite.Rollback(ctx, &Ping{}); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// Transaction opens a read-write transaction. The caller is responsible for
// committing or rolling back the returned *sql.Tx.
func (sqlite *SQLiteClient) Transaction(ctx context.Context) (*sql.Tx, error) {
	return sqlite.db.BeginTx(ctx, nil)
}

// ReadOnlyTransaction opens a read-only transaction. Callers that only run
// SELECTs (e.g. health checks, schema validation) should prefer it over
// Transaction so the intent is explicit at the call site.
func (sqlite *SQLiteClient) ReadOnlyTransaction(ctx context.Context) (*sql.Tx, error) {
	return sqlite.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

// Commit runs action and each extraAction inside a single transaction and commits.
// Returns on the first error; the transaction is rolled back automatically via defer.
func (sqlite *SQLiteClient) Commit(ctx context.Context, action sqlAction, extraActions ...sqlAction) error {
	ctx, cancel := context.WithTimeout(ctx, sqlite.Timeout)
	defer cancel()

	tx, err := sqlite.db.BeginTx(ctx, nil)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer func(tx interface{ Rollback() error }) { _ = tx.Rollback() }(tx)

	err = action.Run(tx, ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	for _, a := range extraActions {
		if err = a.Run(tx, ctx); err != nil {
			err = errors.Join(err, internal.NewStackTraceError())
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// Rollback runs action and each extraAction inside a transaction that is always
// rolled back, regardless of errors. Use this for read-only operations where a
// deliberate rollback is preferred over commit to avoid any unintended writes.
func (sqlite *SQLiteClient) Rollback(ctx context.Context, action sqlAction, extraActions ...sqlAction) error {
	ctx, cancel := context.WithTimeout(ctx, sqlite.Timeout)
	defer cancel()

	tx, err := sqlite.db.BeginTx(ctx, nil)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}
	defer func(tx interface{ Rollback() error }) { _ = tx.Rollback() }(tx)

	err = action.Run(tx, ctx)
	if err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	for _, a := range extraActions {
		if err = a.Run(tx, ctx); err != nil {
			err = errors.Join(err, internal.NewStackTraceError())
			return err
		}
	}

	if err = tx.Rollback(); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// Vacuum runs VACUUM to reclaim unused space from the SQLite file.
func (sqlite *SQLiteClient) Vacuum(ctx context.Context) error {
	_, err := sqlite.db.ExecContext(ctx, "VACUUM;")
	return err
}

// Close closes the underlying database connection.
func (sqlite *SQLiteClient) Close() error {
	return sqlite.db.Close()
}

// Ping is a sqlAction that validates the DB connection by querying the migration
// table row count. It is used internally by SQLiteClient.Ping.
type Ping struct{}

func (_ *Ping) Run(tx *sql.Tx, ctx context.Context) error {
	var count int

	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM"+" "+migrationTableName).Scan(&count); err != nil {
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	if count < 0 {
		err := fmt.Errorf("invalid count: %d", count)
		err = errors.Join(err, internal.NewStackTraceError())
		return err
	}

	return nil
}

// sqlAction is implemented by types that can execute SQL inside an open transaction.
type sqlAction interface {
	Run(*sql.Tx, context.Context) error
}
