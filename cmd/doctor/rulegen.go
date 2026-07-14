package main

// runRulegen implements the "doctor rulegen" subcommand. It generates or
// regenerates an extraction rule for a named rate source by asking an LLM,
// validating the rule against the live source URL, and persisting the result to
// the SQLite database.
//
// Flag surface:
//
//	<source-name>              positional argument (single-source mode)
//	--all                      iterate every active source (cron mode)
//	--force-fallback           skip primary, go straight to fallback AI
//	--max-primary-attempts N   max primary attempts before escalation (default 3)
//	--max-fallback-attempts N  max fallback attempts before total failure (default 2)
//	--logs-dir DIR             path to logs directory (default: os.TempDir()/logs)
//	--verbosity LEVEL          minimum log level (debug|info|warning|error|severe|critical)
//
// Environment variables:
//
//	BEACON_SQLITEDB_DSN      (required) SQLite connection string
//	BEACON_AI_PRIMARY_DSN    (required) primary AI provider DSN
//	BEACON_AI_FALLBACK_DSN   (optional) fallback AI provider DSN; stub used when absent
//	BEACON_CHROMIUM_PATH     (optional) absolute path to Chromium binary; when unset,
//	                         chromedp searches PATH (chromium, chromium-browser,
//	                         google-chrome, chrome)
//
// Exit codes (single-source mode):
//
//	0  success — rule generated and persisted
//	1  generation failed — source exists but no valid rule could be produced
//	2  usage error — missing argument, malformed flag, or --all combined with positional arg
//	3  infrastructure error — DB unreachable or migrations not applied
//
// Exit codes (--all mode):
//
//	0  normal completion — per-source failures are logged and counted but never escalated.
//	   Check the "rulegen --all:" summary line in stdout for succeeded/failed/skipped counts.
//	3  infrastructure error — DB unreachable, migrations not applied, or logger/AI client
//	   init failure.
//
// The summary line prefix is the literal "rulegen --all:" (not "doctor rulegen
// --all:") to preserve compatibility with external grep patterns and existing
// runall_test.go assertions.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/application/rulegen"
	"github.com/seilbekskindirov/beacon/internal/application/sourceaudit"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/artificialintelligence"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/seilbekskindirov/beacon/internal/tools/proxyutil"
	_ "modernc.org/sqlite"
)

const (
	envDsnSqliteDB   = "BEACON_SQLITEDB_DSN"
	envDsnAIPrimary  = "BEACON_AI_PRIMARY_DSN"
	envDsnAIFallback = "BEACON_AI_FALLBACK_DSN"
	// envChromiumPath is the optional absolute path to the Chromium/Chrome binary;
	// when unset, chromedp searches PATH (chromium, chromium-browser, google-chrome,
	// chrome).
	envChromiumPath = "BEACON_CHROMIUM_PATH"
	// envProxyURL is the optional outbound proxy URL parsed via dsninjector;
	// when unset or empty, outbound traffic goes direct.
	envProxyURL = "BEACON_PROXY_URL"
)

// rateSourceLister is the narrow read-side interface runAll needs, defined locally
// so tests can fake it without the concrete repository.
type rateSourceLister interface {
	ObtainAllRateSources(ctx context.Context) ([]domain.RateSource, error)
}

// ruleGenerator is the narrow generate interface runAll needs, defined locally so
// tests can fake it without rebuilding the full dependency graph.
type ruleGenerator interface {
	Generate(ctx context.Context, sourceName string, forceFallback bool) (*rulegen.Result, error)
}

// runAll invokes gen.Generate for every active rate source. It fetches all sources
// (active and inactive) via ObtainAllRateSources and counts inactive rows as skipped,
// filtering in Go not SQL so the summary line reflects the full inventory (plan
// trade-off R2). Per-source failures are logged to out and counted but not propagated;
// the return is always 0 so cron does not page on partial failure. Per-source panics
// are recovered, logged, and counted as failures. A lister failure is written to errOut
// and still returns 0 with a zero-count summary line on out, so "grep rulegen --all:
// cron.log" always matches.
func runAll(ctx context.Context, gen ruleGenerator, srcs rateSourceLister, forceFallback bool, out, errOut io.Writer) int {
	sources, err := srcs.ObtainAllRateSources(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "FAIL mode=--all reason=list sources: %v\n", err)
		fmt.Fprintf(out, "rulegen --all: processed=0 succeeded=0 failed=0 skipped=0\n")
		return 0
	}

	var processed, succeeded, failed, skipped int
	for _, src := range sources {
		if !src.Active {
			skipped++
			fmt.Fprintf(out, "SKIP source=%s reason=inactive\n", src.Name)
			continue
		}
		processed++
		func() {
			defer func() {
				if r := recover(); r != nil {
					failed++
					fmt.Fprintf(out, "FAIL source=%s reason=panic: %v\n", src.Name, r)
				}
			}()
			res, gerr := gen.Generate(ctx, src.Name, forceFallback)
			if gerr != nil {
				failed++
				fmt.Fprintf(out, "FAIL source=%s reason=%v\n", src.Name, gerr)
				return
			}
			succeeded++
			fmt.Fprintf(out, "OK source=%s rules=%d value=%g attempts=%d\n",
				src.Name, len(res.Rules), res.Value, res.AttemptsUsed)
		}()
	}
	fmt.Fprintf(out, "rulegen --all: processed=%d succeeded=%d failed=%d skipped=%d\n",
		processed, succeeded, failed, skipped)
	return 0
}

// runRulegen is the entry point for the "doctor rulegen" subcommand.
func runRulegen(args []string, out, errOut io.Writer) int {
	var (
		allSources    bool
		forceFallback bool
		maxPrimary    int
		maxFallback   int
		logsDir       string
		verbosity     string
	)

	fset := newFlagSet("rulegen", errOut)
	fset.BoolVar(&allSources, "all", false, "iterate every active source (cron mode; always exits 0)")
	fset.BoolVar(&forceFallback, "force-fallback", false, "skip primary, go straight to fallback")
	fset.IntVar(&maxPrimary, "max-primary-attempts", 3, "max primary attempts before escalation")
	fset.IntVar(&maxFallback, "max-fallback-attempts", 2, "max fallback attempts before total failure")
	fset.StringVar(&logsDir, "logs-dir", LogsDir, "path to logs directory")
	fset.StringVar(&verbosity, "verbosity", "warning", "minimum stdout log level (debug, info, warning, error, severe, critical)")
	// Suppress the FlagSet's built-in usage so help routes to out (not errOut)
	// without printing twice.
	fset.Usage = func() {}

	if err := fset.Parse(args); err != nil {
		if isHelpErr(err) {
			printRulegenUsage(out)
			return 0
		}
		fmt.Fprintf(errOut, "Run \"doctor rulegen --help\" for usage.\n")
		return 2
	}

	positional := fset.Args()

	if allSources && len(positional) > 0 {
		fmt.Fprintln(errOut, "usage error: --all and a positional source name are mutually exclusive")
		fmt.Fprintln(errOut, "")
		fset.PrintDefaults()
		return 2
	}

	if !allSources && len(positional) != 1 {
		fmt.Fprintln(errOut, "usage: doctor rulegen <source-name> [flags]")
		fmt.Fprintln(errOut, "       doctor rulegen --all [flags]")
		fmt.Fprintln(errOut, "")
		fset.PrintDefaults()
		return 2
	}

	resolvedLogsDir := LogsDir
	if logsDir != "" {
		resolvedLogsDir = logsDir
	}
	resolvedVerbosity := LogVerbosity
	if verbosity != "" {
		resolvedVerbosity = internal.ParseLogLevel(verbosity)
	}

	// infraFail prints an infrastructure FAIL line to errOut: "mode=--all" in --all
	// mode (so the key isn't mistaken for a source name), "source=<name>" otherwise.
	var infraFail func(format string, args ...any)
	if allSources {
		infraFail = func(format string, a ...any) {
			reason := fmt.Sprintf(format, a...)
			fmt.Fprintf(errOut, "FAIL mode=--all reason=%s\n", reason)
		}
	} else {
		sourceName := positional[0]
		infraFail = func(format string, a ...any) {
			reason := fmt.Sprintf(format, a...)
			fmt.Fprintf(errOut, "FAIL source=%s reason=%s\n", sourceName, reason)
		}
	}

	l, err := internal.NewLogger(resolvedLogsDir, "doctor", resolvedVerbosity)
	if err != nil {
		infraFail("logger init: %v", err)
		return 3
	}

	proxyURL := proxyutil.ResolveURL(envProxyURL)

	dsnSQLiteDB, err := dsninjector.Unmarshal(envDsnSqliteDB)
	if err != nil {
		infraFail("settings %s: %v", envDsnSqliteDB, err)
		return 3
	}

	dsnAIPrimary, err := dsninjector.Unmarshal(envDsnAIPrimary)
	if err != nil {
		infraFail("settings %s: %v", envDsnAIPrimary, err)
		return 3
	}

	db, err := sqlitedb.NewSQLiteClient(dsnSQLiteDB, l.WriterAs(internal.LogLevelInfo))
	if err != nil {
		infraFail("sqlite connection: %v", err)
		return 3
	}
	defer func() {
		if e := db.Close(); e != nil {
			log.Printf("close sqlite: %v", e)
		}
	}()

	if err = sqlitedb.RequireMigratedSchema(context.Background(), db); err != nil {
		infraFail("schema check: %v", err)
		return 3
	}

	aiPrimary, err := artificialintelligence.NewClient(dsnAIPrimary, l.WriterAs(internal.LogLevelInfo), proxyURL)
	if err != nil {
		infraFail("ai primary client: %v", err)
		return 3
	}

	var aiFallback artificialintelligence.AIClient
	if _, ok := os.LookupEnv(envDsnAIFallback); ok {
		dsnAIFallback, dsnErr := dsninjector.Unmarshal(envDsnAIFallback)
		if dsnErr != nil {
			infraFail("settings %s: %v", envDsnAIFallback, dsnErr)
			return 3
		}
		aiFallback, err = artificialintelligence.NewClient(dsnAIFallback, l.WriterAs(internal.LogLevelInfo), proxyURL)
		if err != nil {
			infraFail("ai fallback client: %v", err)
			return 3
		}
	} else {
		aiFallback, err = artificialintelligence.NewStubClient()
		if err != nil {
			infraFail("ai fallback stub: %v", err)
			return 3
		}
	}

	rRateSource, err := repository.NewRateSourceRepository(db)
	if err != nil {
		infraFail("rate source repo: %v", err)
		return 3
	}

	plainHTTPFetcher, err := sourceaudit.NewHTTPFetcher(time.Minute, proxyURL)
	if err != nil {
		infraFail("plain fetcher build: %v", err)
		return 3
	}
	plainFetcher := &sourceAuditFetcherAdapter{inner: plainHTTPFetcher}

	chromedpFor := func(waitSelector string) rulegen.Fetcher {
		return rulegen.NewChromedpFetcher(rulegen.ChromedpFetcherOptions{
			ChromiumPath: os.Getenv(envChromiumPath),
			ProxyURL:     proxyURL,
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
		maxPrimary,
		maxFallback,
		l.WriterAs(internal.LogLevelInfo),
	)
	if err != nil {
		infraFail("build generator: %v", err)
		return 3
	}

	if allSources {
		return runAll(context.Background(), gen, rRateSource, forceFallback, out, errOut)
	}

	sourceName := positional[0]
	res, err := gen.Generate(context.Background(), sourceName, forceFallback)
	if err != nil {
		fmt.Fprintf(errOut, "FAIL source=%s reason=%v\n", sourceName, err)
		if errors.Is(err, rulegen.ErrUnsupportedFetcherKind) {
			return 2
		}
		return 1
	}

	fmt.Fprintf(out, "OK source=%s rules=%d value=%g attempts=%d escalated=%t provider=%s model=%s\n",
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

var _ rulegen.Fetcher = (*sourceAuditFetcherAdapter)(nil)

// sourceAuditFetcherAdapter adapts sourceaudit.Fetcher to rulegen.Fetcher, which
// returns only the body bytes. Headers are forwarded to the inner Fetcher so
// per-source header-dependent sources (e.g. Yahoo Finance) work correctly in rulegen.
type sourceAuditFetcherAdapter struct {
	inner sourceaudit.Fetcher
}

func (a *sourceAuditFetcherAdapter) Fetch(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	result, err := a.inner.Fetch(ctx, url, headers)
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}

func printRulegenUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  doctor rulegen <source-name> [flags]
  doctor rulegen --all [flags]

Generates or regenerates an extraction rule for a rate source using an LLM,
validates the rule against the live URL, and persists it to the database.

Flags:
  --all                      iterate every active source (cron mode; always exits 0)
  --force-fallback           skip primary, go straight to fallback AI
  --max-primary-attempts N   max primary attempts before escalation (default 3)
  --max-fallback-attempts N  max fallback attempts before total failure (default 2)
  --logs-dir DIR             path to logs directory
  --verbosity LEVEL          minimum log level (debug|info|warning|error|severe|critical)

Environment variables:
  BEACON_SQLITEDB_DSN    (required) SQLite connection string
  BEACON_AI_PRIMARY_DSN  (required) primary AI provider DSN
  BEACON_AI_FALLBACK_DSN (optional) fallback AI provider DSN; stub used when absent
  BEACON_CHROMIUM_PATH   (optional) absolute path to Chromium binary
  BEACON_PROXY_URL       (optional) outbound proxy URL, e.g. http://127.0.0.1:7788

Exit codes (single-source mode):
  0  success — rule generated and persisted
  1  generation failed
  2  usage error
  3  infrastructure error

Exit codes (--all mode):
  0  normal completion (check "rulegen --all:" summary line for counts)
  3  infrastructure error`)
}
