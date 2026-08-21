// Command notifier delivers pending notification events to Telegram users.
// It runs on a schedule, fetching unprocessed events from SQLite via BEACON_SQLITEDB_DSN
// and dispatching them through the bot configured by BEACON_TELEGRAMBOT_DSN.
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
	_ "time/tzdata" // embedded IANA tzdata so time.LoadLocation works without system tzdata

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/application/notification"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	integration "github.com/seilbekskindirov/beacon/internal/infrastructure/telegrambot"
	"github.com/seilbekskindirov/beacon/internal/repository"
	_ "modernc.org/sqlite"
)

func main() {
	flag.Parse()
	initFlags()

	l, err := internal.NewLogger(LogsDir, "notifier", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	// First line of every run in the log file: which build wrote everything below.
	// Logged after the logger exists — before it, this went to a stderr the cron
	// wrappers discard, so no line in the file could be attributed to a release.
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)
	log.Println("logger: initiated")

	// Notifier only calls Telegram, and Telegram traffic bypasses any proxy via the
	// hardcoded transport in NewTBotClient, so BEACON_PROXY_URL is intentionally not parsed.

	dsnTelegramBOT, err := dsninjector.Unmarshal(internal.EnvTelegramBotDSN)
	if err != nil {
		log.Fatalf("settings: %s: unparseable value (contents not logged)", internal.EnvTelegramBotDSN)
	}
	dsnDB, err := dsninjector.Unmarshal(internal.EnvSQLiteDSN)
	if err != nil {
		log.Fatalf("settings: %s: unparseable value (contents not logged)", internal.EnvSQLiteDSN)
	}
	log.Println("settings: initiated")

	db, err := sqlitedb.NewSQLiteClient(dsnDB)
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
	tbot, err := integration.NewTBotClient(dsnTelegramBOT)
	if err != nil {
		//nolint:gocritic // a fatal here is a refusal to start: the deferred close is
		// skipped, and the OS reclaims the descriptor a moment later. Threading an
		// error out of every startup step would add plumbing for a path that ends
		// the process anyway.
		log.Fatalf("dependencies: telegram bot connection is failed, %s", err.Error())
	}
	log.Println("dependencies: initiated")

	sourceRepo, err := repository.NewRateSourceRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	rateValueRepo, err := repository.NewRateValueRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	subscriptionRepo, err := repository.NewRateUserSubscriptionRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	eventRepo, err := repository.NewRateUserEventRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	profileRepo, err := repository.NewRateUserProfileRepository(db)
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
	weatherForecastRepo, err := repository.NewWeatherForecastDayRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	historyRepo, err := repository.NewExecutionHistoryRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	sourceHealthRepo, err := repository.NewRateSourceHealthRepository(db)
	if err != nil {
		log.Fatalf("repositories: %s", err.Error())
	}
	log.Println("repositories: initiated")

	// SIGTERM/SIGINT cancel ctx mid-run so an in-flight tick aborts the next
	// dispatch instead of the OS killing the process between transactions.
	// The migrator uses the same pattern.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	checkAgent, err := notification.NewRateCheckAgent(
		sourceRepo,
		rateValueRepo,
		subscriptionRepo,
		eventRepo,
		profileRepo,
		l.WriterAs(internal.LogLevelWarning),
	)
	if err != nil {
		log.Fatalf("runners: check agent build is failed: %s", err)
	}

	weatherCheckAgent, err := notification.NewWeatherCheckAgent(
		weatherCityRepo,
		weatherObsRepo,
		weatherForecastRepo,
		eventRepo,
		l.WriterAs(internal.LogLevelWarning),
	)
	if err != nil {
		log.Fatalf("runners: weather check agent build is failed: %s", err)
	}

	// Collection health is reported from here rather than from the collector: the
	// collector holds no Telegram client, and the component that just failed to collect
	// is the wrong one to be responsible for saying so.
	sourceHealthAgent, err := notification.NewSourceHealthAgent(
		sourceRepo,
		historyRepo,
		sourceHealthRepo,
		eventRepo,
		tbot.AdminChatID(),
		notification.DefaultSourceStaleFactor,
		notification.DefaultSourceStaleFloor,
		l.WriterAs(internal.LogLevelWarning),
	)
	if err != nil {
		log.Fatalf("runners: source health agent build is failed: %s", err)
	}

	dispatchAgent, err := notification.NewRateDispatchAgent(tbot, eventRepo)
	if err != nil {
		log.Fatalf("runners: dispatch agent build is failed: %s", err)
	}

	// Vacuum is housekeeping — never block execution on its failure. Use a
	// background context so a mid-run SIGTERM doesn't surface as a Vacuum
	// cancellation and a false-positive crash exit.
	if err = dispatchAgent.Vacuum(context.Background()); err != nil {
		log.Printf("runners: vacuum failed (non-fatal): %s", err)
	}
	log.Println("runners: initiated")

	var errs []error
	// Skip context.Canceled so a clean shutdown reason isn't logged twice (joined
	// errors line plus the "stopped by signal" line below).
	if err = checkAgent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		errs = append(errs, err)
	}
	if err = weatherCheckAgent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		errs = append(errs, err)
	}
	// Before the dispatch, so a transition detected on this tick is delivered on this
	// tick rather than waiting for the next one.
	if report, healthErr := sourceHealthAgent.Run(ctx); healthErr != nil && !errors.Is(healthErr, context.Canceled) {
		errs = append(errs, healthErr)
	} else if len(report.Alerted)+len(report.Recovered) > 0 {
		log.Printf("execution: source health: %d down, %d recovered", len(report.Alerted), len(report.Recovered))
	}
	if err = dispatchAgent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		errs = append(errs, err)
	}
	runErr := errors.Join(errs...)
	if runErr != nil {
		log.Printf("execution: completed with errors: %s", runErr)
	}

	// An agent that did no work at all is the one failure nobody would otherwise hear
	// about. The log file is the only destination for everything above, cron discards
	// stderr, and /health/check cannot see it — its inspectors probe connectivity, not
	// whether a sweep ran. Worst case is SourceHealthAgent itself: it is the watchdog
	// that reports a source gone silent, so when it aborts, the outage it exists to
	// announce becomes invisible along with it (#74).
	//
	// Deliberately not sent for runErr as a whole. RateAgent reports an error when any
	// single source of thirty fails to fetch, which on scraped third-party pages happens
	// most ticks; a message that arrives every tick is one nobody reads by the end of the
	// week, and SourceHealthAgent already exists because only sustained silence is news.
	//
	// Failing to deliver this is logged and nothing more: there is no third channel to
	// escalate to, and taking the run down over it would help no one.
	if errors.Is(runErr, internal.ErrAgentAborted) {
		text := fmt.Sprintf("Beacon notifier: an agent aborted before doing any work.\n\n%s", runErr)
		if sendErr := tbot.SendPlainTextMessage(context.Background(), integration.TelegramChatID(tbot.AdminChatID()), text); sendErr != nil {
			log.Printf("execution: could not report the aborted agent to the admin chat: %s", sendErr)
		}
	}
	if ctx.Err() != nil {
		log.Printf("execution: stopped by signal: %s", ctx.Err())
	}

	log.Println("execution: done")
}

// test binary sees the flags too. The rule this package does follow is the one
// that matters: nothing here logs or exits, because a line written before the
// logger exists goes to a stderr the cron wrappers discard.
//
//nolint:gochecknoinits // flag registration has to happen before main runs so the
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
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

// flagLogsDir and flagVerbosity hold the raw flag values populated by flag.Parse in
// main. They are package-level so initFlags can apply them after parsing.
var (
	flagLogsDir   *string
	flagVerbosity *string
)

// initFlags applies the parsed flag values. Called from main after flag.Parse.
func initFlags() {
	if dir := *flagLogsDir; dir != "" {
		LogsDir = dir
	}

	if v := *flagVerbosity; v != "" {
		LogVerbosity = internal.ParseLogLevel(v)
	}
}
