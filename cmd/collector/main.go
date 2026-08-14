// Command collector polls all active rate sources on a configurable schedule,
// extracts exchange-rate values, and persists them to the SQLite database.
//
// It reads BEACON_SQLITEDB_DSN from the environment. Outbound traffic is direct by
// default: BEACON_PROXY_URL is consulted, but it only makes a proxy available — a rate
// source reaches it solely by setting options.use_proxy, so configuring the variable
// alone routes nothing. See the collection-egress note in CLAUDE.md for the measurements
// behind that default. cmd/doctor honours the proxy unconditionally, for AI provider
// calls and its chromedp fetcher.
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
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/application/collection"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	weatherinfra "github.com/seilbekskindirov/beacon/internal/infrastructure/weather"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/seilbekskindirov/beacon/internal/tools/proxyutil"
	_ "modernc.org/sqlite"
)

func main() {
	flag.Parse()
	initFlags()

	l, err := internal.NewLogger(LogsDir, "collector", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	// First line of every run in the log file: which build wrote everything below.
	// Logged after the logger exists — before it, this went to a stderr the cron
	// wrappers discard, so no line in the file could be attributed to a release.
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)
	log.Println("logger: initiated")

	// Preserve the startup-marker sequence (logger -> settings ->
	// dependencies -> repositories -> runners) that operators grep on.
	dsnDB, err := dsninjector.Unmarshal(internal.EnvSQLiteDSN)
	if err != nil {
		log.Fatalf("settings: %s: unparseable value (contents not logged)", internal.EnvSQLiteDSN)
	}
	ProxyURL = proxyutil.ResolveURL(internal.EnvProxyURL)
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
	weatherCityRepo, err := repository.NewWeatherUserCityRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	weatherObsRepo, err := repository.NewWeatherObservationRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	metaRepo, err := repository.NewServiceMetaRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	log.Println("repositories: initiated")

	maintenanceAgent, err := collection.NewMaintenanceAgent(
		metaRepo, db,
		collection.DefaultHotWindow, collection.DefaultArchiveRetention, collection.DefaultVacuumInterval,
		l.WriterAs(internal.LogLevelInfo),
		rateValueRepo, historyRepo,
	)
	if err != nil {
		log.Fatalf("runners: maintenance agent: %s", err.Error())
	}

	runners, err := buildRunners(
		sourceRepo, historyRepo, rateValueRepo,
		weatherCityRepo, weatherObsRepo,
		l.WriterAs(internal.LogLevelWarning),
	)
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
					stackErr := loginjector.NewStackTraceError()
					log.Printf("execution: runner panic recovered: %v\n%s\n%s", rec, loginjector.StackRedacted(stackErr), stackErr.Runtime())
					errs = append(errs, fmt.Errorf("runner panic: %v", rec))
				}
			}()
			if rerr := r.Run(ctx); rerr != nil && !errors.Is(rerr, context.Canceled) {
				errs = append(errs, rerr)
			}
		}()
	}
	runErr := errors.Join(errs...)
	if runErr != nil {
		log.Printf("execution: completed with errors: %s", runErr)
	}

	// Vacuum stale weather observations to prevent unbounded table growth.
	// Each collector tick stores new hourly_json rows; without pruning, the table
	// accumulates indefinitely. Non-fatal: a vacuum failure does not abort the run.
	if vacuumErr := weatherObsRepo.RemoveWeatherObservationsOlderThan(context.Background(), 48*time.Hour); vacuumErr != nil {
		log.Printf("execution: weather obs vacuum: %v", vacuumErr)
	}

	// Bound the hot tables and reclaim what that frees. Runs after collection, on a
	// background context rather than ctx, so a tick cut short by SIGTERM still gets its
	// maintenance — every step is idempotent and picks up where it left off next tick.
	// Non-fatal for the same reason the weather vacuum is: falling behind on housekeeping
	// must never take collection down with it.
	if report, maintErr := maintenanceAgent.Run(context.Background()); maintErr != nil {
		log.Printf("execution: maintenance: %v", maintErr)
	} else if report.Total() > 0 || report.Vacuumed {
		log.Printf("execution: maintenance archived %d row(s), vacuumed=%t", report.Total(), report.Vacuumed)
	}

	if ctx.Err() != nil {
		log.Printf("execution: stopped by signal: %s", ctx.Err())
	}

	log.Println("execution: done")

	// A non-zero exit is reserved for an agent that did no work at all. Exiting on
	// runErr as a whole would mean exiting most ticks: RateAgent reports an error when
	// any single source of thirty fails to fetch, and on scraped third-party pages that
	// is routine rather than news. A signal that fires every run is not one (#74).
	//
	// This binary holds no Telegram client, by the same reasoning recorded in
	// cmd/notifier: the component that just failed to collect is the wrong one to
	// announce it. The exit code is what the cron wrapper can see.
	if errors.Is(runErr, internal.ErrAgentAborted) {
		os.Exit(1)
	}
}

func init() {
	// Register flags here so the test binary can see them, but do NOT call flag.Parse()
	// in init() — it would consume go test's own flags before the testing package
	// registers them ("flag provided but not defined"). main() calls flag.Parse() once;
	// tests never invoke main().
	flagLogsDir = flag.String("logs-dir", LogsDir, "path to logs directory")
	flagVerbosity = flag.String("verbosity", "warning", "minimum stdout log level (debug, info, warning, error, severe, critical)")
}

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
	// BEACON_CHROMIUM_PATH. When empty, chromedp searches PATH (chromium, chromium-browser,
	// google-chrome, chrome).
	ChromiumPath = os.Getenv(internal.EnvChromiumPath)
	// ProxyURL is the outbound proxy resolved from BEACON_PROXY_URL. It says only that
	// a proxy exists; nothing is routed through it until a source sets
	// options.use_proxy, so setting it alone leaves all 56 sources direct — the
	// measured default from issue #16.
	//
	// Assigned in main after the logger exists, not here: proxyutil.ResolveURL logs the
	// resolved value and log.Fatalf's on a malformed one, and a package initialiser runs
	// before the file logger is wired, where both would go to a stderr the cron wrappers
	// discard.
	ProxyURL string
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

// flagLogsDir and flagVerbosity hold the raw flag values populated by flag.Parse in
// main. They are package-level so initFlags can apply them after parsing.
var (
	flagLogsDir   *string
	flagVerbosity *string
)

// runner is the minimal interface the collector needs from each agent.
// One Run call per binary invocation; the loop in main wraps each call in a
// panic-recover shim.
type runner interface {
	Run(context.Context) error
}

// initFlags applies the parsed flag values to the exported globals. Called once from
// main() after flag.Parse().
func initFlags() {
	if dir := *flagLogsDir; dir != "" {
		LogsDir = dir
	}

	if v := *flagVerbosity; v != "" {
		LogVerbosity = internal.ParseLogLevel(v)
	}
}

func buildRunners(
	source *repository.RateSourceRepository,
	history *repository.ExecutionHistoryRepository,
	value *repository.RateValueRepository,
	weatherCity *repository.WeatherUserCityRepository,
	weatherObs *repository.WeatherObservationRepository,
	logger io.Writer,
) ([]runner, error) {
	// Passing the proxy URL does not route anything through it: the extractor builds a
	// proxied client alongside the direct one, and a source reaches it only by setting
	// options.use_proxy. Default stays direct. See the collection-egress note in CLAUDE.md.
	collectionRateAgent, err := collection.NewRateAgent(
		ProxyURL,
		ChromiumPath,
		source,
		history,
		value,
		logger,
		internal.UserAgent,
	)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	weatherAgent, err := wireWeather(weatherCity, weatherObs, logger)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	return []runner{collectionRateAgent, weatherAgent}, nil
}

// wireWeather constructs the Open-Meteo weather collection agent. Open-Meteo is
// hardcoded and always on — there is no per-provider config table and no "inactive"
// state, so this always returns a non-nil runner. An agent construction failure is fatal
// and returned as an error.
//
// Direct, like the rate sources, which also makes this consistent with cmd/web: the
// health inspector there has always probed Open-Meteo directly, and the two paths
// disagreeing was a documented wart.
func wireWeather(
	weatherCity *repository.WeatherUserCityRepository,
	weatherObs *repository.WeatherObservationRepository,
	logger io.Writer,
) (runner, error) {
	openMeteoProvider, err := weatherinfra.NewOpenMeteo("", logger)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("weather: open-meteo provider: %w", err), loginjector.NewTraceError())
	}

	weatherAgent, err := collection.NewWeatherAgent(
		openMeteoProvider, weatherCity, weatherObs,
		collection.DefaultWeatherThrottleInterval,
		logger,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("weather: agent: %w", err), loginjector.NewTraceError())
	}
	return weatherAgent, nil
}
