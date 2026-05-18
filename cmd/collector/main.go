// Command collector polls all active rate sources on a configurable schedule,
// extracts exchange-rate values, and persists them to the SQLite database.
// It reads SQLITEDB_DSN from the environment and supports an optional HTTP proxy
// via the PROXY_URL environment variable.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"path"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/collection"
	"github.com/seilbekskindirov/monitor/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/monitor/internal/repository"
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
	// ProxyURL is the HTTP proxy URL read from the PROXY_URL environment variable.
	ProxyURL = os.Getenv(envProxyUrl)
	// ChromiumPath is the absolute path to the Chromium/Chrome binary read from
	// CHROMIUM_PATH. When empty, chromedp searches PATH (chromium, chromium-browser,
	// google-chrome, chrome) on first use.
	ChromiumPath = os.Getenv(envChromiumPath)
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

const (
	envProxyUrl    = "PROXY_URL"
	envDsnSqliteDB = "SQLITEDB_DSN"
	// envChromiumPath is an optional absolute path to the Chromium/Chrome binary.
	// When unset, chromedp searches PATH on first use for a chromedp-kind source.
	envChromiumPath = "CHROMIUM_PATH"
)

func main() {
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)

	// init logger
	l, err := internal.NewLogger(LogsDir, "collector", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	log.Println("logger: initiated")

	// init settings
	dsnSQLiteDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		if env := os.Getenv(envDsnSqliteDB); env == "" {
			err = errors.Join(errors.New("environment variable is not set"), err)
		}
		log.Fatalf("settings: %s, %s", envDsnSqliteDB, err.Error())
		return
	}
	log.Println("settings: initiated")

	// init dependencies
	db, err := sqlitedb.NewSQLiteClient(dsnSQLiteDB, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: sqlite connection is failed, %s", err.Error())
		return
	}
	defer func(c io.Closer) {
		if e := c.Close(); e != nil {
			log.Printf("close sqlite client: %v", e)
		}
	}(db)
	if err = sqlitedb.RequireMigratedSchema(context.Background(), db); err != nil {
		log.Fatalf("schema check: %s", err.Error())
	}
	log.Println("dependencies: initiated")

	// init repositories
	rRateSource, err := repository.NewRateSourceRepository(db)
	if err != nil {
		log.Fatalf("repositories: rate source build is failed, %s", err.Error())
		return
	}
	rExecutionHistory, err := repository.NewExecutionHistoryRepository(db)
	if err != nil {
		log.Fatalf("repositories: execution history build is failed, %s", err.Error())
		return
	}
	rRateValue, err := repository.NewRateValueRepository(db)
	if err != nil {
		log.Fatalf("repositories: rate value build is failed, %s", err.Error())
		return
	}
	log.Println("repositories: initiated")

	runners, err := buildRunners(
		rRateSource,
		rExecutionHistory,
		rRateValue,
		l.WriterAs(internal.LogLevelWarning),
	)
	if err != nil {
		log.Fatalf("runners: runners building is failed: %s", err)
		return
	}
	log.Println("runners: initiated")

	ctx := context.Background()

	errs := make([]error, 0, len(runners))
	for _, r := range runners {
		if err = r.Run(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err = errors.Join(errs...); err != nil {
		log.Fatalf("execution:  %s", err)
	}

	log.Println("execution: done")
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

// runner is the minimal interface that the scheduler needs from each agent.
type runner interface {
	Run(context.Context) error
}

func buildRunners(
	rRateSource *repository.RateSourceRepository,
	rExecutionHistory *repository.ExecutionHistoryRepository,
	rRateValue *repository.RateValueRepository,
	logger io.Writer,
) ([]runner, error) {
	collectionRateAgent, err := collection.NewRateAgent(
		ProxyURL,
		ChromiumPath,
		rRateSource,
		rExecutionHistory,
		rRateValue,
		logger,
	)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return []runner{collectionRateAgent}, nil
}
