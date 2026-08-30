package rulegen

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockManager_TryAcquire(t *testing.T) {
	t.Parallel()

	t.Run("first acquire succeeds", func(t *testing.T) {
		t.Parallel()
		m := NewLockManager()
		release, ok := m.TryAcquire("source-x")
		require.True(t, ok)
		require.NotNil(t, release)
		release()
	})

	t.Run("second acquire for same key fails while first holds lock", func(t *testing.T) {
		t.Parallel()
		m := NewLockManager()
		release, ok := m.TryAcquire("source-x")
		require.True(t, ok)

		_, ok2 := m.TryAcquire("source-x")
		assert.False(t, ok2, "second TryAcquire must fail while first holds the lock")

		release()
	})

	t.Run("acquire succeeds again after release", func(t *testing.T) {
		t.Parallel()
		m := NewLockManager()
		release, ok := m.TryAcquire("source-x")
		require.True(t, ok)
		release()

		release2, ok2 := m.TryAcquire("source-x")
		require.True(t, ok2, "TryAcquire must succeed after previous release")
		require.NotNil(t, release2)
		release2()
	})

	t.Run("different source names are independent", func(t *testing.T) {
		t.Parallel()
		m := NewLockManager()
		releaseX, okX := m.TryAcquire("source-x")
		releaseY, okY := m.TryAcquire("source-y")

		assert.True(t, okX, "source-x must be acquirable")
		assert.True(t, okY, "source-y must be acquirable concurrently")

		releaseX()
		releaseY()
	})

	t.Run("concurrent goroutines on same key: exactly one succeeds", func(t *testing.T) {
		t.Parallel()
		m := NewLockManager()

		const numGoroutines = 20
		results := make([]bool, numGoroutines)
		releases := make([]func(), numGoroutines)
		var wg sync.WaitGroup

		wg.Add(numGoroutines)
		for i := range numGoroutines {
			go func(i int) {
				defer wg.Done()
				rel, ok := m.TryAcquire("hot-source")
				results[i] = ok
				releases[i] = rel
			}(i)
		}
		wg.Wait()

		successCount := 0
		for i, ok := range results {
			if ok {
				successCount++
				releases[i]()
			}
		}
		assert.Equal(t, 1, successCount, "exactly one goroutine must hold the lock at any given time")
	})
}
