package threadsafe

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

// TestExpiredEntryIsNeverReturned pins the guarantee that lets Fetch and Push do no
// sweeping of their own: go-cache consults an item's expiration on every read and on
// Add, and treats an expired entry as absent.
//
// The entry is seeded already expired rather than written and waited out. Sleeping past a
// TTL would make the test time-dependent for no gain, and the janitor is disabled
// (cleanupInterval 0 starts none) so the item is still physically in the map when the
// assertions run — with a janitor this would pass whether or not the library checked
// expiry at all, and would be evidence for nothing.
func TestExpiredEntryIsNeverReturned(t *testing.T) {
	t.Parallel()

	expiredAnHourAgo := time.Now().Add(-time.Hour).UnixNano()
	c := &Cache{c: cache.NewFrom(time.Minute, 0, map[string]cache.Item{
		"k": {Object: "stale", Expiration: expiredAnHourAgo},
	})}

	_, err := c.Fetch("k")
	require.Error(t, err, "an expired entry must read as absent, not as a stale hit")

	_, err = c.Pull("k")
	require.Error(t, err, "an expired entry must not be pullable")

	require.NoError(t, c.Push("k", "fresh"),
		"an expired entry must not block a Push: Add treats it as absent")

	val, err := c.Fetch("k")
	require.NoError(t, err)
	require.Equal(t, "fresh", val)
}

// TestPullDeliversToExactlyOneCaller pins the reason Pull keeps a mutex when Fetch and
// Push do not. It reads and then deletes, and go-cache has no atomic take, so without
// serialisation two callers can both observe the value before either removes it.
//
// Many short rounds rather than one crowded one: the window between the read and the
// delete is a few instructions, so the way to land inside it is to try often. Verified to
// detect the defect — with the lock removed this fails within a run.
func TestPullDeliversToExactlyOneCaller(t *testing.T) {
	t.Parallel()

	const (
		rounds     = 400
		contenders = 8
	)

	c := NewCache(time.Minute)
	var wins atomic.Int32

	for round := range rounds {
		key := fmt.Sprintf("k%d", round)
		require.NoError(t, c.Push(key, "only-once"))

		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(contenders)
		for range contenders {
			go func() {
				defer wg.Done()
				<-start
				if _, err := c.Pull(key); err == nil {
					wins.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
	}

	require.Equal(t, int32(rounds), wins.Load(),
		"exactly one concurrent Pull per key may receive the value")
}
