package proxyutil

import (
	"bytes"
	"log"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogOutput swaps the global log writer for a buffer for one test,
// restoring the original on t.Cleanup. Needed because ResolveURL emits via
// log.Printf and exposes no per-call logger seam to mock.
func captureLogOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

func TestRedactURL(t *testing.T) {
	t.Parallel()

	t.Run("strips password from userinfo", func(t *testing.T) {
		t.Parallel()
		result := RedactURL("http://user:secret@127.0.0.1:7788")
		assert.Contains(t, result, "user", "username must be preserved in redacted URL")
		assert.NotContains(t, result, "secret", "password must be stripped from redacted URL")
		assert.Contains(t, result, "127.0.0.1:7788", "host:port must be preserved in redacted URL")
	})

	t.Run("returns unchanged when no userinfo", func(t *testing.T) {
		t.Parallel()
		raw := "http://127.0.0.1:7788"
		assert.Equal(t, raw, RedactURL(raw))
	})

	t.Run("returns unchanged on parse error", func(t *testing.T) {
		t.Parallel()
		raw := "://bad"
		assert.Equal(t, raw, RedactURL(raw))
	})
}

// TestResolveURL omits top-level t.Parallel because subtests use t.Setenv,
// which mutates process environment and cannot run under a parallel parent.
func TestResolveURL(t *testing.T) {
	// Test-specific env names avoid colliding with an operator's own PROXY_URL.

	t.Run("unset env returns empty string and logs not configured", func(t *testing.T) {
		// PROXY_URL_TEST_ABSENT_XYZ is intentionally never set in this process.
		const absent = "PROXY_URL_TEST_ABSENT_XYZ"
		logBuf := captureLogOutput(t)
		result := ResolveURL(absent)
		require.Equal(t, "", result)
		assert.Contains(t, logBuf.String(), "proxy: not configured")
	})

	t.Run("valid URL is returned and logged with credentials redacted", func(t *testing.T) {
		const localEnv = "PROXY_URL_TEST_VALID"
		// dsninjector reconstructs the URL from Driver + "://" + Addr, dropping
		// userinfo, so ResolveURL returns/logs a credential-free host:port and
		// RedactURL is a no-op here (it still guards callers that assemble the URL
		// differently). The invariant: s3cret never appears in the log.
		t.Setenv(localEnv, "http://user:s3cret@127.0.0.1:7788")

		logBuf := captureLogOutput(t)
		result := ResolveURL(localEnv)
		require.NotEmpty(t, result, "valid proxy URL must be returned")
		assert.Contains(t, result, "127.0.0.1:7788", "returned URL must include host:port")

		logLine := logBuf.String()
		assert.Contains(t, logLine, "PROXY_URL=", "log line must include the PROXY_URL= prefix")
		assert.Contains(t, logLine, "127.0.0.1:7788", "log line must include host:port")
		assert.NotContains(t, logLine, "s3cret", "log line must not contain the password")
	})
}
