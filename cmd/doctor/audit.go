package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/application/sourceaudit"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/seilbekskindirov/beacon/internal/tools/proxyutil"
	"github.com/seilbekskindirov/beacon/migrations"
)

// runAudit is the entry point for the "doctor audit" subcommand. It audits
// INSERT-only seed rules by default; seed mode does not reflect later UPDATE
// or DELETE migrations. Use --dsn to audit effective rules from a migrated DB.
//
// It must be invoked from the repository root: the default --seed-glob
// ("migrations/*.seed*.sql") is relative to the working directory.
//
// Flag surface:
//
//	--seed-glob string   glob pattern for seed SQL files (default "migrations/*.seed*.sql")
//	--dsn string         SQLite DSN for effective rules in a migrated database
//	--all                audit every seeded source
//	--source string      exact source name to audit (mutually exclusive with --all and --only)
//	--only string        regex filter on source names (mutually exclusive with --all and --source)
//	-v                   verbose: print per-source table
//
// Exactly one of --all, --source, or --only must be supplied; mixing them is a
// usage error.
//
// --dsn opens the database file read-write (the connection applies
// journal_mode=WAL), so point it at a throwaway copy, never at a live service
// database. The DSN comes from the flag only, never from BEACON_SQLITEDB_DSN.
//
// Exit codes:
//
//	0  all probes OK
//	1  at least one source reported MISS
//	2  usage error (missing or mutually exclusive flags, bad regex)
//	3  infrastructure error (glob failure, no sources found, auditor or report error)
//
// A nil fetcher selects the default HTTP fetcher; a nil seedFS selects
// os.DirFS(".") for seed-glob resolution. Callers that have no reason to
// override either pass nil for both — see cmd/doctor/main.go.
func runAudit(args []string, fetcher sourceaudit.Fetcher, seedFS fs.FS, out, errOut io.Writer) int {
	var (
		seedGlob string
		dsn      string
		onlyRe   string
		source   string
		all      bool
		verbose  bool
	)

	fset := newFlagSet("audit", errOut)
	fset.StringVar(&seedGlob, "seed-glob", "migrations/*.seed*.sql", "glob for seed SQL files")
	fset.StringVar(&dsn, "dsn", "", "SQLite DSN for effective rules in a migrated database")
	fset.StringVar(&onlyRe, "only", "", "regex filter on source names")
	fset.StringVar(&source, "source", "", "exact source name to audit (shorthand for --only=^<name>$)")
	fset.BoolVar(&all, "all", false, "audit every seeded source")
	fset.BoolVar(&verbose, "v", false, "verbose: print per-source table")
	// Suppress the FlagSet's built-in usage so help routes to out (not errOut)
	// without printing twice.
	fset.Usage = func() {}

	if err := fset.Parse(args); err != nil {
		if isHelpErr(err) {
			printAuditUsage(out)
			return 0
		}
		fmt.Fprintf(errOut, "Run \"doctor audit --help\" for usage.\n")
		return 2
	}
	seedGlobSet := false
	fset.Visit(func(f *flag.Flag) {
		if f.Name == "seed-glob" {
			seedGlobSet = true
		}
	})
	if dsn != "" && seedGlobSet {
		fmt.Fprintln(errOut, "doctor audit: --dsn and --seed-glob cannot be used together")
		return 2
	}
	var ds *dsninjector.DataSourceMapper
	if dsn != "" {
		var err error
		ds, err = dsninjector.Parse(dsn)
		if err != nil {
			fmt.Fprintf(errOut, "doctor audit: parse --dsn: %v\n", err)
			return 2
		}
	}

	// Validate mutually exclusive flag combinations before any I/O.
	switch {
	case all && source != "":
		fmt.Fprintln(errOut, "doctor audit: --all and --source are mutually exclusive")
		return 2
	case all && onlyRe != "":
		fmt.Fprintln(errOut, "doctor audit: --all and --only are mutually exclusive")
		return 2
	case source != "" && onlyRe != "":
		fmt.Fprintln(errOut, "doctor audit: --source and --only are mutually exclusive")
		return 2
	case !all && source == "" && onlyRe == "":
		fmt.Fprintln(errOut, "doctor audit: specify --all, --source, or --only")
		return 2
	}

	var nameFilter *regexp.Regexp
	if source != "" {
		// Anchor and quote-meta so dots, underscores, etc. are matched literally,
		// not as regex metacharacters.
		nameFilter = regexp.MustCompile(`^` + regexp.QuoteMeta(source) + `$`)
	} else if onlyRe != "" {
		var err error
		nameFilter, err = regexp.Compile(onlyRe)
		if err != nil {
			fmt.Fprintf(errOut, "doctor audit: compile --only regex: %v\n", err)
			return 2
		}
	}

	// The load step bounds its own time; the audit itself runs for minutes and must not share a deadline.
	ctx := context.Background()

	var sources []sourceaudit.SeededSource
	if dsn != "" {
		var code int
		sources, code = loadDBSources(ctx, ds, errOut)
		if code != 0 {
			return code
		}
		if len(sources) == 0 {
			fmt.Fprintln(errOut, "doctor audit: no active plain-fetcher sources in rate_sources")
			return 3
		}
	} else {
		if strings.Contains(seedGlob, "..") {
			fmt.Fprintf(errOut, "doctor audit: --seed-glob must not contain '..'\n")
			return 2
		}

		if seedFS == nil {
			seedFS = os.DirFS(".")
		}
		var err error
		sources, err = sourceaudit.ParseSeedFiles(seedFS, seedGlob)
		if err != nil {
			fmt.Fprintf(errOut, "doctor audit: parse seed files: %v\n", err)
			return 3
		}
		if len(sources) == 0 {
			fmt.Fprintf(errOut, "doctor audit: no sources found matching glob %q — check --seed-glob\n", seedGlob)
			return 3
		}
	}

	if nameFilter != nil {
		var filtered []sourceaudit.SeededSource
		for _, s := range sources {
			if nameFilter.MatchString(s.Name) {
				filtered = append(filtered, s)
			}
		}
		sources = filtered
	}

	if len(sources) == 0 {
		fmt.Fprintln(errOut, "doctor audit: no sources matched the given filter")
		return 3
	}

	proxyURL := proxyutil.ResolveURL(internal.EnvProxyURL)
	if dsn != "" && proxyURL != "" {
		fmt.Fprintln(errOut, "doctor audit: warning: BEACON_PROXY_URL is set, so the audit proxies every source; the collector proxies only rows with options.use_proxy")
	}
	if fetcher == nil {
		var fetcherErr error
		fetcher, fetcherErr = sourceaudit.NewHTTPFetcher(time.Minute, proxyURL, internal.UserAgent)
		if fetcherErr != nil {
			fmt.Fprintf(errOut, "doctor audit: build fetcher: %v\n", fetcherErr)
			return 3
		}
	}
	auditor := &sourceaudit.Auditor{Fetcher: fetcher}
	results, err := auditor.Run(ctx, sources)
	if err != nil {
		fmt.Fprintf(errOut, "doctor audit: run: %v\n", err)
		return 3
	}

	failures, err := sourceaudit.WriteReport(out, results, verbose)
	if err != nil {
		fmt.Fprintf(errOut, "doctor audit: write report: %v\n", err)
		return 3
	}

	if failures > 0 {
		return 1
	}
	return 0
}

// loadDBSources reads the effective rate_sources rows of a migrated database,
// bounded by a 30-second deadline derived from ctx.
func loadDBSources(ctx context.Context, ds *dsninjector.DataSourceMapper, errOut io.Writer) (sources []sourceaudit.SeededSource, code int) {
	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if _, err := os.Stat(ds.Database()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(errOut, "doctor audit: database file not found")
		} else {
			fmt.Fprintf(errOut, "doctor audit: stat database: %v\n", err)
		}
		return nil, 3
	}
	//nolint:contextcheck // NewSQLiteClient takes no context; it opens a local file and has no cancellable work
	db, err := sqlitedb.NewSQLiteClient(ds)
	if err != nil {
		fmt.Fprintf(errOut, "doctor audit: open database: %v\n", err)
		return nil, 3
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			fmt.Fprintf(errOut, "doctor audit: close database: %v\n", closeErr)
			code = 3
		}
	}()
	if err = sqlitedb.RequireMigratedSchema(loadCtx, db, migrations.MigrationsFS); err != nil {
		fmt.Fprintf(errOut, "doctor audit: schema behind this build: run cmd/migrator: %v\n", err)
		return nil, 3
	}
	repo, err := repository.NewRateSourceRepository(db)
	if err != nil {
		fmt.Fprintf(errOut, "doctor audit: create rate source repository: %v\n", err)
		return nil, 3
	}
	rows, err := repo.ObtainAllRateSources(loadCtx)
	if err != nil {
		fmt.Fprintf(errOut, "doctor audit: read rate_sources: %v\n", err)
		return nil, 3
	}
	sources, skipped, inactive := sourceaudit.SourcesFromRateSources(rows)
	fmt.Fprintf(errOut, "doctor audit: %d active sources from rate_sources (%d inactive not audited)\n", len(sources)+len(skipped), inactive)
	for _, row := range skipped {
		fmt.Fprintf(errOut, "doctor audit: skipped %s: fetcher_kind=%s is not audited (plain HTTP only)\n", row.Name, row.FetcherKind)
	}
	return sources, 0
}

func printAuditUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  doctor audit [flags]

Probes rate sources against their live URLs to verify extraction rules. By default,
sources come from seed SQL files; --dsn loads effective rules from a migrated database.
Seed mode's --seed-glob is relative to CWD.
Seed mode reads INSERT statements only, so later UPDATE/DELETE migrations are not reflected;
--dsn audits effective rules from a migrated database.

Flags:
  --seed-glob string   glob pattern for seed SQL files (default "migrations/*.seed*.sql")
  --dsn string         SQLite DSN for effective rules in a migrated database (mutually exclusive with --seed-glob).
                       Opens the file read-write (WAL is applied): use a throwaway copy, never a live database.
  --all                audit every seeded source
  --source string      exact source name to audit (mutually exclusive with --all and --only)
  --only string        regex filter on source names (mutually exclusive with --all and --source)
  -v                   verbose: print per-source table

Exactly one of --all, --source, or --only must be supplied.

Exit codes:
  0  all probes OK
  1  at least one source reported MISS
  2  usage error
  3  infrastructure error (seed-glob failure, no sources found, auditor error)

Examples:
  doctor audit --all
  doctor audit --source halyk_usd
  doctor audit --only '^halyk_'
  doctor audit --all -v`)
}
