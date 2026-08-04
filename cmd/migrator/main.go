// Command migrator applies pending SQL migration files from the embedded
// migrations.MigrationsFS to the SQLite database pointed to by BEACON_SQLITEDB_DSN.
// It is idempotent: already-applied migration filenames are tracked in
// __schema_migrations and skipped on subsequent runs.
//
// When BEACON_SQLITEDB_ARCHIVE_DSN is set it also applies the archive tier's own
// chain (migrations.ArchiveMigrationsFS) to that database. The archive is a separate
// file with a separate schema and a separate ledger, so the two never share a
// migration; leaving the variable unset skips the archive entirely, which is what
// makes it safe to roll this binary out before the archive exists.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/prorochestvo/dsninjector"
	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/migrations"
	_ "modernc.org/sqlite"
)

var (
	// BuildVersion is the application version string, injected at link time via -ldflags.
	BuildVersion = "dev"
	// BuildTime is the build timestamp, injected at link time via -ldflags.
	BuildTime = "unknown"
	// BuildHash is the VCS commit hash, injected at link time via -ldflags.
	BuildHash = "undefined"
	// LogsDir is the directory where log files are written.
	LogsDir = path.Join(os.TempDir(), "logs")
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

const (
	envDsnSqliteDB = "BEACON_SQLITEDB_DSN"
	// envDsnSqliteDBArchive points at the history tier. Optional: unset means the
	// deployment has no archive yet and only the hot chain is applied.
	envDsnSqliteDBArchive = "BEACON_SQLITEDB_ARCHIVE_DSN"
)

func main() {
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)

	l, err := internal.NewLogger(LogsDir, "migrator", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	log.Println("logger: initiated")

	dsnDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		if env := os.Getenv(envDsnSqliteDB); env == "" {
			err = errors.Join(errors.New("environment variable is not set"), err)
		}
		log.Fatalf("settings: %s, %s", envDsnSqliteDB, err.Error())
		return
	}
	// The archive DSN is optional. Its absence is a valid deployment (no history
	// tier yet), so an unset variable skips the archive silently while a malformed
	// one is still fatal — a typo must not read as "no archive configured".
	var dsnArchive dsninjector.DataSource
	if env := os.Getenv(envDsnSqliteDBArchive); env != "" {
		dsnArchive, err = dsninjector.Unmarshal(envDsnSqliteDBArchive)
		if err != nil {
			log.Fatalf("settings: %s, %s", envDsnSqliteDBArchive, err.Error())
			return
		}
	}
	log.Println("settings: initiated")

	err = run(dsnDB, dsnArchive, l)
	if err != nil {
		log.Printf("migrator: %s", err)
		os.Exit(1)
	}
}

func run(dsnSQLiteDB, dsnArchive dsninjector.DataSource, logger *loginjector.Logger) (err error) {

	db, err := sqlitedb.NewSQLiteClient(dsnSQLiteDB, os.Stdout)
	if err != nil {
		return
	}
	defer func() {
		if e := db.Close(); e != nil {
			err = errors.Join(err, fmt.Errorf("close db: %w", e))
		}
	}()

	m, err := sqlitedb.NewMigrator(db, migrations.MigrationsFS)
	if err != nil {
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err = m.Run(ctx); err != nil {
		return
	}

	log.Printf("migrator: applied %d migration(s)", m.Applied())

	if dsnArchive == nil {
		return
	}

	// The archive runs its own chain against its own ledger. It is migrated after
	// the hot database and never inside the same transaction — they are separate
	// files, and SQLite gives no atomicity across them under WAL anyway.
	archiveDB, err := sqlitedb.NewSQLiteClient(dsnArchive, os.Stdout)
	if err != nil {
		err = fmt.Errorf("open archive db: %w", err)
		return
	}
	defer func() {
		if e := archiveDB.Close(); e != nil {
			err = errors.Join(err, fmt.Errorf("close archive db: %w", e))
		}
	}()

	am, err := sqlitedb.NewMigrator(archiveDB, migrations.ArchiveMigrationsFS)
	if err != nil {
		err = fmt.Errorf("archive migrator: %w", err)
		return
	}
	if err = am.Run(ctx); err != nil {
		err = fmt.Errorf("archive migrate: %w", err)
		return
	}

	log.Printf("migrator: applied %d archive migration(s)", am.Applied())

	return
}

func init() {
	logsDir := flag.String("logs-dir", LogsDir, "path to logs directory")
	verbosity := flag.String("verbosity", "warning", "minimum stdout log level (debug, info, warning, error, severe, critical)")
	flag.Parse()

	if dir := *logsDir; dir != "" {
		LogsDir = dir
	}

	if v := *verbosity; v != "" {
		LogVerbosity = internal.ParseLogLevel(*verbosity)
	}
}
