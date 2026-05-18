package internal

import (
	"bytes"
	"fmt"
	"io"
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

	fHNDL := loginjector.CyclicOverwritingFilesHandler(folder, name, defaultFileSize, 7)

	l, err := loginjector.NewLogger(LogLevelWarning, fHNDL)
	if err != nil {
		return nil, err
	}

	var printerLevels []loginjector.LogLevel
	for _, lvl := range []loginjector.LogLevel{LogLevelDebug, LogLevelInfo, LogLevelWarning, LogLevelError, LogLevelSevere, LogLevelCritical} {
		if lvl >= printerMinLevel {
			printerLevels = append(printerLevels, lvl)
		}
	}
	if len(printerLevels) > 0 {
		_ = l.Hook(&printer{}, printerLevels[0], printerLevels[1:]...)
	}

	log.SetOutput(l.WriterAs(LogLevelWarning))
	log.SetFlags(0) // timestamp is added by printer; avoid duplicating it

	return l, err
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

// Logger wraps loginjector.Logger with an optional Telegram alert hook.
type Logger struct {
	*loginjector.Logger
	telegramHookID loginjector.HookID
}

// SetTelegramHandler attaches a Telegram notification hook to the logger.
// Error-level and severe-level log entries are forwarded to the specified Telegram chat.
func (l *Logger) SetTelegramHandler(token, chatID string) {
	hndl := loginjector.TelegramHandler(token, chatID, "error", "lingocrm.product.api.error")
	l.telegramHookID = l.Logger.Hook(hndl, LogLevelError, LogLevelSevere)
}

type printer struct{}

func (p *printer) Write(msg []byte) (n int, err error) {
	s := string(bytes.TrimSpace(msg))
	if len(s) == 0 {
		return 0, nil
	}
	items := strings.Split(s, "\n")
	s = strings.Join(items, "\n                    ")
	_, _ = fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().Format("2006/01/02 15:04:05"), s)
	return len(msg), nil
}

// CloseOrLogError closes c and writes any resulting error to w.
func CloseOrLogError(w io.Writer, c io.Closer) {
	if err := c.Close(); err != nil {
		logMsg := fmt.Sprintf("error closing resource: %s\n", err.Error())
		_, _ = w.Write([]byte(logMsg))
	}
}
