// Command web serves the HTTP API and the embedded Mini App static files.
// It reads BEACON_SQLITEDB_DSN and BEACON_TELEGRAMBOT_DSN from the environment, starts the
// Telegram bot update loop, and listens on the port configured by --port (default 8080).
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embedded IANA tzdata for time.LoadLocation in profile-upsert

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/beacon/internal"
	appchart "github.com/seilbekskindirov/beacon/internal/application/chart"
	"github.com/seilbekskindirov/beacon/internal/application/inspector"
	"github.com/seilbekskindirov/beacon/internal/application/service"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/seilbekskindirov/beacon/internal/gateway"
	"github.com/seilbekskindirov/beacon/internal/gateway/middleware"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	integration "github.com/seilbekskindirov/beacon/internal/infrastructure/telegrambot"
	weatherinfra "github.com/seilbekskindirov/beacon/internal/infrastructure/weather"
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
	// HttpPort is the TCP port the HTTP server listens on.
	HttpPort = 8080
	// HttpTimeOut is the read/write/idle timeout for the HTTP server.
	HttpTimeOut = 30 * time.Second
	// StaticDir overrides the embedded static file system when non-empty.
	StaticDir = ""
	// APIDsn is the public HTTPS origin passed via --api-dsn; used by the WASM client.
	APIDsn = ""
)

const (
	envDsnTelegramBOT = "BEACON_TELEGRAMBOT_DSN"
	envDsnSqliteDB    = "BEACON_SQLITEDB_DSN"
)

func main() {
	serviceStart := time.Now()
	flag.Parse()
	initFlags()

	log.Printf("build: %s (%s) at %s\n", BuildVersion, BuildHash, BuildTime)
	if StaticDir != "" {
		log.Printf("static directory (override): %s\n", StaticDir)
	} else {
		log.Println("static directory: embedded FS")
	}

	l, err := internal.NewLogger(LogsDir, "web", LogVerbosity)
	if err != nil {
		log.Fatalf("logger: %v", err)
	}
	log.Println("logger: initiated")

	// web only calls Telegram (bot polling/webhook), and Telegram traffic bypasses
	// any proxy via the hardcoded transport in NewTBotClient, so BEACON_PROXY_URL is not
	// parsed here.

	// init settings
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
	// Telegram WebApp buttons reject non-HTTPS, IP literals, and localhost, so the
	// host must be a publicly reachable HTTPS host. The trailing slash is required;
	// the site root serves the unified dispatcher page.
	webAppURL := "https://" + strings.TrimPrefix(strings.TrimPrefix(dsnAPI.Addr(), "https://"), "http://") + "/"
	// Echo the resolved URL so operators can confirm the BotFather Menu Button
	// still points at the same origin after each deploy. Not a secret.
	log.Printf("settings: webAppURL=%s (must match BotFather Menu Button URL)", webAppURL)
	dsnDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		log.Fatalf("settings: %s, %s", envDsnSqliteDB, err.Error())
		return
	}
	log.Println("settings: initiated")

	// init dependencies
	db, err := sqlitedb.NewSQLiteClient(dsnDB, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		log.Fatalf("dependencies: %s", err.Error())
		return
	}
	if err = sqlitedb.RequireMigratedSchema(context.Background(), db); err != nil {
		log.Fatalf("dependencies: schema check: %s", err.Error())
		return
	}
	defer func(c io.Closer) {
		if e := c.Close(); e != nil {
			log.Printf("close sqlite client: %v", e)
		}
	}(db)
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
	healthAgent := inspector.NewAgentWithAdvisory(0,
		[]inspector.Inspector{
			inspector.NewDBInspector(db),
			inspector.NewTelegramInspector(tbot),
		},
		[]inspector.Inspector{
			inspector.NewOpenMeteoInspector(),
			inspector.NewGismeteoInspector(),
		},
	)
	log.Println("dependencies: initiated")

	// init repositories
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

	restAPI, err := service.NewRateRestAPI(
		historyRepo,
		sourceRepo,
		rateValueRepo,
		subscriptionRepo,
		eventRepo,
	)
	if err != nil {
		log.Fatalf("services: rest api is failed, %s", err.Error())
		return
	}
	botToken := tbot.BotToken()
	if botToken == "" {
		// Without a bot token the Mini App initData HMAC can't be verified, so
		// every /api/me/* call returns 401. Fail at startup rather than silently
		// rejecting every authenticated request.
		log.Fatalf("services: bot token is empty — check BEACON_TELEGRAMBOT_DSN")
	}
	// rateValueRepo satisfies both ValuesLoader (ObtainMeChart) and
	// HistoryValuesLoader (ObtainMeHistory); sourceRepo also satisfies
	// PublicSourcesLoader (ObtainPublicChart).
	chartSvc := appchart.NewService(subscriptionRepo, sourceRepo, rateValueRepo, rateValueRepo, sourceRepo, time.Now)

	// Open-Meteo geocoder for city search. cmd/web always uses a direct connection
	// (no proxy) — Telegram traffic already bypasses the proxy, and geocoding calls
	// from the web handler are ephemeral lookups. The proxy URL is intentionally
	// empty here; collector and doctor pass BEACON_PROXY_URL instead.
	openMeteoClient, err := weatherinfra.NewOpenMeteo("")
	if err != nil {
		log.Fatalf("geocoder: open-meteo: %s", err.Error())
	}
	geoAdapter := &openMeteoGeoAdapter{client: openMeteoClient}

	mux, err := gateway.NewGateway(restAPI, botToken, subscriptionRepo, sourceRepo, rateValueRepo, profileRepo, chartSvc, healthAgent, BuildVersion, serviceStart, gateway.WeatherGatewayDeps{
		CityRepo: weatherCityRepo,
		Geocoder: geoAdapter,
		ObsRepo:  weatherObsRepo,
	})
	if err != nil {
		log.Fatalf("services: mux api is failed, %s", err.Error())
		return
	}
	var httpFsys http.FileSystem
	var fsSub fs.FS
	if StaticDir != "" {
		dirFS := os.DirFS(StaticDir)
		fsSub = dirFS
		httpFsys = http.FS(dirFS)
	} else {
		sub, err := fs.Sub(staticFS, "static")
		if err != nil {
			log.Fatalf("embed sub: %v", err)
		}
		fsSub = sub
		httpFsys = http.FS(sub)
	}

	// Build the hashed-asset registry from the active FS. It hashes raw bytes (not
	// .gz siblings) so a gzip-level change alone doesn't invalidate the cache-busting
	// URL. Missing assets are fatal here.
	hashSpecs := []assetSpec{
		{sourcePath: "app.wasm", contentType: "application/wasm", gzipPath: "app.wasm.gz"},
		{sourcePath: "wasm_exec.js", contentType: "text/javascript; charset=utf-8"},
	}
	registry, err := newHashedAssetRegistry(fsSub, hashSpecs)
	if err != nil {
		log.Fatalf("hashed assets: %v", err)
	}
	registry.logEntries()

	// Build the boot-time HTML caches: both entry points are rewritten once so the
	// served HTML references the registry's hashed asset URLs.
	bootTime := time.Now()
	indexCache, err := newHTMLCache(fsSub, "index.html", registry, bootTime)
	if err != nil {
		log.Fatalf("html cache: %v", err)
	}
	adminCache, err := newHTMLCache(fsSub, "admin/index.html", registry, bootTime)
	if err != nil {
		log.Fatalf("html cache: %v", err)
	}

	// staticHandler dispatches hashed-asset and HTML-cache paths first, then falls
	// through to the plain FileServer for unhashed paths (stale-HTML recovery) and
	// other static content. The mux's API routes shadow this catch-all.
	fileHandler := http.FileServer(httpFsys)
	mux.Handle("/", staticHandler(fileHandler, fsSub, indexCache, adminCache, registry))
	tbotAPI, err := service.NewTelegramApi(tbot, subscriptionRepo, rateValueRepo, sourceRepo, profileRepo, webAppURL)
	if err != nil {
		log.Fatalf("services: telegram api is failed, %s", err.Error())
		return
	}
	log.Println("services: initiated")

	// One signal context drives both the Telegram update loop and the HTTP
	// server's shutdown wait; sibling binaries (collector, notifier) use the
	// same pattern.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// run telegram server (bound to ctx so SIGTERM cancels the bot poll loop)
	tbotAPI.Run(ctx)

	// run http server. Bind the listener before logging "listening" so the
	// readiness marker fires only after the kernel has bound the port; otherwise
	// a probe grepping for the marker can race the goroutine and connect to a
	// not-yet-bound port.
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", HttpPort),
		Handler:      middleware.Logger(mux, l.WriterAs(internal.LogLevelInfo)),
		ReadTimeout:  HttpTimeOut,
		WriteTimeout: HttpTimeOut,
		IdleTimeout:  HttpTimeOut >> 1,
	}
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Fatalf("http server: bind %s: %s", srv.Addr, err)
	}
	// srv.Serve closes listener on clean exit; this guards the panic/fatal window
	// between bind and Serve. Double-close on the happy path is a harmless no-op,
	// so the error is intentionally discarded.
	defer func() { _ = listener.Close() }()
	log.Printf("http server: listening on %d port", HttpPort)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %s", err)
		}
	}()

	log.Println("initialization completed")

	<-ctx.Done()
	log.Println("http server: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http server: forced shutdown failed, %s", err)
	}
}

//go:embed static
var staticFS embed.FS

// openMeteoGeoAdapter adapts *weatherinfra.OpenMeteo to the gateway's
// weatherGeocoder interface, which expects []dto.WeatherCitySearchItem rather
// than the infrastructure-layer []weatherinfra.GeoResult. The adapter lives in
// cmd/web so that the gateway and handler layers do not import the infrastructure
// package.
type openMeteoGeoAdapter struct {
	client *weatherinfra.OpenMeteo
}

// Geocode implements the gateway weatherGeocoder interface.
func (a *openMeteoGeoAdapter) Geocode(ctx context.Context, name string, count int) ([]dto.WeatherCitySearchItem, error) {
	results, err := a.client.Geocode(ctx, name, count)
	if err != nil {
		return nil, err
	}
	items := make([]dto.WeatherCitySearchItem, 0, len(results))
	for _, r := range results {
		items = append(items, dto.WeatherCitySearchItem{
			LocationID:  weatherinfra.LocationKey(r),
			DisplayName: r.Name,
			Latitude:    r.Latitude,
			Longitude:   r.Longitude,
			Timezone:    r.Timezone,
			Country:     r.Country,
			Admin1:      r.Admin1,
		})
	}
	return items, nil
}

// flagPort, flagTimeout, etc. hold the raw flag values populated by flag.Parse in
// main. They are package-level so initFlags can apply them to the exported globals,
// keeping the flag-registration init() free of flag.Parse.
var (
	flagPort      *int
	flagTimeout   *string
	flagLogsDir   *string
	flagVerbosity *string
	flagStaticDir *string
	flagAPIDsn    *string
)

func init() {
	// Register flags here so the test binary can see them, but do NOT call
	// flag.Parse() in init() — it would consume go test's own flags before the
	// testing package registers them ("flag provided but not defined"). main()
	// calls flag.Parse() once; tests never invoke main().
	flagPort = flag.Int("port", HttpPort, "http server port")
	flagTimeout = flag.String("timeout", HttpTimeOut.String(), "HTTP read/write/idle timeout duration")
	flagLogsDir = flag.String("logs-dir", LogsDir, "path to logs directory")
	flagVerbosity = flag.String("verbosity", "warning", "minimum stdout log level (debug, info, warning, error, severe, critical)")
	flagStaticDir = flag.String("static-dir", StaticDir, "path to static files directory")
	flagAPIDsn = flag.String("api-dsn", APIDsn, "public HTTPS origin DSN, format: https://<host>/")
}

// initFlags applies the parsed flag values to the exported globals. Called once
// from main() after flag.Parse().
func initFlags() {
	if *flagPort <= 1000 || *flagPort >= 32000 {
		log.Printf("invalid port value: %d, using default %d", *flagPort, HttpPort)
	} else {
		HttpPort = *flagPort
	}

	if value, err := time.ParseDuration(*flagTimeout); err != nil {
		log.Printf("invalid timeout value: %s, using default %s", *flagTimeout, HttpTimeOut.String())
	} else if value > 10*time.Second {
		HttpTimeOut = value
	}

	if dir := *flagStaticDir; dir != "" {
		StaticDir = dir
	}

	if dir := *flagLogsDir; dir != "" {
		LogsDir = dir
	}

	if v := *flagVerbosity; v != "" {
		LogVerbosity = internal.ParseLogLevel(*flagVerbosity)
	}

	if v := *flagAPIDsn; v != "" {
		APIDsn = v
	}
}
