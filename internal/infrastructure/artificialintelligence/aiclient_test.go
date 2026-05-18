package artificialintelligence

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	t.Run("unknown driver falls back to stub and logs a message", func(t *testing.T) {
		t.Parallel()

		var logBuf bytes.Buffer
		ds := &dsninjector.DataSourceMapper{}
		ds.SetDriver("totally-unknown-driver-xyz")

		client, err := NewClient(ds, &logBuf)
		require.NoError(t, err)
		assert.Equal(t, "StubAI", client.Name())

		logOutput := logBuf.String()
		assert.Contains(t, logOutput, "unsupported driver",
			"logger must record that the driver was unsupported")
		assert.Contains(t, logOutput, "totally-unknown-driver-xyz",
			"logger must include the bogus driver name")
	})

	t.Run("stub client passes CheckUP", func(t *testing.T) {
		t.Parallel()

		ds := &dsninjector.DataSourceMapper{}
		ds.SetDriver("totally-unknown-driver-xyz")

		client, err := NewClient(ds, &bytes.Buffer{})
		require.NoError(t, err)

		checkErr := client.CheckUP(context.Background())
		require.NoError(t, checkErr, "stub client returned by unknown-driver fallback must pass CheckUP")
	})
}

func TestParseDSNTimeout(t *testing.T) {
	t.Parallel()

	t.Run("returns default of one minute when option is absent", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		got, err := parseDSNTimeout(ds)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, got)
	})

	t.Run("parses a valid duration in the allowed range", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetOption("timeout", "45s")
		got, err := parseDSNTimeout(ds)
		require.NoError(t, err)
		assert.Equal(t, 45*time.Second, got)
	})

	t.Run("clamps a value below the ten-second minimum", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetOption("timeout", "1s")
		got, err := parseDSNTimeout(ds)
		require.NoError(t, err)
		assert.Equal(t, 10*time.Second, got)
	})

	t.Run("clamps a value above the fifteen-minute maximum", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetOption("timeout", "30m")
		got, err := parseDSNTimeout(ds)
		require.NoError(t, err)
		assert.Equal(t, 15*time.Minute, got)
	})

	t.Run("accepts the boundary value of exactly fifteen minutes", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetOption("timeout", "15m")
		got, err := parseDSNTimeout(ds)
		require.NoError(t, err)
		assert.Equal(t, 15*time.Minute, got)
	})

	t.Run("returns wrapped error when duration string is unparseable", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetOption("timeout", "not-a-duration")
		got, err := parseDSNTimeout(ds)
		require.Error(t, err)
		assert.Equal(t, time.Duration(0), got)
		assert.Contains(t, err.Error(), "unable to parse timeout")
	})
}

func TestParseDSNKey(t *testing.T) {
	t.Parallel()

	t.Run("decodes a URL-safe base64 password into the original key", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetPassword(base64.URLEncoding.EncodeToString([]byte("sk-secret-value")))
		got, err := parseDSNKey(ds)
		require.NoError(t, err)
		assert.Equal(t, "sk-secret-value", got)
	})

	t.Run("returns error when password is missing", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		_, err := parseDSNKey(ds)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing API key")
	})

	t.Run("returns error when value is not valid URL-safe base64", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetPassword("not-valid-base64!!!")
		_, err := parseDSNKey(ds)
		require.Error(t, err)
	})

	t.Run("returns error when decoded value is empty", func(t *testing.T) {
		t.Parallel()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetPassword(base64.URLEncoding.EncodeToString([]byte("")))
		_, err := parseDSNKey(ds)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing API key")
	})
}
