package internal

import (
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/prorochestvo/loginjector"
)

const (
	// LogLevelDebug is the most verbose log level, used for detailed diagnostic output.
	LogLevelDebug loginjector.LogLevel = 1
	// LogLevelInfo records normal operational messages.
	LogLevelInfo loginjector.LogLevel = 2
	// LogLevelWarning records recoverable problems or unexpected conditions.
	LogLevelWarning loginjector.LogLevel = 3
	// LogLevelError records failures that require attention but do not crash the service.
	LogLevelError loginjector.LogLevel = 4
	// LogLevelSevere records critical failures that may require immediate action.
	LogLevelSevere loginjector.LogLevel = 5
	// LogLevelCritical records the most severe failures, typically leading to shutdown.
	LogLevelCritical loginjector.LogLevel = 6

	defaultFileSize = 5 * 1024 * 1024 // 5MB
)

// NewLogger creates a new file-based logger that writes cyclic log files to logsDir.
// name is used as the log file prefix (e.g. "collector", "web", "api").
// Log entries at or above printerMinLevel are also printed to stdout.
// If logsDir is empty, a default temp directory is used.
// If name is empty, "app" is used as the prefix.
func NewLogger(logsDir, name string, printerMinLevel loginjector.LogLevel) (*loginjector.Logger, error) {
	folder := logsDir
	if folder == "" {
		folder = path.Join(os.TempDir(), "logs")
	}
	if name == "" {
		name = "app"
	}
	err := os.MkdirAll(folder, os.ModePerm)
	if err != nil {
		return nil, err
	}
	defer log.Printf("logs folder: %s", folder)

	// WithFileMode 0600: log lines carry chat_id (PII-adjacent per the data policy),
	// so keep files owner-only. Applies to files the handler creates; an existing live
	// file opened O_APPEND keeps its prior mode until the next rotation.
	fHNDL := loginjector.RotatingFileHandler(folder, name, loginjector.WithMaxFileSize(defaultFileSize), loginjector.WithMaxFiles(7), loginjector.WithFileMode(0o600))

	// The file is the sink that survives — it rotates, outlives the process, and is what
	// gets read weeks later. Without a timestamp on it, an incident can only be located
	// by line number, never dated or correlated with a deploy, a restart or a snapshot.
	// Every issue investigated in this project so far cites line ranges for exactly that
	// reason.
	//
	// RFC3339 rather than a bare clock time: it sorts lexicographically, matches how
	// timestamps are stored everywhere else in this project, and carries its own UTC
	// offset — so a host whose timezone changes says so in the log instead of quietly
	// renumbering history.
	l := loginjector.NewLogger(LogLevelWarning, loginjector.TimestampedHandler(fHNDL, loginjector.WithTimeLayout(time.RFC3339)))

	var printerLevels []loginjector.LogLevel
	for _, lvl := range []loginjector.LogLevel{LogLevelDebug, LogLevelInfo, LogLevelWarning, LogLevelError, LogLevelSevere, LogLevelCritical} {
		if lvl >= printerMinLevel {
			printerLevels = append(printerLevels, lvl)
		}
	}
	if len(printerLevels) > 0 {
		_ = l.Hook(loginjector.TimestampedPrintHandler(loginjector.WithTimeLayout(time.RFC3339)), printerLevels[0], printerLevels[1:]...)
	}

	SetRuntimeDetailsProvider()

	log.SetOutput(l.WriterAs(LogLevelWarning))
	// Both sinks now timestamp at the handler, so the standard logger must not add its
	// own or every line carries two.
	log.SetFlags(0)

	return l, nil
}

// ParseLogLevel converts a string name to a LogLevel constant.
// Accepted values (case-insensitive): debug, info, warning, error, severe, critical.
// Defaults to LogLevelInfo for unrecognised input.
func ParseLogLevel(s string) loginjector.LogLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LogLevelDebug
	case "warning":
		return LogLevelWarning
	case "error":
		return LogLevelError
	case "severe":
		return LogLevelSevere
	case "critical":
		return LogLevelCritical
	default:
		return LogLevelInfo
	}
}
