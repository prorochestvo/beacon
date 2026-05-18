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
// All operations are serialised with a mutex.
type Cache struct {
	c *cache.Cache
	m sync.Mutex
}

// Fetch returns the value stored under key, or an error if the key is absent or expired.
func (s *Cache) Fetch(key string) (interface{}, error) {
	s.m.Lock()
	defer s.m.Unlock()

	s.c.DeleteExpired()

	val, found := s.c.Get(key)
	if !found {
		err := fmt.Errorf("key %s not found in cache", key)
		return nil, err
	}

	return val, nil
}

// Pull returns and removes the value stored under key, or an error if absent.
func (s *Cache) Pull(key string) (interface{}, error) {
	s.m.Lock()
	defer s.m.Unlock()

	s.c.DeleteExpired()

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
func (s *Cache) Push(key string, val interface{}) error {
	s.m.Lock()
	defer s.m.Unlock()

	s.c.DeleteExpired()

	return s.c.Add(key, val, cache.DefaultExpiration)
}
