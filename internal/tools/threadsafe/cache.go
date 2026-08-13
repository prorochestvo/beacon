package threadsafe

import (
	"fmt"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
)

// NewCache creates a Cache with the given default expiration. The internal
// cleanup interval is set to one quarter of defaultExpiration.
func NewCache(defaultExpiration time.Duration) *Cache {
	return &Cache{c: cache.New(defaultExpiration, defaultExpiration>>2)}
}

// Cache is a goroutine-safe in-memory key-value store with TTL expiration.
//
// Safety comes from go-cache's own RWMutex, so reads of different keys proceed
// concurrently. This type does not add a lock of its own except on Pull, which is
// the one compound operation here — see the comment there.
//
// Expiry is likewise the library's: Get and Add both consult an item's expiration
// and treat an expired entry as absent, so no method sweeps the map to stay correct.
// Reclaiming the memory is the janitor's job, started by NewCache at a quarter of the
// TTL. TestExpiredEntryIsNeverReturned pins that assumption; if this type is ever
// moved off go-cache, that test is what says whether the replacement can carry it.
type Cache struct {
	c *cache.Cache

	// pullMu serialises Pull against other Pulls so at most one caller receives a
	// given value. Nothing weaker suffices: go-cache has no atomic take, so Pull
	// reads and then deletes, and without this two callers can both observe the
	// value before either removes it. It deliberately does not guard Fetch or Push —
	// serialising those buys nothing the library does not already provide, and costs
	// the concurrency it does.
	pullMu sync.Mutex
}

// Fetch returns the value stored under key, or an error if the key is absent or expired.
func (s *Cache) Fetch(key string) (interface{}, error) {
	val, found := s.c.Get(key)
	if !found {
		err := fmt.Errorf("key %s not found in cache", key)
		return nil, err
	}

	return val, nil
}

// Pull returns and removes the value stored under key, or an error if absent.
//
// At most one concurrent caller receives the value; the rest get the absent error.
func (s *Cache) Pull(key string) (interface{}, error) {
	s.pullMu.Lock()
	defer s.pullMu.Unlock()

	val, found := s.c.Get(key)
	if !found {
		err := fmt.Errorf("key %s not found in cache", key)
		return nil, err
	}

	s.c.Delete(key)

	return val, nil
}

// Push stores val under key with the default expiration. Returns an error if the
// key already exists (uses cache.Add semantics — no overwrite).
//
// An expired entry does not block a Push: Add treats it as absent.
func (s *Cache) Push(key string, val interface{}) error {
	return s.c.Add(key, val, cache.DefaultExpiration)
}
