// Command web serves the HTTP API and the embedded Mini App static files.
// It reads SQLITEDB_DSN, TELEGRAMBOT_DSN, AI_PRIMARY_DSN, and AI_FALLBACK_DSN
// from the environment, starts the Telegram bot update loop, and listens on
// the port configured by --port (default 8080).
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/internal/application/service"
	"github.com/seilbekskindirov/monitor/internal/application/sourceaudit"
	"github.com/seilbekskindirov/monitor/internal/gateway"
	v1handlers "github.com/seilbekskindirov/monitor/internal/gateway/httpV1/handlers"
	"github.com/seilbekskindirov/monitor/internal/infrastructure/artificialintelligence"
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
	// HttpPort is the TCP port the HTTP server listens on.
	HttpPort = 8080
	// HttpTimeOut is the read/idle timeout for the HTTP server.
	HttpTimeOut = 30 * time.Second
	// HttpWriteTimeout is the write timeout for the HTTP server; set to 130s to give
	// 10 s headroom over the rulegen 120 s per-request ceiling.
	HttpWriteTimeout = 130 * time.Second
	// StaticDir overrides the embedded static file system when non-empty.
	StaticDir = ""
	// APIDsn is the public HTTPS origin passed via --api-dsn; used by the WASM client.
	APIDsn = ""
)

const (
	envDsnSqliteDB    = "SQLITEDB_DSN"
	envDsnTelegramBOT = "TELEGRAMBOT_DSN"
	envDsnAIPrimary   = "AI_PRIMARY_DSN"
	envDsnAIFallback  = "AI_FALLBACK_DSN"
)

func main() {
	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)
	if StaticDir != "" {
		log.Printf("static directory (override): %s\n", StaticDir)
	} else {
		log.Println("static directory: embedded FS")
	}

	l, err := internal.NewLogger(LogsDir, "web", LogVerbosity)
	if err != nil {
		log.Fatalf("logger init: %v", err)
	}

	// init settings
	dsnSQLiteDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnSqliteDB, err.Error())
		return
	}
	dsnTelegramBOT, err := dsninjector.Unmarshal(envDsnTelegramBOT)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnTelegramBOT, err.Error())
		return
	}
	if APIDsn == "" {
		log.Fatalf("settings: --api-dsn is required (format: https://<host>/)")
	}
	dsnAPI, err := dsninjector.Parse(APIDsn)
	if err != nil {
		log.Fatalf("settings: --api-dsn, %s", err.Error())
		return
	}
	// Telegram WebApp buttons reject non-HTTPS, IP literals, and localhost,
	// so the DSN's host must resolve to a publicly reachable HTTPS host.
	webAppURL := "https://" + strings.TrimPrefix(strings.TrimPrefix(dsnAPI.Addr(), "https://"), "http://") + "/app/subscriptions.html"
	dsnAIPrimary, err := dsninjector.Unmarshal(envDsnAIPrimary)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnAIPrimary, err.Error())
	}
	log.Println("settings: initiated")

	// init dependencies
	db, err := sqlitedb.NewSQLiteClient(dsnSQLiteDB, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: sqlite %s connection is failed, %s", dsnSQLiteDB.Database(), err.Error())
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
	tbot, err := integration.NewTBotClient(dsnTelegramBOT, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: telegram bot connection is failed, %s", err.Error())
		return
	}
	if id, username, err := tbot.Me(context.Background()); err != nil {
		log.Printf("telegram: identity probe failed: %v", err)
	} else {
		log.Printf("telegram: authenticated as @%s (id=%d)", username, id)
	}
	aiPrimary, err := artificialintelligence.NewClient(dsnAIPrimary, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: ai primary client is failed, %s", err.Error())
	}
	var aiFallback artificialintelligence.AIClient
	if _, ok := os.LookupEnv(envDsnAIFallback); ok {
		dsnAIFallback, dsnErr := dsninjector.Unmarshal(envDsnAIFallback)
		if dsnErr != nil {
			log.Fatalf("settings: %s, %s", envDsnAIFallback, dsnErr.Error())
		}
		aiFallback, err = artificialintelligence.NewClient(dsnAIFallback, l.WriterAs(internal.LogLevelInfo))
		if err != nil {
			log.Fatalf("dependencies: ai fallback client is failed, %s", err.Error())
		}
	} else {
		aiFallback, err = artificialintelligence.NewStubClient()
		if err != nil {
			log.Fatalf("dependencies: ai fallback stub is failed, %s", err.Error())
		}
	}
	log.Printf("ai: primary=%s fallback=%s", aiPrimary.Name(), aiFallback.Name())
	log.Println("dependencies: initiated")

	// init repositories
	rRateSource, err := repository.NewRateSourceRepository(db)
	if err != nil {
		log.Fatalf("rate source repo: %s", err)
	}
	rExecutionHistory, err := repository.NewExecutionHistoryRepository(db)
	if err != nil {
		log.Fatalf("execution history repo: %s", err)
	}
	rRateValue, err := repository.NewRateValueRepository(db)
	if err != nil {
		log.Fatalf("rate value repo: %s", err)
	}
	rRateUserSubscription, err := repository.NewRateUserSubscriptionRepository(db)
	if err != nil {
		log.Fatalf("repositories: user subscription build is failed, %s", err.Error())
		return
	}
	rRateUserEvent, err := repository.NewRateUserEventRepository(db)
	if err != nil {
		log.Fatalf("repositories: notification pool build is failed, %s", err.Error())
		return
	}
	log.Println("repositories: initiated")

	// Build rulegen generator. plainFetcher wraps sourceaudit.HTTPFetcher to satisfy
	// the rulegen.Fetcher interface. The adapter type is declared at the bottom of this
	// file — see sourceAuditFetcherAdapter. Deliberate re-declaration (see also
	// cmd/rulegen/main.go) to avoid inflating the rulegen package's public surface.
	plainFetcher := &sourceAuditFetcherAdapter{inner: sourceaudit.NewHTTPFetcher(time.Minute)}
	chromedpFor := func(waitSelector string) rulegen.Fetcher {
		return rulegen.NewChromedpFetcher(rulegen.ChromedpFetcherOptions{
			ChromiumPath: os.Getenv("CHROMIUM_PATH"),
			Logger:       l.WriterAs(internal.LogLevelInfo),
			WaitSelector: waitSelector,
		})
	}
	ruleExecutor := rulegen.NewRuleExecutor()

	buildGenerator := func(maxPrimary, maxFallback int) (v1handlers.RulegenGenerator, error) {
		return rulegen.NewGenerator(
			aiPrimary, aiFallback,
			plainFetcher, chromedpFor,
			ruleExecutor, rRateSource,
			maxPrimary, maxFallback,
			l.WriterAs(internal.LogLevelInfo),
		)
	}
	defaultGen, genErr := buildGenerator(3, 2)
	if genErr != nil {
		log.Fatalf("dependencies: rulegen default generator: %s", genErr.Error())
	}
	log.Printf("rulegen: default generator ready (primary=%s fallback=%s)", aiPrimary.Name(), aiFallback.Name())
	rulegenLocks := rulegen.NewLockManager()

	restAPI, err := service.NewRateRestAPI(
		rExecutionHistory,
		rRateSource,
		rRateValue,
		rRateUserSubscription,
		rRateUserEvent,
	)
	if err != nil {
		log.Fatalf("services: rest api is failed, %s", err.Error())
		return
	}
	botToken := tbot.BotToken()
	mux, err := gateway.NewGateway(restAPI, botToken, rRateUserSubscription, rRateSource, rRateValue,
		defaultGen, buildGenerator, tbot.AdminChatID(), rulegenLocks)
	if err != nil {
		log.Fatalf("services: mux api is failed, %s", err.Error())
		return
	}
	var fsys http.FileSystem
	if StaticDir != "" {
		fsys = http.Dir(StaticDir)
	} else {
		sub, err := fs.Sub(staticFS, "static")
		if err != nil {
			log.Fatalf("embed sub: %v", err)
		}
		fsys = http.FS(sub)
	}
	mux.Handle("/", http.FileServer(fsys))
	botGenFactory := func(maxPrimary, maxFallback int) (service.RulegenGenerator, error) {
		return buildGenerator(maxPrimary, maxFallback)
	}
	tbotAPI, err := service.NewTelegramApi(tbot, rRateUserSubscription, rRateSource, webAppURL,
		defaultGen, rulegenLocks, tbot.AdminChatID(), botGenFactory)
	if err != nil {
		log.Fatalf("services: telegram api is failed, %s", err.Error())
		return
	}
	log.Println("services: initiated")

	// run telegram server
	tbotAPI.Run(context.Background())

	// run http server
	// WriteTimeout is set to HttpWriteTimeout (130s) — larger than HttpTimeOut (30s read) —
	// to give the rulegen endpoint (120s handler ceiling) headroom to deliver its response
	// before the server cuts the connection.
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", HttpPort),
		Handler:      mux,
		ReadTimeout:  HttpTimeOut,
		WriteTimeout: HttpWriteTimeout,
		IdleTimeout:  HttpTimeOut >> 1,
	}
	go func() {
		log.Printf("http server: listening on %d port", HttpPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %s", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	time.Sleep(10 * time.Millisecond)

	log.Println("initialization completed")

	<-quit
	log.Println("http server: shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http server: forced shutdown failed, %s", err)
	}
}

//go:embed static
var staticFS embed.FS

func init() {
	port := flag.Int("port", HttpPort, "http server port")
	timeout := flag.String("timeout", HttpTimeOut.String(), "HTTP read/idle timeout duration (controls ReadTimeout; WriteTimeout is fixed at 130s to give the rulegen endpoint headroom)")
	logsDir := flag.String("logs-dir", LogsDir, "path to logs directory")
	verbosity := flag.String("verbosity", "warning", "minimum stdout log level (debug, info, warning, error, severe, critical)")
	staticDir := flag.String("static-dir", StaticDir, "path to static files directory")
	apiDsn := flag.String("api-dsn", APIDsn, "public HTTPS origin DSN, format: https://<host>/")
	flag.Parse()

	if *port <= 1000 || *port >= 32000 {
		log.Printf("invalid port value: %d, using default %d", *port, HttpPort)
	} else {
		HttpPort = *port
	}

	if value, err := time.ParseDuration(*timeout); err != nil {
		log.Printf("invalid timeout value: %s, using default %s", *timeout, HttpTimeOut.String())
	} else if value > 10*time.Second {
		HttpTimeOut = value
	}

	if dir := *staticDir; dir != "" {
		StaticDir = dir
	}

	if dir := *logsDir; dir != "" {
		LogsDir = dir
	}

	if v := *verbosity; v != "" {
		LogVerbosity = internal.ParseLogLevel(*verbosity)
	}

	if v := *apiDsn; v != "" {
		APIDsn = v
	}
}

// sourceAuditFetcherAdapter wraps sourceaudit.Fetcher to satisfy the
// rulegen.Fetcher interface, which returns only the body bytes.
// Deliberately re-declared here (see also cmd/rulegen/main.go) to avoid
// inflating the rulegen package's public surface.
type sourceAuditFetcherAdapter struct {
	inner sourceaudit.Fetcher
}

func (a *sourceAuditFetcherAdapter) Fetch(ctx context.Context, url string) ([]byte, error) {
	result, err := a.inner.Fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}
