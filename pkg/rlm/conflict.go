package rlm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ConflictDetector tracks read/write access for resources (typically files).
type ConflictDetector struct {
	mu      sync.Mutex
	cond    *sync.Cond
	locks   map[string]*resourceLock
	timeout time.Duration
}

type resourceLock struct {
	readers     map[string]int
	writer      string
	writerCount int
}

// ConflictDetectorOption configures a ConflictDetector.
type ConflictDetectorOption func(*ConflictDetector)

// WithLockTimeout sets the default timeout for lock acquisition.
func WithLockTimeout(timeout time.Duration) ConflictDetectorOption {
	return func(c *ConflictDetector) {
		c.timeout = timeout
	}
}

// NewConflictDetector returns an empty detector.
func NewConflictDetector(opts ...ConflictDetectorOption) *ConflictDetector {
	c := &ConflictDetector{
		locks:   make(map[string]*resourceLock),
		timeout: 30 * time.Second, // Default timeout
	}
	c.cond = sync.NewCond(&c.mu)
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// AcquireRead acquires a read lock for a task on a path.
func (c *ConflictDetector) AcquireRead(taskID, path string) error {
	return c.AcquireReadWithTimeout(taskID, path, c.timeout)
}

// AcquireReadWithTimeout acquires a read lock with a specific timeout.
func (c *ConflictDetector) AcquireReadWithTimeout(taskID, path string, timeout time.Duration) error {
	taskID, path = normalizeLockInputs(taskID, path)
	if taskID == "" || path == "" {
		return fmt.Errorf("invalid read lock: taskID and path required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		lock := c.lockFor(path)
		// Can acquire read if no writer or we are the writer
		if lock.writer == "" || lock.writer == taskID {
			lock.readers[taskID]++
			return nil
		}

		// Wait for condition or timeout
		if err := c.waitWithContext(ctx); err != nil {
			return &LockConflictError{
				Path:    path,
				Holder:  lock.writer,
				Mode:    "write",
				Timeout: true,
			}
		}
	}
}

// AcquireWrite acquires a write lock for a task on a path.
func (c *ConflictDetector) AcquireWrite(taskID, path string) error {
	return c.AcquireWriteWithTimeout(taskID, path, c.timeout)
}

// AcquireWriteWithTimeout acquires a write lock with a specific timeout.
func (c *ConflictDetector) AcquireWriteWithTimeout(taskID, path string, timeout time.Duration) error {
	taskID, path = normalizeLockInputs(taskID, path)
	if taskID == "" || path == "" {
		return fmt.Errorf("invalid write lock: taskID and path required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		lock := c.lockFor(path)

		// Check if we can acquire the write lock
		canAcquire := true
		var conflictHolder string
		var conflictMode string

		// Another writer holds the lock
		if lock.writer != "" && lock.writer != taskID {
			canAcquire = false
			conflictHolder = lock.writer
			conflictMode = "write"
		}

		// Check for other readers
		if canAcquire {
			for reader := range lock.readers {
				if reader != taskID {
					canAcquire = false
					conflictHolder = reader
					conflictMode = "read"
					break
				}
			}
		}

		if canAcquire {
			// Remove ourselves from readers if present
			delete(lock.readers, taskID)
			lock.writer = taskID
			lock.writerCount++
			return nil
		}

		// Wait for condition or timeout
		if err := c.waitWithContext(ctx); err != nil {
			return &LockConflictError{
				Path:    path,
				Holder:  conflictHolder,
				Mode:    conflictMode,
				Timeout: true,
			}
		}
	}
}

// waitWithContext waits on the condition variable with context cancellation support.
// Must be called with mu held. Returns error if context is done.
func (c *ConflictDetector) waitWithContext(ctx context.Context) error {
	// Use a channel to detect when Wait returns
	done := make(chan struct{})
	go func() {
		c.cond.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Broadcast to unblock the waiting goroutine
		c.cond.Broadcast()
		<-done // Wait for it to finish
		return ctx.Err()
	}
}

// ReleaseRead releases a read lock for a task on a path.
func (c *ConflictDetector) ReleaseRead(taskID, path string) {
	taskID, path = normalizeLockInputs(taskID, path)
	if taskID == "" || path == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	lock := c.locks[path]
	if lock == nil {
		return
	}
	if count, ok := lock.readers[taskID]; ok {
		if count <= 1 {
			delete(lock.readers, taskID)
		} else {
			lock.readers[taskID] = count - 1
		}
	}
	c.cleanupLock(path, lock)
	c.cond.Broadcast() // Wake up waiters
}

// ReleaseWrite releases a write lock for a task on a path.
func (c *ConflictDetector) ReleaseWrite(taskID, path string) {
	taskID, path = normalizeLockInputs(taskID, path)
	if taskID == "" || path == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	lock := c.locks[path]
	if lock == nil || lock.writer != taskID {
		return
	}
	if lock.writerCount > 1 {
		lock.writerCount--
	} else {
		lock.writer = ""
		lock.writerCount = 0
	}
	c.cleanupLock(path, lock)
	c.cond.Broadcast() // Wake up waiters
}

// ReleaseAll clears any locks held by a task.
func (c *ConflictDetector) ReleaseAll(taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for path, lock := range c.locks {
		delete(lock.readers, taskID)
		if lock.writer == taskID {
			lock.writer = ""
			lock.writerCount = 0
		}
		c.cleanupLock(path, lock)
	}
	c.cond.Broadcast() // Wake up waiters
}

// Snapshot returns a copy of the current lock state.
func (c *ConflictDetector) Snapshot() map[string]LockState {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make(map[string]LockState, len(c.locks))
	for path, lock := range c.locks {
		state := LockState{Writer: lock.writer}
		for reader := range lock.readers {
			state.Readers = append(state.Readers, reader)
		}
		out[path] = state
	}
	return out
}

// LockState summarizes lock holders for a path.
type LockState struct {
	Readers []string
	Writer  string
}

// LockConflictError indicates a conflicting lock holder.
type LockConflictError struct {
	Path    string
	Holder  string
	Mode    string
	Timeout bool
}

func (e *LockConflictError) Error() string {
	if e == nil {
		return "lock conflict"
	}
	if e.Timeout {
		return fmt.Sprintf("lock timeout on %s (held %s by %s)", e.Path, e.Mode, e.Holder)
	}
	return fmt.Sprintf("lock conflict on %s (held %s by %s)", e.Path, e.Mode, e.Holder)
}

func (c *ConflictDetector) lockFor(path string) *resourceLock {
	lock := c.locks[path]
	if lock == nil {
		lock = &resourceLock{readers: make(map[string]int)}
		c.locks[path] = lock
	}
	return lock
}

func (c *ConflictDetector) cleanupLock(path string, lock *resourceLock) {
	if lock == nil {
		return
	}
	if lock.writer == "" && len(lock.readers) == 0 {
		delete(c.locks, path)
	}
}

func normalizeLockInputs(taskID, path string) (string, string) {
	taskID = strings.TrimSpace(taskID)
	path = strings.TrimSpace(path)
	if path == "" {
		return taskID, ""
	}
	path = filepath.Clean(path)
	if path == "." {
		path = ""
	}
	return taskID, path
}
