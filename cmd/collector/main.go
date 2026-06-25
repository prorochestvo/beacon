// Command collector polls all active rate sources on a configurable schedule,
// extracts exchange-rate values, and persists them to the SQLite database.
//
// It reads SQLITEDB_DSN from the environment. Outbound HTTP/HTTPS traffic from
// plain and chromedp sources routes through PROXY_URL (format: http://<host>:<port>,
// parsed via dsninjector); when unset or empty, traffic goes direct. Telegram Bot
// API traffic bypasses the proxy via a hardcoded transport in
// internal/infrastructure/telegrambot.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/collection"
	"github.com/seilbekskindirov/monitor/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/monitor/internal/repository"
	"github.com/seilbekskindirov/monitor/internal/tools/proxyutil"
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
	// ChromiumPath is the absolute path to the Chromium/Chrome binary read from
	// CHROMIUM_PATH. When empty, chromedp searches PATH (chromium, chromium-browser,
	// google-chrome, chrome).
	ChromiumPath = os.Getenv(envChromiumPath)
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

const (
	envProxyURL = "PROXY_URL"
	// envChromiumPath is an optional absolute path to the Chromium/Chrome binary;
	// when unset, chromedp searches PATH for chromedp-kind sources.
	envChromiumPath = "CHROMIUM_PATH"
	envDsnSqliteDB  = "SQLITEDB_DSN"
)

func main() {
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)

	l, err := internal.NewLogger(LogsDir, "collector", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	log.Println("logger: initiated")

	proxyURL := proxyutil.ResolveURL(envProxyURL)

	// Preserve the startup-marker sequence (logger -> settings ->
	// dependencies -> repositories -> runners) that operators grep on.
	dsnDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnSqliteDB, err.Error())
	}
	log.Println("settings: initiated")

	db, err := sqlitedb.NewSQLiteClient(dsnDB, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: %s", err.Error())
	}
	if err = sqlitedb.RequireMigratedSchema(context.Background(), db); err != nil {
		log.Fatalf("dependencies: schema check: %s", err.Error())
	}
	defer func(c io.Closer) {
		if e := c.Close(); e != nil {
			log.Printf("close sqlite client: %v", e)
		}
	}(db)
	log.Println("dependencies: initiated")

	sourceRepo, err := repository.NewRateSourceRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	historyRepo, err := repository.NewExecutionHistoryRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	rateValueRepo, err := repository.NewRateValueRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	log.Println("repositories: initiated")

	runners, err := buildRunners(sourceRepo, historyRepo, rateValueRepo, proxyURL, l.WriterAs(internal.LogLevelWarning))
	if err != nil {
		log.Fatalf("runners: runners building is failed: %s", err)
		return
	}
	log.Println("runners: initiated")

	// SIGTERM and SIGINT cancel ctx mid-run so an in-flight tick aborts the
	// next source fetch instead of the OS killing the process between
	// transactions. The migrator uses the same pattern.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make([]error, 0, len(runners))
	for _, r := range runners {
		// Skip context.Canceled to avoid duplicating the shutdown reason across
		// two log lines (the only deadline here is the OS signal).
		//
		// Panic recovery replaces the removed scheduler package's per-job
		// defer-recover, so one bad source doesn't crash the whole tick.
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					stackErr := internal.NewStackTraceError()
					log.Printf("execution: runner panic recovered: %v\n%s", rec, stackErr.Error())
					errs = append(errs, fmt.Errorf("runner panic: %v", rec))
				}
			}()
			if rerr := r.Run(ctx); rerr != nil && !errors.Is(rerr, context.Canceled) {
				errs = append(errs, rerr)
			}
		}()
	}
	if err = errors.Join(errs...); err != nil {
		log.Printf("execution: completed with errors: %s", err)
	}
	if ctx.Err() != nil {
		log.Printf("execution: stopped by signal: %s", ctx.Err())
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

// runner is the minimal interface the collector needs from each agent.
// One Run call per binary invocation; the loop in main wraps each call in a
// panic-recover shim.
type runner interface {
	Run(context.Context) error
}

func buildRunners(
	source *repository.RateSourceRepository,
	history *repository.ExecutionHistoryRepository,
	value *repository.RateValueRepository,
	proxyURL string,
	logger io.Writer,
) ([]runner, error) {
	collectionRateAgent, err := collection.NewRateAgent(
		proxyURL,
		ChromiumPath,
		source,
		history,
		value,
		logger,
	)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return []runner{collectionRateAgent}, nil
}
