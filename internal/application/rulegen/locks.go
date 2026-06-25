package rulegen

import "sync"

// NewLockManager constructs a ready-to-use LockManager.
func NewLockManager() *LockManager {
	return &LockManager{locks: make(map[string]*sync.Mutex)}
}

// LockManager serialises rule generation per source name. The manager retains
// each per-source mutex after release, trading one mutex per ever-seen source
// name for a branch-free fast path.
type LockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// TryAcquire attempts to acquire the per-source lock for sourceName.
// Returns (release, true) on success; (nil, false) when the lock is
// already held. The returned release function must be called exactly once.
func (m *LockManager) TryAcquire(sourceName string) (func(), bool) {
	m.mu.Lock()
	lk, ok := m.locks[sourceName]
	if !ok {
		lk = &sync.Mutex{}
		m.locks[sourceName] = lk
	}
	m.mu.Unlock()

	if !lk.TryLock() {
		return nil, false
	}
	return lk.Unlock, true
}
