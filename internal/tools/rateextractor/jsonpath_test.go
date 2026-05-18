package rateextractor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseJSONPath(t *testing.T) {
	t.Parallel()

	t.Run("simple key", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("rate")
		require.NoError(t, err)
		require.Equal(t, []pathSegment{{Key: "rate", HasIndex: false, Index: 0}}, segs)
	})
	t.Run("nested keys", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("usd.rate_value")
		require.NoError(t, err)
		require.Equal(t, []pathSegment{
			{Key: "usd", HasIndex: false, Index: 0},
			{Key: "rate_value", HasIndex: false, Index: 0},
		}, segs)
	})
	t.Run("array index", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("records[0].value")
		require.NoError(t, err)
		require.Equal(t, []pathSegment{
			{Key: "records", HasIndex: true, Index: 0},
			{Key: "value", HasIndex: false, Index: 0},
		}, segs)
	})
	t.Run("deep path with multiple indexes", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("a[1].b[2].c")
		require.NoError(t, err)
		require.Len(t, segs, 3)
		require.Equal(t, pathSegment{Key: "a", HasIndex: true, Index: 1}, segs[0])
		require.Equal(t, pathSegment{Key: "b", HasIndex: true, Index: 2}, segs[1])
		require.Equal(t, pathSegment{Key: "c", HasIndex: false, Index: 0}, segs[2])
	})
	t.Run("empty pattern", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("")
		require.Error(t, err)
		require.Nil(t, segs)
	})
	t.Run("empty segment", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("foo..bar")
		require.Error(t, err)
		require.Nil(t, segs)
	})
	t.Run("non-integer index", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("foo[abc]")
		require.Error(t, err)
		require.Nil(t, segs)
	})
	t.Run("negative-like index", func(t *testing.T) {
		t.Parallel()

		segs, err := parseJSONPath("foo[-1]")
		require.Error(t, err)
		require.Nil(t, segs)
	})
}
