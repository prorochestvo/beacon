// Command rulegen generates an extraction rule for a named rate source by asking
// an LLM, validating the rule against the live source URL, and persisting the
// result to the SQLite database.
//
// Usage:
//
//	rulegen <source-name> [flags]
//
// Flags:
//
//	--force-fallback           skip primary, go straight to fallback
//	--max-primary-attempts N   max primary attempts before escalation (default 3)
//	--max-fallback-attempts N  max fallback attempts before total failure (default 2)
//	--logs-dir DIR             path to logs directory
//	--verbosity LEVEL          minimum log level (debug|info|warning|error|severe|critical)
//
// Exit codes:
//
//	0  success — rule generated and persisted
//	1  generation failed — source exists but no valid rule could be produced
//	2  usage error — missing argument or malformed flag
//	3  infrastructure error — DB unreachable or migrations not applied
//
// Environment variables:
//
//	SQLITEDB_DSN      (required) SQLite connection string
//	AI_PRIMARY_DSN    (required) primary AI provider DSN
//	AI_FALLBACK_DSN   (optional) fallback AI provider DSN; stub used when absent
//	CHROMIUM_PATH     (optional) absolute path to Chromium/Chrome binary;
//	                  defaults to chromedp PATH lookup (chromium, chromium-browser, google-chrome, chrome)
//
// Each invocation makes up to maxPrimaryAttempts + maxFallbackAttempts LLM calls.
// Ensure your provider account has sufficient budget before running on many sources.
// The constant locateWindowBytes in internal/application/rulegen/sanitizer.go
// controls the body window size (80 KB by default, centred on the first anchor hit).
//
// When using a stub fallback (no AI_FALLBACK_DSN), metadata.Model will record "stub".
// This is intentional — it makes stub-generated rules trivially greppable in the DB.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/internal/application/sourceaudit"
	"github.com/seilbekskindirov/monitor/internal/infrastructure/artificialintelligence"
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
	// LogVerbosity controls the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

const (
	envDsnSqliteDB   = "SQLITEDB_DSN"
	envDsnAIPrimary  = "AI_PRIMARY_DSN"
	envDsnAIFallback = "AI_FALLBACK_DSN"
	// envChromiumPath is an optional absolute path to the Chromium/Chrome binary.
	// When unset, chromedp falls back to its own PATH lookup order:
	// chromium, chromium-browser, google-chrome, chrome.
	envChromiumPath = "CHROMIUM_PATH"
)

func main() {
	os.Exit(run())
}

func run() int {
	forceFallback := flag.Bool("force-fallback", false, "skip primary, go straight to fallback")
	maxPrimary := flag.Int("max-primary-attempts", 3, "max primary attempts before escalation")
	maxFallback := flag.Int("max-fallback-attempts", 2, "max fallback attempts before total failure")
	logsDir := flag.String("logs-dir", LogsDir, "path to logs directory")
	verbosity := flag.String("verbosity", "warning", "minimum stdout log level (debug, info, warning, error, severe, critical)")
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: rulegen <source-name> [flags]")
		fmt.Fprintln(os.Stderr, "")
		flag.PrintDefaults()
		return 2
	}
	sourceName := args[0]

	if dir := *logsDir; dir != "" {
		LogsDir = dir
	}
	if v := *verbosity; v != "" {
		LogVerbosity = internal.ParseLogLevel(v)
	}

	l, err := internal.NewLogger(LogsDir, "rulegen", LogVerbosity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=logger init: %v\n", sourceName, err)
		return 3
	}

	dsnSQLiteDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=settings %s: %v\n", sourceName, envDsnSqliteDB, err)
		return 3
	}

	dsnAIPrimary, err := dsninjector.Unmarshal(envDsnAIPrimary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=settings %s: %v\n", sourceName, envDsnAIPrimary, err)
		return 3
	}

	db, err := sqlitedb.NewSQLiteClient(dsnSQLiteDB, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=sqlite connection: %v\n", sourceName, err)
		return 3
	}
	defer func() {
		if e := db.Close(); e != nil {
			log.Printf("close sqlite: %v", e)
		}
	}()

	if err = sqlitedb.RequireMigratedSchema(context.Background(), db); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=schema check: %v\n", sourceName, err)
		return 3
	}

	aiPrimary, err := artificialintelligence.NewClient(dsnAIPrimary, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=ai primary client: %v\n", sourceName, err)
		return 3
	}

	var aiFallback artificialintelligence.AIClient
	if _, ok := os.LookupEnv(envDsnAIFallback); ok {
		dsnAIFallback, dsnErr := dsninjector.Unmarshal(envDsnAIFallback)
		if dsnErr != nil {
			fmt.Fprintf(os.Stderr, "FAIL source=%s reason=settings %s: %v\n", sourceName, envDsnAIFallback, dsnErr)
			return 3
		}
		aiFallback, err = artificialintelligence.NewClient(dsnAIFallback, l.WriterAs(internal.LogLevelInfo))
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL source=%s reason=ai fallback client: %v\n", sourceName, err)
			return 3
		}
	} else {
		aiFallback, err = artificialintelligence.NewStubClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL source=%s reason=ai fallback stub: %v\n", sourceName, err)
			return 3
		}
	}

	rRateSource, err := repository.NewRateSourceRepository(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=rate source repo: %v\n", sourceName, err)
		return 3
	}

	plainFetcher := &sourceAuditFetcherAdapter{inner: sourceaudit.NewHTTPFetcher(time.Minute)}

	chromedpFor := func(waitSelector string) rulegen.Fetcher {
		return rulegen.NewChromedpFetcher(rulegen.ChromedpFetcherOptions{
			ChromiumPath: os.Getenv(envChromiumPath),
			Logger:       l.WriterAs(internal.LogLevelInfo),
			WaitSelector: waitSelector,
		})
	}

	gen, err := rulegen.NewGenerator(
		aiPrimary,
		aiFallback,
		plainFetcher,
		chromedpFor,
		rulegen.NewRuleExecutor(),
		rRateSource,
		*maxPrimary,
		*maxFallback,
		l.WriterAs(internal.LogLevelInfo),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=build generator: %v\n", sourceName, err)
		return 3
	}

	res, err := gen.Generate(context.Background(), sourceName, *forceFallback)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL source=%s reason=%v\n", sourceName, err)
		if errors.Is(err, rulegen.ErrUnsupportedFetcherKind) {
			return 2
		}
		return 1
	}

	fmt.Printf("OK source=%s rules=%d value=%g attempts=%d escalated=%t provider=%s model=%s\n",
		sourceName,
		len(res.Rules),
		res.Value,
		res.AttemptsUsed,
		res.Escalated,
		res.Metadata.Provider,
		res.Metadata.Model,
	)
	return 0
}

// sourceAuditFetcherAdapter wraps sourceaudit.Fetcher to satisfy the
// rulegen.Fetcher interface, which returns only the body bytes.
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
