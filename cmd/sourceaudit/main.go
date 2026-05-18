// Command sourceaudit probes seeded rate sources against the live web to verify
// that their extraction rules still return plausible values. Results are printed
// to stdout; non-zero exit code indicates at least one probe failure.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"regexp"
	"time"

	"github.com/seilbekskindirov/monitor/internal/application/sourceaudit"
)

func main() {
	os.Exit(run())
}

func run() int {
	seedGlob := flag.String("seed-glob", "migrations/*.seed*.sql", "glob for seed SQL files")
	onlyRe := flag.String("only", "", "regex filtering source names (empty = all sources)")
	verbose := flag.Bool("v", false, "verbose: print per-source table; default prints only OK summary or MISS DETAILS")
	flag.Parse()

	var nameFilter *regexp.Regexp
	if *onlyRe != "" {
		var err error
		nameFilter, err = regexp.Compile(*onlyRe)
		if err != nil {
			log.Fatalf("sourceaudit: compile --only regex: %v", err)
		}
	}

	sources, err := sourceaudit.ParseSeedFiles(os.DirFS("."), *seedGlob)
	if err != nil {
		log.Fatalf("sourceaudit: parse seed files: %v", err)
	}
	if len(sources) == 0 {
		log.Fatalf("sourceaudit: no sources found matching glob %q — check --seed-glob", *seedGlob)
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

	auditor := &sourceaudit.Auditor{Fetcher: sourceaudit.NewHTTPFetcher(time.Minute)}
	results, err := auditor.Run(context.Background(), sources)
	if err != nil {
		log.Fatalf("sourceaudit: run: %v", err)
	}

	failures, err := sourceaudit.WriteReport(os.Stdout, results, *verbose)
	if err != nil {
		log.Fatalf("sourceaudit: write report: %v", err)
	}

	if failures > 0 {
		return 1
	}
	return 0
}
