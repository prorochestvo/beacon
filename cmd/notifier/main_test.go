package main

import (
	"flag"
	"testing"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/stretchr/testify/require"
)

// TestInitFlags is also the regression guard for flag.Parse() in init(): package
// init runs before testing.MainStart registers the -test.* flags, so parsing there
// makes every test in this package die with "flag provided but not defined". This
// file only runs at all because main() owns the parse.
//
// not t.Parallel() — initFlags mutates the package-level LogsDir and LogVerbosity,
// and the subtests swap the flag pointers those read from.
//
//nolint:paralleltest // deliberately serial; the reason is stated directly above
func TestInitFlags(t *testing.T) {
	origDir, origVerbosity := LogsDir, LogVerbosity
	origFlagDir, origFlagVerbosity := flagLogsDir, flagVerbosity
	t.Cleanup(func() {
		LogsDir, LogVerbosity = origDir, origVerbosity
		flagLogsDir, flagVerbosity = origFlagDir, origFlagVerbosity
	})

	t.Run("applies both values", func(t *testing.T) {
		dir, verbosity := "/custom/logs", "debug"
		flagLogsDir, flagVerbosity = &dir, &verbosity
		initFlags()
		require.Equal(t, "/custom/logs", LogsDir)
		require.Equal(t, internal.ParseLogLevel("debug"), LogVerbosity)
	})

	t.Run("empty values leave the defaults alone", func(t *testing.T) {
		LogsDir, LogVerbosity = "/kept", internal.LogLevelWarning
		empty := ""
		flagLogsDir, flagVerbosity = &empty, &empty
		initFlags()
		require.Equal(t, "/kept", LogsDir)
		require.Equal(t, internal.LogLevelWarning, LogVerbosity)
	})
}

// TestFlagsAreRegistered pins that init registers the flags rather than parsing them,
// so the binary still accepts them on the command line.
func TestFlagsAreRegistered(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"logs-dir", "verbosity"} {
		require.NotNilf(t, flag.Lookup(name), "flag %q must be registered in init", name)
	}
}
