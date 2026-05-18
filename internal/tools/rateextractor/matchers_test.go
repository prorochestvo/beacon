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
