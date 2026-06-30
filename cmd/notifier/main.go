// Command notifier delivers pending notification events to Telegram users.
// It runs on a schedule, fetching unprocessed events from SQLite via BEACON_SQLITEDB_DSN
// and dispatching them through the bot configured by BEACON_TELEGRAMBOT_DSN.
package main

import (
	"context"
	"errors"
	"flag"
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
	envDsnTelegramBOT = "BEACON_TELEGRAMBOT_DSN"
	envDsnSqliteDB    = "BEACON_SQLITEDB_DSN"
)

func main() {
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)

	l, err := internal.NewLogger(LogsDir, "notifier", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	log.Println("logger: initiated")

	// Notifier only calls Telegram, and Telegram traffic bypasses any proxy via the
	// hardcoded transport in NewTBotClient, so BEACON_PROXY_URL is intentionally not parsed.

	dsnTelegramBOT, err := dsninjector.Unmarshal(envDsnTelegramBOT)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnTelegramBOT, err.Error())
	}
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
	tbot, err := integration.NewTBotClient(dsnTelegramBOT, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
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
		eventRepo,
		l.WriterAs(internal.LogLevelWarning),
	)
	if err != nil {
		log.Fatalf("runners: weather check agent build is failed: %s", err)
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
	if err = dispatchAgent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		errs = append(errs, err)
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
