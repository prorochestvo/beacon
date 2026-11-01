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
	t.Run("a negative index parses and is carried through", func(t *testing.T) {
		t.Parallel()

		// This used to be an error. It is now the only way to address the newest point
		// of a series whose length changes between requests, which is what Yahoo's
		// batched quote response returns. Resolution against the real length happens in
		// ApplyJSONPath, where the length is known.
		segs, err := parseJSONPath("foo[-1]")
		require.NoError(t, err)
		require.Len(t, segs, 1)
		require.True(t, segs[0].HasIndex)
		require.Equal(t, -1, segs[0].Index)
		require.Equal(t, "foo", segs[0].Key)
	})

	t.Run("a hyphenated key parses", func(t *testing.T) {
		t.Parallel()

		// Upstream keys its batched response by ticker, so "BTC-USD" is an object key.
		segs, err := parseJSONPath("BTC-USD.close[-1]")
		require.NoError(t, err)
		require.Len(t, segs, 2)
		require.Equal(t, "BTC-USD", segs[0].Key)
		require.False(t, segs[0].HasIndex)
		require.Equal(t, "close", segs[1].Key)
		require.Equal(t, -1, segs[1].Index)
	})

	t.Run("nonsense inside the brackets is still rejected", func(t *testing.T) {
		t.Parallel()

		// Widening the charset must not turn a typo into a silently missing key.
		for _, bad := range []string{"foo[--1]", "foo[1-]", "foo[]", "foo[1.5]", "foo[ 1]"} {
			segs, err := parseJSONPath(bad)
			require.Error(t, err, "pattern %q must not parse", bad)
			require.Nil(t, segs)
		}
	})
}
