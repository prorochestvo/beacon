package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal/application/sourceaudit"
	"github.com/seilbekskindirov/monitor/internal/tools/proxyutil"
)

// runAudit is the entry point for the "doctor audit" subcommand.
//
// It must be invoked from the repository root: the default --seed-glob
// ("migrations/*.seed*.sql") is relative to the working directory.
//
// Flag surface:
//
//	--seed-glob string   glob pattern for seed SQL files (default "migrations/*.seed*.sql")
//	--all                audit every seeded source
//	--source string      exact source name to audit (mutually exclusive with --all and --only)
//	--only string        regex filter on source names (mutually exclusive with --all and --source)
//	-v                   verbose: print per-source table
//
// Exactly one of --all, --source, or --only must be supplied; mixing them is a
// usage error.
//
// Exit codes:
//
//	0  all probes OK
//	1  at least one source reported MISS
//	2  usage error (missing or mutually exclusive flags, bad regex)
//	3  infrastructure error (glob failure, no sources found, auditor or report error)
func runAudit(args []string, out, errOut io.Writer) int {
	return runAuditWith(args, nil, nil, out, errOut)
}

// runAuditWith is the internal implementation of runAudit. A non-nil fetcher
// replaces the default HTTP fetcher; a non-nil seedFS replaces os.DirFS(".") for
// seed-glob resolution. Both exist so tests can inject stubs without hitting the
// network or relying on CWD layout.
func runAuditWith(args []string, fetcher sourceaudit.Fetcher, seedFS fs.FS, out, errOut io.Writer) int {
	var (
		seedGlob string
		onlyRe   string
		source   string
		all      bool
		verbose  bool
	)

	fset := newFlagSet("audit", errOut)
	fset.StringVar(&seedGlob, "seed-glob", "migrations/*.seed*.sql", "glob for seed SQL files")
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

	if strings.Contains(seedGlob, "..") {
		fmt.Fprintf(errOut, "doctor audit: --seed-glob must not contain '..'\n")
		return 2
	}

	if seedFS == nil {
		seedFS = os.DirFS(".")
	}
	sources, err := sourceaudit.ParseSeedFiles(seedFS, seedGlob)
	if err != nil {
		fmt.Fprintf(errOut, "doctor audit: parse seed files: %v\n", err)
		return 3
	}
	if len(sources) == 0 {
		fmt.Fprintf(errOut, "doctor audit: no sources found matching glob %q — check --seed-glob\n", seedGlob)
		return 3
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

	if fetcher == nil {
		proxyURL := proxyutil.ResolveURL(envProxyURL)
		var fetcherErr error
		fetcher, fetcherErr = sourceaudit.NewHTTPFetcher(time.Minute, proxyURL)
		if fetcherErr != nil {
			fmt.Fprintf(errOut, "doctor audit: build fetcher: %v\n", fetcherErr)
			return 3
		}
	}
	auditor := &sourceaudit.Auditor{Fetcher: fetcher}
	results, err := auditor.Run(context.Background(), sources)
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

func printAuditUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  doctor audit [flags]

Probes seeded rate sources against their live URLs to verify extraction rules.
Must be run from the repository root (--seed-glob is relative to CWD).

Flags:
  --seed-glob string   glob pattern for seed SQL files (default "migrations/*.seed*.sql")
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
