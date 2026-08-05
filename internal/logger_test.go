package internal

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readLogFile returns the lines the rotating handler wrote into dir.
func readLogFile(t *testing.T, dir string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, "*.log"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "the handler wrote no log file")

	raw, err := os.ReadFile(matches[0])
	require.NoError(t, err)

	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	require.NotEmpty(t, lines, "the log file is empty")
	return lines
}

// newLoggerInTempDir builds a logger writing into a fresh directory and restores the
// global standard-logger state NewLogger mutates.
//
// Not parallel on purpose: NewLogger calls log.SetOutput and log.SetFlags, which are
// process-wide.
func newLoggerInTempDir(t *testing.T) string {
	t.Helper()

	prevFlags := log.Flags()
	prevOutput := log.Writer()
	t.Cleanup(func() {
		log.SetFlags(prevFlags)
		log.SetOutput(prevOutput)
	})

	dir := t.TempDir()
	_, err := NewLogger(dir, "test", LogLevelCritical) // critical: keep stdout quiet during tests
	require.NoError(t, err)
	return dir
}

func TestNewLogger_FileTimestamps(t *testing.T) {
	dir := newLoggerInTempDir(t)

	log.Println("a message worth dating")

	lines := readLogFile(t, dir)

	t.Run("every line carries a parseable RFC3339 timestamp", func(t *testing.T) {
		// The log file is the sink that rotates and outlives the process — it is what
		// gets read weeks after an incident. Without a timestamp on it, an outage can
		// only be located by line number, never dated or correlated with a deploy.
		for _, line := range lines {
			stamp, _, found := strings.Cut(line, " ")
			require.True(t, found, "line has no timestamp: %q", line)
			_, err := time.Parse(time.RFC3339, stamp)
			require.NoError(t, err, "line does not start with an RFC3339 timestamp: %q", line)
		}
	})

	t.Run("the timestamp carries its UTC offset", func(t *testing.T) {
		// A bare clock time renumbers history silently when a host's timezone changes.
		stamp, _, _ := strings.Cut(lines[0], " ")
		parsed, err := time.Parse(time.RFC3339, stamp)
		require.NoError(t, err)
		_, offset := parsed.Zone()
		_ = offset // any offset is fine; what matters is that one was recorded at all
		assert.Regexp(t, `(Z|[+-]\d{2}:\d{2})$`, stamp, "the offset must be in the line, not implied")
	})

	t.Run("lines are not timestamped twice", func(t *testing.T) {
		// The standard logger's own flags must stay off: with both it and the handler
		// prefixing, every line would carry two dates.
		assert.Zero(t, log.Flags(), "the standard logger must not add its own prefix")

		for _, line := range lines {
			_, rest, _ := strings.Cut(line, " ")
			first, _, _ := strings.Cut(rest, " ")
			_, err := time.Parse(time.RFC3339, first)
			assert.Error(t, err, "a second timestamp follows the first: %q", line)
		}
	})

	t.Run("the message survives intact", func(t *testing.T) {
		joined := strings.Join(lines, "\n")
		assert.Contains(t, joined, "a message worth dating")
	})
}

func TestNewLogger_BuildLineReachesTheFile(t *testing.T) {
	dir := newLoggerInTempDir(t)

	// Every binary logs its build immediately after this point. It used to be logged
	// before the logger existed, so it went to a stderr the cron wrappers discard and no
	// line in the file could be attributed to a release.
	log.Printf("build: %s (%s) at %s\n", "r_0.0.0-test", "deadbeef", "sometime")

	joined := strings.Join(readLogFile(t, dir), "\n")
	assert.Contains(t, joined, "build: r_0.0.0-test")
	assert.Contains(t, joined, "deadbeef")
}

func TestNewLogger_MultiLineMessages(t *testing.T) {
	dir := newLoggerInTempDir(t)

	log.Printf("first line\nsecond line")

	lines := readLogFile(t, dir)
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "first line")
	require.Contains(t, joined, "second line")

	// The continuation is indented rather than timestamped again, so a multi-line payload
	// reads as one block instead of looking like two unrelated events.
	for _, line := range lines {
		if strings.Contains(line, "second line") {
			assert.True(t, strings.HasPrefix(line, " "), "continuation must be indented, got %q", line)
			return
		}
	}
	t.Fatal("the continuation line never reached the file")
}

func TestNewLogger_DefaultsAndDirectoryCreation(t *testing.T) {
	prevFlags := log.Flags()
	prevOutput := log.Writer()
	t.Cleanup(func() {
		log.SetFlags(prevFlags)
		log.SetOutput(prevOutput)
	})

	t.Run("a nested directory is created", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "deeper", "still")
		_, err := NewLogger(dir, "test", LogLevelCritical)
		require.NoError(t, err)
		_, err = os.Stat(dir)
		require.NoError(t, err)
	})

	t.Run("an empty name still produces a file", func(t *testing.T) {
		dir := t.TempDir()
		_, err := NewLogger(dir, "", LogLevelCritical)
		require.NoError(t, err)
		log.Println("named by default")
		matches, err := filepath.Glob(filepath.Join(dir, "*.log"))
		require.NoError(t, err)
		assert.NotEmpty(t, matches)
	})
}

func TestNewLogger_StdoutIsTimestampedOnce(t *testing.T) {
	prevFlags := log.Flags()
	prevOutput := log.Writer()
	t.Cleanup(func() {
		log.SetFlags(prevFlags)
		log.SetOutput(prevOutput)
	})

	// Capture stdout: the printer hook writes there, and it must format the same way the
	// file does rather than inventing a second convention.
	realStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = realStdout })

	_, err = NewLogger(t.TempDir(), "test", LogLevelWarning)
	require.NoError(t, err)
	log.Println("visible on stdout")

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	os.Stdout = realStdout

	out := buf.String()
	require.Contains(t, out, "visible on stdout")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "visible on stdout") {
			continue
		}
		stamp, _, found := strings.Cut(line, " ")
		require.True(t, found)
		_, parseErr := time.Parse(time.RFC3339, stamp)
		assert.NoError(t, parseErr, "stdout must use the same layout as the file: %q", line)
	}
}
