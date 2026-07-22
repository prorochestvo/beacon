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

// Committer is the minimal DB interface required by NewMigrator.
type Committer interface {
	Transaction(context.Context) (*sql.Tx, error)
}

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

type source interface {
	Migration() (map[string]string, error)
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

const (
	migrationTableName = "__schema_migrations"
)

// committer is kept as a type alias so internal call sites are unaffected.
type committer = Committer

// migration is a sqlAction that executes a single SQL statement.
type migration struct {
	name    string
	content string
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

		if len(content) == 0 {
			continue
		}

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
