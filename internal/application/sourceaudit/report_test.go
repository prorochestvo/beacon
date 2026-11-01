package sourceaudit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReport(t *testing.T) {
	t.Parallel()

	t.Run("quiet all OK prints single OK line and zero failures", func(t *testing.T) {
		t.Parallel()

		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC_A", Side: "BID", Active: true}, URL: "https://a.com/", Status: StatusOK, Value: "468.00"},
			{Source: SeededSource{Name: "SRC_B", Side: "ASK", Active: true}, URL: "https://b.com/", Status: StatusOK, Value: "470.35"},
		}

		var buf strings.Builder
		failures, err := WriteReport(&buf, results, false)
		require.NoError(t, err)
		assert.Equal(t, 0, failures)
		out := buf.String()
		assert.Contains(t, out, "OK: audited 2 sources across 2 URLs")
		assert.NotContains(t, out, "SRC_A")
		assert.NotContains(t, out, "MISS DETAILS")
	})

	t.Run("quiet with failures prints FAIL summary plus MISS DETAILS", func(t *testing.T) {
		t.Parallel()

		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC_OK", Side: "BID", Active: true}, URL: "https://ok.com/", Status: StatusOK, Value: "468.00"},
			{Source: SeededSource{Name: "SRC_BAD", Side: "ASK", Active: true}, URL: "https://bad.com/", Status: StatusRegexNoMatch, Detail: "no match found"},
			{Source: SeededSource{Name: "SRC_ERR", Side: "BID", Active: true}, URL: "https://err.com/", Status: StatusFetchError, Detail: "connection refused"},
		}

		var buf strings.Builder
		failures, err := WriteReport(&buf, results, false)
		require.NoError(t, err)
		assert.Equal(t, 2, failures)
		out := buf.String()
		assert.Contains(t, out, "FAIL: 2/3 sources MISS across 3 URLs")
		assert.Contains(t, out, "MISS DETAILS")
		assert.Contains(t, out, "SRC_BAD")
		assert.Contains(t, out, "SRC_ERR")
		assert.Contains(t, out, "REGEX_NO_MATCH")
		assert.Contains(t, out, "FETCH_ERROR")
		assert.NotContains(t, out, "SRC_OK\thttps")
	})

	t.Run("verbose all OK includes table summary and source names", func(t *testing.T) {
		t.Parallel()

		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC_A", Side: "BID", Active: true}, URL: "https://a.com/", Status: StatusOK, Value: "468.00"},
			{Source: SeededSource{Name: "SRC_B", Side: "ASK", Active: true}, URL: "https://b.com/", Status: StatusOK, Value: "470.35"},
		}

		var buf strings.Builder
		failures, err := WriteReport(&buf, results, true)
		require.NoError(t, err)
		assert.Equal(t, 0, failures)
		out := buf.String()
		assert.Contains(t, out, "SRC_A")
		assert.Contains(t, out, "SRC_B")
		assert.Contains(t, out, "audited 2 sources across 2 URLs: 2 OK, 0 MISS")
		assert.NotContains(t, out, "MISS DETAILS")
	})

	t.Run("verbose mixed input prints table summary and MISS DETAILS", func(t *testing.T) {
		t.Parallel()

		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC_OK", Side: "BID", Active: true}, URL: "https://ok.com/", Status: StatusOK, Value: "468.00"},
			{Source: SeededSource{Name: "SRC_BAD", Side: "ASK", Active: true}, URL: "https://bad.com/", Status: StatusRegexNoMatch, Detail: "no match found"},
		}

		var buf strings.Builder
		failures, err := WriteReport(&buf, results, true)
		require.NoError(t, err)
		assert.Equal(t, 1, failures)
		out := buf.String()
		assert.Contains(t, out, "SRC_OK")
		assert.Contains(t, out, "SRC_BAD")
		assert.Contains(t, out, "audited 2 sources across 2 URLs: 1 OK, 1 MISS")
		assert.Contains(t, out, "MISS DETAILS")
	})

	t.Run("quiet empty input prints OK zero", func(t *testing.T) {
		t.Parallel()

		var buf strings.Builder
		failures, err := WriteReport(&buf, nil, false)
		require.NoError(t, err)
		assert.Equal(t, 0, failures)
		assert.Contains(t, buf.String(), "OK: audited 0 sources across 0 URLs")
	})

	t.Run("verbose URL truncated at 72 chars with ellipsis", func(t *testing.T) {
		t.Parallel()

		longURL := "https://example.com/" + strings.Repeat("x", 60)
		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC", Side: "BID", Active: true}, URL: longURL, Status: StatusOK, Value: "100.0"},
		}

		var buf strings.Builder
		_, err := WriteReport(&buf, results, true)
		require.NoError(t, err)
		out := buf.String()
		assert.Contains(t, out, "...")
		assert.NotContains(t, out, longURL)
	})

	t.Run("verbose inactive source gets [inactive] suffix", func(t *testing.T) {
		t.Parallel()

		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC_OFF", Side: "BID", Active: false}, URL: "https://x.com/", Status: StatusOK, Value: "100.0"},
		}

		var buf strings.Builder
		_, err := WriteReport(&buf, results, true)
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "SRC_OFF [inactive]")
	})

	t.Run("quiet detail with newlines in MISS DETAILS replaces with pipe", func(t *testing.T) {
		t.Parallel()

		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC_NL", Side: "BID", Active: true}, URL: "https://x.com/", Status: StatusFetchError, Detail: "line1\nline2"},
		}

		var buf strings.Builder
		_, err := WriteReport(&buf, results, false)
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "line1 | line2")
	})

	t.Run("verbose output stable across runs and preserves input order", func(t *testing.T) {
		t.Parallel()

		results := []ProbeResult{
			{Source: SeededSource{Name: "SRC_1", Side: "BID", Active: true}, URL: "https://x.com/", Status: StatusOK, Value: "100.0"},
			{Source: SeededSource{Name: "SRC_2", Side: "ASK", Active: true}, URL: "https://y.com/", Status: StatusOK, Value: "200.0"},
		}

		var buf1, buf2 strings.Builder
		_, err := WriteReport(&buf1, results, true)
		require.NoError(t, err)
		_, err = WriteReport(&buf2, results, true)
		require.NoError(t, err)
		assert.Equal(t, buf1.String(), buf2.String())

		out := buf1.String()
		pos1 := strings.Index(out, "SRC_1")
		pos2 := strings.Index(out, "SRC_2")
		assert.Less(t, pos1, pos2, "SRC_1 should appear before SRC_2")
	})
}
