// Command doctor is the operator maintenance umbrella for the fx-rate-monitor
// service. It hosts two subcommands:
//
//	doctor rulegen <source>        generate or regenerate a rule for one source
//	doctor rulegen --all           regenerate rules for every active source (cron mode)
//	doctor audit   --all           probe every seeded source against its live URL
//	doctor audit   --source NAME   probe one source by exact name
//	doctor audit   --only REGEX    probe sources whose names match a regex
//
// rulegen logs to <logsDir>/doctor.YYYYMMDD.log; audit writes its report to
// stdout only.
//
// Top-level exit codes:
//
//	0  subcommand succeeded (subcommand-specific meaning)
//	2  unknown subcommand or no subcommand supplied
//
// For per-subcommand exit codes run:
//
//	doctor rulegen --help
//	doctor audit   --help
package main

import (
	"fmt"
	"io"
	"os"
	"path"

	"github.com/seilbekskindirov/monitor/internal"
)

var (
	// BuildVersion is the application version string, injected at link time via -ldflags.
	BuildVersion = "dev"
	// BuildTime is the build timestamp, injected at link time via -ldflags.
	BuildTime = "unknown"
	// BuildHash is the VCS commit hash, injected at link time via -ldflags.
	BuildHash = "undefined"
	// LogsDir is the directory where log files are written, overridable via --logs-dir.
	LogsDir = path.Join(os.TempDir(), "logs")
	// LogVerbosity is the minimum log level emitted by the logger.
	LogVerbosity = internal.LogLevelWarning
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. args is os.Args[1:]; out and errOut are the
// stdout and stderr writers respectively.
func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "doctor: expected a subcommand")
		printUsage(errOut)
		return 2
	}

	switch args[0] {
	case "rulegen":
		return runRulegen(args[1:], out, errOut)
	case "audit":
		return runAudit(args[1:], out, errOut)
	case "--help", "-h", "help":
		printUsage(out)
		return 0
	default:
		fmt.Fprintf(errOut, "doctor: unknown subcommand %q\n", args[0])
		printUsage(errOut)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  doctor <subcommand> [flags]

Subcommands:
  rulegen   Generate or regenerate extraction rules for rate sources via an LLM.
  audit     Probe seeded rate sources against their live URLs to verify rules.

Run "doctor <subcommand> --help" for subcommand-specific flags and exit codes.

Examples:
  doctor rulegen halyk_usd
  doctor rulegen --all
  doctor audit --all
  doctor audit --source halyk_usd`)
}
