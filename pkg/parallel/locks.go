package parallel

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileLockManager provides distributed file locking for parallel agents.
// It prevents multiple agents from modifying the same file simultaneously.
type FileLockManager struct {
	mu          sync.RWMutex
	locks       map[string]*FileLock
	waiters     map[string][]chan struct{}
	maxWaitTime time.Duration
	defaultTTL  time.Duration
	onConflict  func(lock *FileLock, waiter string) // Called when lock contention occurs
}

// FileLock represents an active lock on a file.
type FileLock struct {
	Path       string
	AgentID    string
	TaskID     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
	Heartbeat  time.Time
}

// LockResult represents the outcome of a lock attempt.
type LockResult struct {
	Acquired   bool
	Lock       *FileLock
	WaitedFor  time.Duration
	HeldBy     string // If not acquired, who holds the lock
	QueueDepth int    // How many other agents are waiting
}

// FileLockConfig configures the lock manager.
type FileLockConfig struct {
	DefaultTTL      time.Duration // Default lock expiration
	MaxWaitTime     time.Duration // Maximum time to wait for a lock
	HeartbeatPeriod time.Duration // How often to renew locks
	CleanupPeriod   time.Duration // How often to clean expired locks
}

// DefaultFileLockConfig returns sensible defaults.
func DefaultFileLockConfig() FileLockConfig {
	return FileLockConfig{
		DefaultTTL:      5 * time.Minute,
		MaxWaitTime:     30 * time.Second,
		HeartbeatPeriod: 30 * time.Second,
		CleanupPeriod:   1 * time.Minute,
	}
}

// NewFileLockManager creates a new file lock manager.
func NewFileLockManager(cfg FileLockConfig) *FileLockManager {
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = 5 * time.Minute
	}
	if cfg.MaxWaitTime <= 0 {
		cfg.MaxWaitTime = 30 * time.Second
	}
	if cfg.CleanupPeriod <= 0 {
		cfg.CleanupPeriod = 1 * time.Minute
	}

	m := &FileLockManager{
		locks:       make(map[string]*FileLock),
		waiters:     make(map[string][]chan struct{}),
		maxWaitTime: cfg.MaxWaitTime,
		defaultTTL:  cfg.DefaultTTL,
	}

	// Start background cleanup
	if cfg.CleanupPeriod > 0 {
		go m.cleanupLoop(cfg.CleanupPeriod)
	}

	return m
}

// Acquire attempts to acquire a lock on a file.
// If the file is locked by another agent, it waits up to maxWaitTime.
func (m *FileLockManager) Acquire(ctx context.Context, agentID, taskID, path string, ttl time.Duration) (*LockResult, error) {
	path = normalizeLockPath(path)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if agentID == "" {
		return nil, fmt.Errorf("agentID is required")
	}
	if ttl <= 0 {
		ttl = m.defaultTTL
	}

	start := time.Now()
	result := &LockResult{}

	// Fast path: try to acquire immediately
	if lock := m.tryAcquire(agentID, taskID, path, ttl); lock != nil {
		result.Acquired = true
		result.Lock = lock
		return result, nil
	}

	// Slow path: wait for lock
	waiter := make(chan struct{}, 1)
	m.addWaiter(path, waiter)
	defer m.removeWaiter(path, waiter)

	// Get current holder for reporting
	m.mu.RLock()
	if current := m.locks[path]; current != nil {
		result.HeldBy = current.AgentID
	}
	result.QueueDepth = len(m.waiters[path])
	m.mu.RUnlock()

	// Notify conflict callback
	if m.onConflict != nil {
		m.mu.RLock()
		lock := m.locks[path]
		m.mu.RUnlock()
		if lock != nil {
			m.onConflict(lock, agentID)
		}
	}

	// Wait with timeout
	timeout := m.maxWaitTime
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			result.WaitedFor = time.Since(start)
			return result, ctx.Err()

		case <-timer.C:
			result.WaitedFor = time.Since(start)
			return result, nil // Timeout, not acquired

		case <-waiter:
			// Lock may be available, try again
			if lock := m.tryAcquire(agentID, taskID, path, ttl); lock != nil {
				result.Acquired = true
				result.Lock = lock
				result.WaitedFor = time.Since(start)
				return result, nil
			}
			// Someone else got it, keep waiting
		}
	}
}

// tryAcquire attempts to acquire the lock without waiting.
func (m *FileLockManager) tryAcquire(agentID, taskID, path string, ttl time.Duration) *FileLock {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Check if already locked
	if existing := m.locks[path]; existing != nil {
		// Check if lock expired
		if existing.ExpiresAt.After(now) {
			// Still valid
			if existing.AgentID == agentID {
				// Same agent, extend the lock
				existing.ExpiresAt = now.Add(ttl)
				existing.Heartbeat = now
				return existing
			}
			return nil // Held by another agent
		}
		// Expired, can take over
	}

	// Acquire the lock
	lock := &FileLock{
		Path:       path,
		AgentID:    agentID,
		TaskID:     taskID,
		AcquiredAt: now,
		ExpiresAt:  now.Add(ttl),
		Heartbeat:  now,
	}
	m.locks[path] = lock
	return lock
}

// Release releases a lock.
func (m *FileLockManager) Release(agentID, path string) error {
	path = normalizeLockPath(path)
	m.mu.Lock()
	defer m.mu.Unlock()

	lock := m.locks[path]
	if lock == nil {
		return nil // Already released
	}

	if lock.AgentID != agentID {
		return fmt.Errorf("lock held by %s, not %s", lock.AgentID, agentID)
	}

	delete(m.locks, path)

	// Notify waiters
	m.notifyWaiters(path)

	return nil
}

// ReleaseAll releases all locks held by an agent.
func (m *FileLockManager) ReleaseAll(agentID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	released := 0
	pathsToNotify := make([]string, 0)

	for path, lock := range m.locks {
		if lock.AgentID == agentID {
			delete(m.locks, path)
			pathsToNotify = append(pathsToNotify, path)
			released++
		}
	}

	for _, path := range pathsToNotify {
		m.notifyWaiters(path)
	}

	return released
}

// Heartbeat renews a lock's TTL.
func (m *FileLockManager) Heartbeat(agentID, path string, ttl time.Duration) error {
	path = normalizeLockPath(path)
	if ttl <= 0 {
		ttl = m.defaultTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	lock := m.locks[path]
	if lock == nil {
		return fmt.Errorf("no lock on %s", path)
	}

	if lock.AgentID != agentID {
		return fmt.Errorf("lock held by %s, not %s", lock.AgentID, agentID)
	}

	now := time.Now()
	lock.ExpiresAt = now.Add(ttl)
	lock.Heartbeat = now
	return nil
}

// IsLocked checks if a file is locked.
func (m *FileLockManager) IsLocked(path string) bool {
	path = normalizeLockPath(path)
	m.mu.RLock()
	defer m.mu.RUnlock()

	lock := m.locks[path]
	if lock == nil {
		return false
	}
	return lock.ExpiresAt.After(time.Now())
}

// GetLock returns the current lock on a file, if any.
func (m *FileLockManager) GetLock(path string) *FileLock {
	path = normalizeLockPath(path)
	m.mu.RLock()
	defer m.mu.RUnlock()

	lock := m.locks[path]
	if lock == nil || lock.ExpiresAt.Before(time.Now()) {
		return nil
	}
	return lock
}

// ListLocks returns all active locks.
func (m *FileLockManager) ListLocks() []*FileLock {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	locks := make([]*FileLock, 0, len(m.locks))
	for _, lock := range m.locks {
		if lock.ExpiresAt.After(now) {
			locks = append(locks, lock)
		}
	}
	return locks
}

// ListLocksForAgent returns all locks held by an agent.
func (m *FileLockManager) ListLocksForAgent(agentID string) []*FileLock {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	locks := make([]*FileLock, 0)
	for _, lock := range m.locks {
		if lock.AgentID == agentID && lock.ExpiresAt.After(now) {
			locks = append(locks, lock)
		}
	}
	return locks
}

// addWaiter adds a waiter channel for a path.
func (m *FileLockManager) addWaiter(path string, ch chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waiters[path] = append(m.waiters[path], ch)
}

// removeWaiter removes a waiter channel for a path.
func (m *FileLockManager) removeWaiter(path string, ch chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	waiters := m.waiters[path]
	for i, w := range waiters {
		if w == ch {
			m.waiters[path] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
}

// notifyWaiters notifies all waiters that a lock is released.
// Must be called with mu held.
func (m *FileLockManager) notifyWaiters(path string) {
	for _, ch := range m.waiters[path] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// cleanupLoop periodically cleans up expired locks.
func (m *FileLockManager) cleanupLoop(period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for range ticker.C {
		m.cleanup()
	}
}

// cleanup removes expired locks and notifies waiters.
func (m *FileLockManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	expiredPaths := make([]string, 0)

	for path, lock := range m.locks {
		if lock.ExpiresAt.Before(now) {
			expiredPaths = append(expiredPaths, path)
		}
	}

	for _, path := range expiredPaths {
		delete(m.locks, path)
		m.notifyWaiters(path)
	}
}

// SetConflictCallback sets a callback for lock contention events.
func (m *FileLockManager) SetConflictCallback(fn func(lock *FileLock, waiter string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onConflict = fn
}

// Stats returns lock manager statistics.
type LockStats struct {
	ActiveLocks  int
	TotalWaiters int
	OldestLock   time.Duration
}

// Stats returns current statistics.
func (m *FileLockManager) Stats() LockStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	stats := LockStats{}

	var oldest time.Time
	for _, lock := range m.locks {
		if lock.ExpiresAt.After(now) {
			stats.ActiveLocks++
			if oldest.IsZero() || lock.AcquiredAt.Before(oldest) {
				oldest = lock.AcquiredAt
			}
		}
	}

	for _, waiters := range m.waiters {
		stats.TotalWaiters += len(waiters)
	}

	if !oldest.IsZero() {
		stats.OldestLock = now.Sub(oldest)
	}

	return stats
}

// AcquireMultiple acquires locks on multiple files atomically.
// Either all locks are acquired or none are.
func (m *FileLockManager) AcquireMultiple(ctx context.Context, agentID, taskID string, paths []string, ttl time.Duration) (*LockResult, error) {
	paths = normalizeLockPaths(paths)
	if len(paths) == 0 {
		return &LockResult{Acquired: true}, nil
	}
	if ttl <= 0 {
		ttl = m.defaultTTL
	}

	// Try to acquire all locks
	acquired := make([]string, 0, len(paths))

	for _, path := range paths {
		result, err := m.Acquire(ctx, agentID, taskID, path, ttl)
		if err != nil {
			// Release all acquired locks
			for _, p := range acquired {
				_ = m.Release(agentID, p)
			}
			return nil, err
		}

		if !result.Acquired {
			// Release all acquired locks
			for _, p := range acquired {
				_ = m.Release(agentID, p)
			}
			return result, nil // Return the failed result
		}

		acquired = append(acquired, path)
	}

	return &LockResult{
		Acquired: true,
		Lock: &FileLock{
			AgentID:    agentID,
			TaskID:     taskID,
			AcquiredAt: time.Now(),
		},
	}, nil
}

// ReleaseMultiple releases locks on multiple files.
func (m *FileLockManager) ReleaseMultiple(agentID string, paths []string) {
	for _, path := range normalizeLockPaths(paths) {
		_ = m.Release(agentID, path)
	}
}

func normalizeLockPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func normalizeLockPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = normalizeLockPath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
