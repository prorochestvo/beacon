package threadsafe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCache_Push(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Minute)
	require.NoError(t, c.Push("k", "v"))
	require.Error(t, c.Push("k", "v")) // duplicate key → Add fails
}

func TestCache_Fetch(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Minute)
	require.NoError(t, c.Push("k", "hello"))

	val, err := c.Fetch("k")
	require.NoError(t, err)
	require.Equal(t, "hello", val)

	val, err = c.Fetch("k")
	require.NoError(t, err)
	require.Equal(t, "hello", val)

	_, err = c.Fetch("missing")
	require.Error(t, err)
}

func TestCache_Pull(t *testing.T) {
	t.Parallel()

	c := NewCache(time.Minute)
	require.NoError(t, c.Push("k", 42))

	val, err := c.Pull("k")
	require.NoError(t, err)
	require.Equal(t, 42, val)

	_, err = c.Pull("k") // already deleted
	require.Error(t, err)
}
