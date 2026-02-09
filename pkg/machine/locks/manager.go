package locks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

// Manager provides reader-writer file locks with telemetry Hub integration.
// Based on pkg/rlm/conflict.go ConflictDetector.
type Manager struct {
	mu          sync.Mutex
	cond        *sync.Cond
	locks       map[string]*resourceLock
	timeout     time.Duration
	hub         *telemetry.Hub
	userAgentID string
}

type resourceLock struct {
	readers     map[string]int
	writer      string
	writerCount int
}

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// WithUserAgentID sets the user's agent ID. User locks have no timeout.
func WithUserAgentID(id string) ManagerOption {
	return func(m *Manager) { m.userAgentID = id }
}

// WithLockTimeout sets the default timeout for lock acquisition.
func WithLockTimeout(timeout time.Duration) ManagerOption {
	return func(m *Manager) { m.timeout = timeout }
}

// NewManager creates a lock manager that publishes events to the Hub.
func NewManager(hub *telemetry.Hub, opts ...ManagerOption) *Manager {
	m := &Manager{
		locks:   make(map[string]*resourceLock),
		timeout: 30 * time.Second,
		hub:     hub,
	}
	m.cond = sync.NewCond(&m.mu)
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// AcquireRead acquires a read lock using the default timeout.
func (m *Manager) AcquireRead(agentID, path string) error {
	return m.AcquireReadWithTimeout(agentID, path, m.timeout)
}

// AcquireReadWithTimeout acquires a read lock with a specific timeout.
func (m *Manager) AcquireReadWithTimeout(agentID, path string, timeout time.Duration) error {
	agentID, path = normalize(agentID, path)
	if agentID == "" || path == "" {
		return fmt.Errorf("invalid read lock: agentID and path required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	m.mu.Lock()
	defer m.mu.Unlock()

	for {
		lock := m.lockFor(path)
		if lock.writer == "" || lock.writer == agentID {
			lock.readers[agentID]++
			m.publishAcquired(agentID, path, "read")
			return nil
		}

		m.publishWaiting(agentID, path, lock.writer, "read")
		if err := m.waitWithContext(ctx); err != nil {
			return &LockConflictError{
				Path: path, Holder: lock.writer, Mode: "write", Timeout: true,
			}
		}
	}
}

// AcquireWrite acquires a write lock using the default timeout.
func (m *Manager) AcquireWrite(agentID, path string) error {
	return m.AcquireWriteWithTimeout(agentID, path, m.timeout)
}

// AcquireWriteWithTimeout acquires a write lock with a specific timeout.
func (m *Manager) AcquireWriteWithTimeout(agentID, path string, timeout time.Duration) error {
	agentID, path = normalize(agentID, path)
	if agentID == "" || path == "" {
		return fmt.Errorf("invalid write lock: agentID and path required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	m.mu.Lock()
	defer m.mu.Unlock()

	for {
		lock := m.lockFor(path)

		canAcquire := true
		var conflictHolder, conflictMode string

		if lock.writer != "" && lock.writer != agentID {
			canAcquire = false
			conflictHolder = lock.writer
			conflictMode = "write"
		}

		if canAcquire {
			for reader := range lock.readers {
				if reader != agentID {
					canAcquire = false
					conflictHolder = reader
					conflictMode = "read"
					break
				}
			}
		}

		if canAcquire {
			delete(lock.readers, agentID)
			lock.writer = agentID
			lock.writerCount++
			m.publishAcquired(agentID, path, "write")
			return nil
		}

		m.publishWaiting(agentID, path, conflictHolder, conflictMode)
		if err := m.waitWithContext(ctx); err != nil {
			return &LockConflictError{
				Path: path, Holder: conflictHolder, Mode: conflictMode, Timeout: true,
			}
		}
	}
}

// ReleaseRead releases a read lock.
func (m *Manager) ReleaseRead(agentID, path string) {
	agentID, path = normalize(agentID, path)
	if agentID == "" || path == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	lock := m.locks[path]
	if lock == nil {
		return
	}
	if count, ok := lock.readers[agentID]; ok {
		if count <= 1 {
			delete(lock.readers, agentID)
		} else {
			lock.readers[agentID] = count - 1
		}
	}
	m.cleanupLock(path, lock)
	m.publishReleased(agentID, path)
	m.cond.Broadcast()
}

// ReleaseWrite releases a write lock.
func (m *Manager) ReleaseWrite(agentID, path string) {
	agentID, path = normalize(agentID, path)
	if agentID == "" || path == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	lock := m.locks[path]
	if lock == nil || lock.writer != agentID {
		return
	}
	if lock.writerCount > 1 {
		lock.writerCount--
	} else {
		lock.writer = ""
		lock.writerCount = 0
	}
	m.cleanupLock(path, lock)
	m.publishReleased(agentID, path)
	m.cond.Broadcast()
}

// ReleaseAll clears any locks held by an agent.
func (m *Manager) ReleaseAll(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for path, lock := range m.locks {
		delete(lock.readers, agentID)
		if lock.writer == agentID {
			lock.writer = ""
			lock.writerCount = 0
		}
		m.cleanupLock(path, lock)
	}
	m.cond.Broadcast()
}

// Snapshot returns a copy of the current lock state.
func (m *Manager) Snapshot() map[string]LockState {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]LockState, len(m.locks))
	for path, lock := range m.locks {
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

// --- internal ---

func (m *Manager) lockFor(path string) *resourceLock {
	lock := m.locks[path]
	if lock == nil {
		lock = &resourceLock{readers: make(map[string]int)}
		m.locks[path] = lock
	}
	return lock
}

func (m *Manager) cleanupLock(path string, lock *resourceLock) {
	if lock == nil {
		return
	}
	if lock.writer == "" && len(lock.readers) == 0 {
		delete(m.locks, path)
	}
}

func (m *Manager) waitWithContext(ctx context.Context) error {
	cancelled := false
	done := make(chan struct{})
	go func() {
		m.mu.Lock()
		if !cancelled {
			m.cond.Wait()
		}
		m.mu.Unlock()
		close(done)
	}()

	// Release lock so the goroutine can acquire it for cond.Wait
	m.mu.Unlock()

	select {
	case <-done:
		m.mu.Lock()
		return nil
	case <-ctx.Done():
		m.mu.Lock()
		cancelled = true
		m.cond.Broadcast()
		m.mu.Unlock()
		<-done
		m.mu.Lock()
		return ctx.Err()
	}
}

func (m *Manager) publishAcquired(agentID, path, mode string) {
	m.publish(telemetry.EventMachineLockAcquired, map[string]any{
		"agent_id": agentID, "path": path, "mode": mode,
	})
}

func (m *Manager) publishWaiting(agentID, path, heldBy, mode string) {
	m.publish(telemetry.EventMachineLockWaiting, map[string]any{
		"agent_id": agentID, "path": path, "held_by": heldBy, "mode": mode,
	})
}

func (m *Manager) publishReleased(agentID, path string) {
	m.publish(telemetry.EventMachineLockReleased, map[string]any{
		"agent_id": agentID, "path": path,
	})
}

func (m *Manager) publish(eventType telemetry.EventType, data map[string]any) {
	if m.hub == nil {
		return
	}
	m.hub.Publish(telemetry.Event{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
	})
}

func normalize(agentID, path string) (string, string) {
	agentID = strings.TrimSpace(agentID)
	path = strings.TrimSpace(path)
	if path == "" {
		return agentID, ""
	}
	path = filepath.Clean(path)
	if path == "." {
		path = ""
	}
	return agentID, path
}
