// Command notifier delivers pending notification events to Telegram users.
// It runs on a schedule, fetching unprocessed events from SQLite via SQLITEDB_DSN
// and dispatching them through the bot configured by TELEGRAMBOT_DSN.
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
	"github.com/seilbekskindirov/monitor/internal/application/notification"
	"github.com/seilbekskindirov/monitor/internal/infrastructure/sqlitedb"
	integration "github.com/seilbekskindirov/monitor/internal/infrastructure/telegrambot"
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
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

const (
	envDsnTelegramBOT = "TELEGRAMBOT_DSN"
	envDsnSqliteDB    = "SQLITEDB_DSN"
)

func main() {
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)

	l, err := internal.NewLogger(LogsDir, "notifier", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %s", err.Error())
	}
	log.Println("logger: initiated")

	dsnSQLiteDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnSqliteDB, err.Error())
	}
	dsnTelegramBOT, err := dsninjector.Unmarshal(envDsnTelegramBOT)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnTelegramBOT, err.Error())
	}
	log.Println("settings: initiated")

	db, err := sqlitedb.NewSQLiteClient(dsnSQLiteDB, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: sqlite connection is failed, %s", err.Error())
	}
	defer func(c io.Closer) {
		if e := c.Close(); e != nil {
			log.Printf("close sqlite client: %v", e)
		}
	}(db)
	if err = sqlitedb.RequireMigratedSchema(context.Background(), db); err != nil {
		log.Fatalf("schema check: %s", err.Error())
	}
	tbot, err := integration.NewTBotClient(dsnTelegramBOT, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: telegram bot connection is failed, %s", err.Error())
	}
	log.Println("dependencies: initiated")

	rRateSource, err := repository.NewRateSourceRepository(db)
	if err != nil {
		log.Fatalf("repositories: rate source build is failed, %s", err.Error())
	}
	rRateValue, err := repository.NewRateValueRepository(db)
	if err != nil {
		log.Fatalf("repositories: rate value build is failed, %s", err.Error())
	}
	rRateUserSubscription, err := repository.NewRateUserSubscriptionRepository(db)
	if err != nil {
		log.Fatalf("repositories: subscription build is failed, %s", err.Error())
	}
	rRateUserEvent, err := repository.NewRateUserEventRepository(db)
	if err != nil {
		log.Fatalf("repositories: notification pool build is failed, %s", err.Error())
	}
	log.Println("repositories: initiated")

	ctx := context.Background()

	checkAgent, err := notification.NewRateCheckAgent(
		rRateSource,
		rRateValue,
		rRateUserSubscription,
		rRateUserEvent,
		l.WriterAs(internal.LogLevelWarning),
	)
	if err != nil {
		log.Fatalf("runners: check agent build is failed: %s", err)
	}

	dispatchAgent, err := notification.NewRateDispatchAgent(tbot, rRateUserEvent)
	if err != nil {
		log.Fatalf("runners: dispatch agent build is failed: %s", err)
	}

	if err = dispatchAgent.Vacuum(ctx); err != nil {
		log.Fatalf("runners: vacuum failed: %s", err)
	}
	log.Println("runners: initiated")

	var errs []error
	if err = checkAgent.Run(ctx); err != nil {
		errs = append(errs, err)
	}
	if err = dispatchAgent.Run(ctx); err != nil {
		errs = append(errs, err)
	}
	if err = errors.Join(errs...); err != nil {
		log.Fatalf("execution: %s", err)
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
