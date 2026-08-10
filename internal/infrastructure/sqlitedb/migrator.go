package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/prorochestvo/loginjector"
)

// NewMigrator creates a Migrator that will apply all .sql files from fsys
// (read via fs.ReadDir(fsys, ".")) followed by any migrations returned by the
// optional sources. Call Run to execute pending migrations.
func NewMigrator(db committer, fsys fs.FS, sources ...source) (*Migrator, error) {
	tx, err := db.Transaction(context.Background())
	if err != nil || tx == nil {
		if err == nil {
			err = errors.New("transaction is nil")
		}
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err = tx.Exec(sqlCreateTable()); err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	items, err := newDefaultMigrations(fsys)
	if err != nil {
		err = fmt.Errorf("load default migrations: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	for _, src := range sources {
		m, e := src.Migration()
		if e != nil {
			err = fmt.Errorf("load migrations: %w", e)
			err = errors.Join(err, loginjector.NewTraceError())
			return nil, err
		}

		if len(m) == 0 {
			continue
		}

		for k, v := range m {
			items = append(items, migration{name: k, content: v})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].name < items[j].name
	})

	return &Migrator{db: db, items: items}, nil
}

// Migrator runs migrations from one or more migrationSource implementations
// using a committer (e.g. *SQLiteClient) to execute each statement in a transaction.
type Migrator struct {
	db      committer
	items   []migration
	applied int
}

// Run executes all pending migration statements from every source in order.
func (m *Migrator) Run(ctx context.Context) error {
	tx, err := m.db.Transaction(ctx)
	if err != nil || tx == nil {
		if err == nil {
			err = errors.New("transaction is nil")
		}
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}
	defer func() { _ = tx.Rollback() }()

	applied := 0
	for i, item := range m.items { //nolint:varnamelen
		if len(item.content) == 0 {
			continue
		}

		var exists bool
		if err = tx.QueryRow(sqlLookupFileName(item.name)).Scan(&exists); err != nil {
			err = fmt.Errorf("migrations[%d]: check of the %s is failed, reason: %s", i, item.name, err.Error())
			err = errors.Join(err, loginjector.NewTraceError())
			return err
		}
		if exists {
			continue
		}

		log.Printf("migrator: applying %s", item.name)
		if _, err = tx.ExecContext(ctx, item.content); err != nil {
			err = fmt.Errorf("migrations[%d]: apply of the %s is failed, reason %s", i, item.name, err.Error())
			err = errors.Join(err, loginjector.NewTraceError())
			return err
		}

		if _, err = tx.ExecContext(ctx, sqlInsertFileName(item.name)); err != nil {
			err = fmt.Errorf("migrations[%d]: insert of the %s is failed, reason %s", i, item.name, err.Error())
			err = errors.Join(err, loginjector.NewTraceError())
			return err
		}
		applied++
	}

	if err = tx.Commit(); err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return err
	}

	m.applied = applied
	return nil
}

// Applied returns the number of migrations applied during the last Run call.
// It is exposed so cmd/migrator can log a meaningful count.
func (m *Migrator) Applied() int {
	return m.applied
}

// Verify reports whether the database's recorded migration state matches the set this
// Migrator was built from. It is Run's post-condition, checked separately so a schema
// that does not match the shipped binary fails the deploy loudly instead of surfacing
// weeks later as a "no such column" error in production.
//
// Two conditions fail verification:
//
//   - A migration is absent from __schema_migrations. Immediately after a successful Run
//     that can only mean the database is not the one Run wrote to: a stale DSN, a restore
//     from an older snapshot, or a hand-edited ledger.
//   - A migration file is empty. Run skips empty content silently, so a truncated or
//     accidentally-blank .sql would be a permanent no-op that nothing ever reports. Here
//     it is a failed deploy instead.
//
// Every offending filename is collected before returning, so one run reports the whole
// picture rather than the alphabetically-first problem.
func (m *Migrator) Verify(ctx context.Context) error {
	var tx *sql.Tx
	var err error
	if ro, ok := m.db.(interface {
		ReadOnlyTransaction(context.Context) (*sql.Tx, error)
	}); ok {
		tx, err = ro.ReadOnlyTransaction(ctx)
	} else {
		tx, err = m.db.Transaction(ctx)
	}
	if err != nil || tx == nil {
		if err == nil {
			err = errors.New("transaction is nil")
		}
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, "SELECT filename FROM "+migrationTableName+";")
	if err != nil {
		return errors.Join(
			fmt.Errorf("verify: read %s: %w", migrationTableName, err),
			loginjector.NewTraceError(),
		)
	}
	defer func() { _ = rows.Close() }()

	recorded := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return errors.Join(fmt.Errorf("verify: scan filename: %w", err), loginjector.NewTraceError())
		}
		recorded[name] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return errors.Join(fmt.Errorf("verify: iterate %s: %w", migrationTableName, err), loginjector.NewTraceError())
	}

	var empty, missing []string
	for _, item := range m.items {
		if len(item.content) == 0 {
			empty = append(empty, item.name)
			continue
		}
		if _, ok := recorded[item.name]; !ok {
			missing = append(missing, item.name)
		}
	}

	var problems []error
	if len(empty) > 0 {
		problems = append(problems, fmt.Errorf(
			"empty migration file(s), which apply nothing and are never recorded: %s",
			strings.Join(empty, ", ")))
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Errorf(
			"migration(s) not recorded in %s: %s",
			migrationTableName, strings.Join(missing, ", ")))
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	return nil
}

// Committer is the minimal DB interface required by NewMigrator.
type Committer interface {
	Transaction(context.Context) (*sql.Tx, error)
}

const (
	migrationTableName = "__schema_migrations"
)

type source interface {
	Migration() (map[string]string, error)
}

// committer is kept as a type alias so internal call sites are unaffected.
type committer = Committer

// migration is a sqlAction that executes a single SQL statement.
type migration struct {
	name    string
	content string
}

// RequireMigratedSchema returns nil only when __schema_migrations exists and
// has at least one row. Service binaries call it right after opening the DB so a
// missing migrator step surfaces as a loud startup failure rather than a
// confusing "no such table" error at the first query.
func RequireMigratedSchema(ctx context.Context, db Committer) error {
	var tx *sql.Tx
	var err error
	if ro, ok := db.(interface {
		ReadOnlyTransaction(context.Context) (*sql.Tx, error)
	}); ok {
		tx, err = ro.ReadOnlyTransaction(ctx)
	} else {
		tx, err = db.Transaction(ctx)
	}
	if err != nil || tx == nil {
		if err == nil {
			err = errors.New("transaction is nil")
		}
		return errors.Join(err, loginjector.NewTraceError())
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+migrationTableName).Scan(&count); err != nil {
		return errors.Join(
			fmt.Errorf("schema not initialised: run cmd/migrator before starting the service: %w", err),
			loginjector.NewTraceError(),
		)
	}
	if count == 0 {
		return errors.New("schema not initialised: run cmd/migrator before starting the service")
	}
	return nil
}

func newDefaultMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		err = fmt.Errorf("read migrations dir: %w", err)
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}

	items := make([]migration, 0, len(entries))

	for _, entry := range entries {
		fileName := entry.Name()

		if entry.IsDir() || !strings.HasSuffix(fileName, ".sql") {
			continue
		}

		var content []byte
		content, err = fs.ReadFile(fsys, fileName)
		if err != nil {
			err = fmt.Errorf("read migration %s: %w", fileName, err)
			err = errors.Join(err, loginjector.NewTraceError())
			return nil, err
		}

		// An empty file is kept in the list rather than dropped here: Run already
		// skips empty content, and keeping the entry is what lets Verify report the
		// file as a silent no-op instead of it vanishing before anyone can notice.
		items = append(items, migration{
			name:    fileName,
			content: string(content),
		})
	}

	return items, nil
}

func sqlCreateTable() string {
	return "CREATE TABLE IF NOT EXISTS" + " " + migrationTableName + " (filename TEXT PRIMARY KEY, applied_at TEXT NOT NULL);"
}

func sqlLookupFileName(fileName string) string {
	fileName = strings.ReplaceAll(fileName, "'", "''")
	return "SELECT COUNT(*) > 0 FROM" + " " + migrationTableName + " WHERE filename = '" + fileName + "';"
}

func sqlInsertFileName(fileName string) string {
	fileName = strings.ReplaceAll(fileName, "'", "''")
	now := strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339), "'", "''")
	return "INSERT INTO" + " " + migrationTableName + " (filename, applied_at) VALUES ('" + fileName + "', '" + now + "');"
}
