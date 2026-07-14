// Command collector polls all active rate sources on a configurable schedule,
// extracts exchange-rate values, and persists them to the SQLite database.
//
// It reads BEACON_SQLITEDB_DSN from the environment. Outbound HTTP/HTTPS traffic from
// plain and chromedp sources routes through BEACON_PROXY_URL (format: http://<host>:<port>,
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
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/application/collection"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	weatherinfra "github.com/seilbekskindirov/beacon/internal/infrastructure/weather"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/seilbekskindirov/beacon/internal/tools/proxyutil"
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
	// BEACON_CHROMIUM_PATH. When empty, chromedp searches PATH (chromium, chromium-browser,
	// google-chrome, chrome).
	ChromiumPath = os.Getenv(envChromiumPath)
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

const (
	envProxyURL = "BEACON_PROXY_URL"
	// envChromiumPath is an optional absolute path to the Chromium/Chrome binary;
	// when unset, chromedp searches PATH for chromedp-kind sources.
	envChromiumPath = "BEACON_CHROMIUM_PATH"
	envDsnSqliteDB  = "BEACON_SQLITEDB_DSN"
)

func main() {
	flag.Parse()
	initFlags()

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
	weatherCityRepo, err := repository.NewWeatherUserCityRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	weatherObsRepo, err := repository.NewWeatherObservationRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	weatherSourceRepo, err := repository.NewWeatherSourceRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	weatherGismeteoCityRepo, err := repository.NewWeatherGismeteoCityRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	log.Println("repositories: initiated")

	runners, err := buildRunners(
		sourceRepo, historyRepo, rateValueRepo,
		weatherCityRepo, weatherObsRepo,
		weatherSourceRepo, weatherGismeteoCityRepo,
		proxyURL, l.WriterAs(internal.LogLevelWarning),
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

	// Vacuum stale weather observations to prevent unbounded table growth.
	// Each collector tick stores new hourly_json rows; without pruning, the table
	// accumulates indefinitely. Non-fatal: a vacuum failure does not abort the run.
	if vacuumErr := weatherObsRepo.RemoveWeatherObservationsOlderThan(context.Background(), 48*time.Hour); vacuumErr != nil {
		log.Printf("execution: weather obs vacuum: %v", vacuumErr)
	}

	if ctx.Err() != nil {
		log.Printf("execution: stopped by signal: %s", ctx.Err())
	}

	log.Println("execution: done")
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

// runner is the minimal interface the collector needs from each agent.
// One Run call per binary invocation; the loop in main wraps each call in a
// panic-recover shim.
type runner interface {
	Run(context.Context) error
}

// weatherSourceLoader is the narrow weather_sources surface wireWeather needs. The
// concrete *repository.WeatherSourceRepository satisfies it structurally; the interface
// lets wireWeather be unit-tested with a fake, no live DB required.
type weatherSourceLoader interface {
	ObtainAllWeatherSources(ctx context.Context) ([]domain.WeatherSource, error)
}

// gismeteoCoverageLoader is the narrow weather_gismeteo_cities surface gismeteoOption
// needs. Satisfied structurally by *repository.WeatherGismeteoCityRepository.
type gismeteoCoverageLoader interface {
	ObtainGismeteoCoverage(ctx context.Context) (map[string]domain.WeatherGismeteoCity, error)
}

func buildRunners(
	source *repository.RateSourceRepository,
	history *repository.ExecutionHistoryRepository,
	value *repository.RateValueRepository,
	weatherCity *repository.WeatherUserCityRepository,
	weatherObs *repository.WeatherObservationRepository,
	weatherSource *repository.WeatherSourceRepository,
	gismeteoCoverage *repository.WeatherGismeteoCityRepository,
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

	runners := []runner{collectionRateAgent}

	weatherAgent, err := wireWeather(weatherCity, weatherObs, weatherSource, gismeteoCoverage, proxyURL, logger)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	// weatherAgent is nil when open-meteo is toggled inactive (no weather collection).
	if weatherAgent != nil {
		runners = append(runners, weatherAgent)
	}

	return runners, nil
}

// wireWeather assembles the weather collection agent from the data-driven config in
// weather_sources / weather_gismeteo_cities. Config is loaded once here at startup (the
// collector is one-shot per cron tick, so "startup" and "per invocation" coincide).
//
// Degradation rules: a config-read failure or an absent row defaults a provider to
// active, so a half-migrated or hand-edited DB never goes dark silently. Open-Meteo is
// the primary provider; when its row is explicitly inactive there is no weather
// collection at all and this returns a nil runner. A provider construction failure
// (e.g. an invalid proxy URL) is fatal and returned as an error.
func wireWeather(
	weatherCity *repository.WeatherUserCityRepository,
	weatherObs *repository.WeatherObservationRepository,
	weatherSource weatherSourceLoader,
	gismeteoCoverage gismeteoCoverageLoader,
	proxyURL string,
	logger io.Writer,
) (runner, error) {
	ctx := context.Background()

	byProvider := make(map[string]domain.WeatherSource)
	if sources, err := weatherSource.ObtainAllWeatherSources(ctx); err != nil {
		fmt.Fprintf(logger, "weather: source config load failed, assuming all providers active: %v\n", err)
	} else {
		for _, s := range sources {
			byProvider[s.Provider] = s
		}
	}

	// A missing open-meteo row defaults to active; only an explicit active=0 disables
	// weather collection entirely (open-meteo is the source of truth, gismeteo piggybacks).
	if row, ok := byProvider[domain.ProviderOpenMeteo]; ok && !row.Active {
		fmt.Fprintf(logger, "weather: open-meteo is inactive — weather collection disabled\n")
		return nil, nil
	}

	openMeteoProvider, err := weatherinfra.NewOpenMeteo(proxyURL)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("weather: open-meteo provider: %w", err), internal.NewTraceError())
	}

	var opts []collection.WeatherAgentOption
	gismeteoOpt, err := gismeteoOption(ctx, byProvider, gismeteoCoverage, proxyURL, logger)
	if err != nil {
		return nil, err
	}
	if gismeteoOpt != nil {
		opts = append(opts, gismeteoOpt)
	}

	weatherAgent, err := collection.NewWeatherAgent(
		openMeteoProvider, weatherCity, weatherObs,
		collection.DefaultWeatherThrottleInterval,
		logger,
		opts...,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("weather: agent: %w", err), internal.NewTraceError())
	}
	return weatherAgent, nil
}

// gismeteoOption builds the optional gismeteo WeatherAgentOption from config. It returns
// a nil option — gismeteo comparison phase skipped — when the provider is inactive or its
// coverage map is empty (both valid "disabled" states, never fatal). A provider
// construction failure is returned as an error (fatal to wiring); a malformed
// throttle_interval is non-fatal and falls back to DefaultGismeteoThrottleInterval.
func gismeteoOption(
	ctx context.Context,
	byProvider map[string]domain.WeatherSource,
	gismeteoCoverage gismeteoCoverageLoader,
	proxyURL string,
	logger io.Writer,
) (collection.WeatherAgentOption, error) {
	row, hasRow := byProvider[domain.ProviderGismeteo]
	if hasRow && !row.Active {
		fmt.Fprintf(logger, "weather: gismeteo is inactive — comparison phase disabled\n")
		return nil, nil
	}

	coverage, err := gismeteoCoverage.ObtainGismeteoCoverage(ctx)
	if err != nil {
		fmt.Fprintf(logger, "weather: gismeteo coverage load failed, skipping gismeteo: %v\n", err)
		return nil, nil
	}
	if len(coverage) == 0 {
		fmt.Fprintf(logger, "weather: gismeteo coverage is empty — comparison phase disabled\n")
		return nil, nil
	}

	throttle := collection.DefaultGismeteoThrottleInterval
	baseURL, userAgent := "", ""
	if hasRow {
		baseURL = row.BaseURL
		userAgent = row.Options.UserAgent
		if row.ThrottleInterval != "" {
			if parsed, perr := time.ParseDuration(row.ThrottleInterval); perr != nil {
				fmt.Fprintf(logger, "weather: gismeteo throttle_interval %q invalid, using default %s: %v\n",
					row.ThrottleInterval, collection.DefaultGismeteoThrottleInterval, perr)
			} else {
				throttle = parsed
			}
		}
	}

	provider, err := weatherinfra.NewGismeteo(proxyURL, coverage, baseURL, userAgent)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("weather: gismeteo provider: %w", err), internal.NewTraceError())
	}
	return collection.WithGismeteo(provider, throttle), nil
}
