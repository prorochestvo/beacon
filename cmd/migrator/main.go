// Command migrator applies pending SQL migration files from the embedded
// migrations.MigrationsFS to the SQLite database pointed to by BEACON_SQLITEDB_DSN.
// It is idempotent: already-applied migration filenames are tracked in
// __schema_migrations and skipped on subsequent runs.
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

func main() {
	flag.Parse()
	initFlags()

	l, err := internal.NewLogger(LogsDir, "migrator", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	// First line of every run in the log file: which build wrote everything below.
	// Logged after the logger exists — before it, this went to a stderr the cron
	// wrappers discard, so no line in the file could be attributed to a release.
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)
	log.Println("logger: initiated")

	dsnDB, err := dsninjector.Unmarshal(internal.EnvSQLiteDSN)
	if err != nil {
		if env := os.Getenv(internal.EnvSQLiteDSN); env == "" {
			err = errors.Join(errors.New("environment variable is not set"), err)
		}
		log.Fatalf("settings: %s: unparseable value (contents not logged)", internal.EnvSQLiteDSN)
		return
	}
	log.Println("settings: initiated")

	err = run(dsnDB, l)
	if err != nil {
		log.Printf("migrator: %s", err)
		os.Exit(1)
	}
}

func run(dsnSQLiteDB dsninjector.DataSource, logger *loginjector.Logger) (err error) {

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

	// Post-condition: the ledger must now account for every embedded migration. A
	// non-zero exit here fails `systemctl start beacon-migrate`, which fails the deploy
	// step — so a database that does not match the shipped binary is caught at release
	// time rather than weeks later at the first query against a missing column.
	if err = m.Verify(ctx); err != nil {
		err = fmt.Errorf("schema verification failed after migrate: %w", err)
		return
	}

	log.Println("migrator: schema verified against the embedded migration set")

	return
}

// flagLogsDir and flagVerbosity hold the raw flag values populated by flag.Parse in
// main. They are package-level so initFlags can apply them after parsing.
var (
	flagLogsDir   *string
	flagVerbosity *string
)

func init() {
	// Register flags here so the test binary can see them, but do NOT call flag.Parse()
	// in init() — it would consume go test's own flags before the testing package
	// registers them ("flag provided but not defined"). main() calls flag.Parse() once;
	// tests never invoke main().
	flagLogsDir = flag.String("logs-dir", LogsDir, "path to logs directory")
	flagVerbosity = flag.String("verbosity", "warning", "minimum stdout log level (debug, info, warning, error, severe, critical)")
}

// initFlags applies the parsed flag values. Called from main after flag.Parse.
func initFlags() {
	if dir := *flagLogsDir; dir != "" {
		LogsDir = dir
	}

	if v := *flagVerbosity; v != "" {
		LogVerbosity = internal.ParseLogLevel(v)
	}
}
