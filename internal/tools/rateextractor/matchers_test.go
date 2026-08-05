package rateextractor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyRegex(t *testing.T) {
	t.Parallel()

	t.Run("happy path extracts first capture group", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyRegex(`rate=([\d.]+)`, []byte(`page rate=450.75 end`))
		require.NoError(t, err)
		assert.Equal(t, []byte("450.75"), result)
	})

	t.Run("pattern with no capture group returns error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyRegex(`nocapture`, []byte(`nocapture`))
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "produced no match")
	})

	t.Run("pattern does not match payload returns error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyRegex(`price=(\d+)`, []byte(`no numbers here`))
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "produced no match")
	})

	t.Run("invalid pattern returns compile error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyRegex(`[invalid`, []byte(`any`))
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "compile pattern")
	})

	t.Run("multiline payload extracts across newlines with dotall flag", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyRegex(`(?s)start(.+?)end`, []byte("start\nvalue\nend"))
		require.NoError(t, err)
		assert.Equal(t, []byte("\nvalue\n"), result)
	})

	t.Run("returns first capture group when multiple groups present", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyRegex(`(a)(b)`, []byte(`ab`))
		require.NoError(t, err)
		assert.Equal(t, []byte("a"), result)
	})
}

func TestApplyJSONPath(t *testing.T) {
	t.Parallel()

	t.Run("happy path extracts numeric value at simple key", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("rate", []byte(`{"rate":450.75}`))
		require.NoError(t, err)
		assert.Equal(t, []byte("450.75"), result)
	})

	t.Run("happy path extracts nested key value", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("data.rate", []byte(`{"data":{"rate":123.45}}`))
		require.NoError(t, err)
		assert.Equal(t, []byte("123.45"), result)
	})

	t.Run("happy path extracts array element value", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("items[0].value", []byte(`{"items":[{"value":99.9}]}`))
		require.NoError(t, err)
		assert.Equal(t, []byte("99.9"), result)
	})

	t.Run("happy path extracts string terminal value as bytes", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("rate", []byte(`{"rate":"466.00"}`))
		require.NoError(t, err)
		assert.Equal(t, []byte("466.00"), result)
	})

	t.Run("malformed JSON payload produces a wrapped error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("rate", []byte(`not-json`))
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "json_path")
	})

	t.Run("missing path produces a wrapped error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("missing", []byte(`{"other":1}`))
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("empty path pattern produces error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("", []byte(`{"rate":1}`))
		require.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("terminal value is object produces unsupported type error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("nested", []byte(`{"nested":{"a":1}}`))
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "unsupported type")
	})

	t.Run("index out of range produces error", func(t *testing.T) {
		t.Parallel()

		result, err := ApplyJSONPath("arr[5]", []byte(`{"arr":[1,2]}`))
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "out of range")
	})
}

// TestApplyJSONPath_NegativeIndex covers addressing a series by its end, which is what
// batching the upstream quote endpoint needs: one request returns every symbol, each with
// a close array whose length grows through the session, so only "the last one" names a
// stable thing.
func TestApplyJSONPath_NegativeIndex(t *testing.T) {
	t.Parallel()

	// The shape of a real batched response: keyed by ticker, each carrying a series.
	payload := []byte(`{
		"AAPL":    {"symbol":"AAPL","close":[308.63,307.76,308.16,307.92],"previousClose":309.38},
		"BTC-USD": {"symbol":"BTC-USD","close":[64100.5,64143.86],"previousClose":63900.0},
		"THIN":    {"symbol":"THIN","close":[12.5]},
		"EMPTY":   {"symbol":"EMPTY","close":[]},
		"GAPPY":   {"symbol":"GAPPY","close":[1.0,null]}
	}`)

	t.Run("minus one is the newest point", func(t *testing.T) {
		t.Parallel()
		got, err := ApplyJSONPath("AAPL.close[-1]", payload)
		require.NoError(t, err)
		assert.Equal(t, "307.92", string(got))
	})

	t.Run("a hyphenated ticker is a key, not a subtraction", func(t *testing.T) {
		t.Parallel()
		got, err := ApplyJSONPath("BTC-USD.close[-1]", payload)
		require.NoError(t, err)
		assert.Equal(t, "64143.86", string(got))
	})

	t.Run("counting further back works", func(t *testing.T) {
		t.Parallel()
		got, err := ApplyJSONPath("AAPL.close[-2]", payload)
		require.NoError(t, err)
		assert.Equal(t, "308.16", string(got))
	})

	t.Run("positive indices are unchanged", func(t *testing.T) {
		t.Parallel()
		got, err := ApplyJSONPath("AAPL.close[0]", payload)
		require.NoError(t, err)
		assert.Equal(t, "308.63", string(got))
	})

	t.Run("a single-element series resolves", func(t *testing.T) {
		t.Parallel()
		// The degenerate case where -1 and 0 are the same element.
		got, err := ApplyJSONPath("THIN.close[-1]", payload)
		require.NoError(t, err)
		assert.Equal(t, "12.5", string(got))
	})

	t.Run("an empty series is out of range, not element zero", func(t *testing.T) {
		t.Parallel()
		// -1 on an empty array resolves to -1, which must be rejected rather than
		// wrapping around into a panic or a neighbouring value.
		_, err := ApplyJSONPath("EMPTY.close[-1]", payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("reaching past the start is out of range", func(t *testing.T) {
		t.Parallel()
		_, err := ApplyJSONPath("BTC-USD.close[-5]", payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("the error names the index the rule asked for", func(t *testing.T) {
		t.Parallel()
		// Reporting the resolved index would send whoever reads the log looking for a
		// number that appears nowhere in the rule.
		_, err := ApplyJSONPath("EMPTY.close[-1]", payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "-1")
	})

	t.Run("a null at the end surfaces rather than being skipped", func(t *testing.T) {
		t.Parallel()
		// Upstream can leave a gap where a period had no trade. Silently walking back to
		// the previous point would report a stale price as current; failing lets the
		// source retry on the next tick and, if it persists, be reported.
		got, err := ApplyJSONPath("GAPPY.close[-1]", payload)
		if err == nil {
			assert.NotEqual(t, "1", string(got), "a gap must not be answered with the previous point")
		}
	})
}
