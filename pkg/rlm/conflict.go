package rlm

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// ConflictDetector tracks read/write access for resources (typically files).
type ConflictDetector struct {
	mu    sync.Mutex
	locks map[string]*resourceLock
}

type resourceLock struct {
	readers     map[string]int
	writer      string
	writerCount int
}

// NewConflictDetector returns an empty detector.
func NewConflictDetector() *ConflictDetector {
	return &ConflictDetector{
		locks: make(map[string]*resourceLock),
	}
}

// AcquireRead acquires a read lock for a task on a path.
func (c *ConflictDetector) AcquireRead(taskID, path string) error {
	taskID, path = normalizeLockInputs(taskID, path)
	if taskID == "" || path == "" {
		return fmt.Errorf("invalid read lock: taskID and path required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	lock := c.lockFor(path)
	if lock.writer != "" && lock.writer != taskID {
		return &LockConflictError{Path: path, Holder: lock.writer, Mode: "write"}
	}
	lock.readers[taskID]++
	return nil
}

// AcquireWrite acquires a write lock for a task on a path.
func (c *ConflictDetector) AcquireWrite(taskID, path string) error {
	taskID, path = normalizeLockInputs(taskID, path)
	if taskID == "" || path == "" {
		return fmt.Errorf("invalid write lock: taskID and path required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	lock := c.lockFor(path)
	if lock.writer != "" && lock.writer != taskID {
		return &LockConflictError{Path: path, Holder: lock.writer, Mode: "write"}
	}
	for reader := range lock.readers {
		if reader != taskID {
			return &LockConflictError{Path: path, Holder: reader, Mode: "read"}
		}
	}
	delete(lock.readers, taskID)

	lock.writer = taskID
	lock.writerCount++
	return nil
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
	Path   string
	Holder string
	Mode   string
}

func (e *LockConflictError) Error() string {
	if e == nil {
		return "lock conflict"
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
