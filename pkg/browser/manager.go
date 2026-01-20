package browser

import (
	"context"
	"fmt"
	"sync"
)

// Manager tracks active browser sessions for a runtime.
type Manager struct {
	runtime  Runtime
	sessions map[string]BrowserSession
	mu       sync.Mutex
}

// NewManager creates a Manager backed by the provided runtime.
func NewManager(runtime Runtime) *Manager {
	return &Manager{
		runtime:  runtime,
		sessions: make(map[string]BrowserSession),
	}
}

// CreateSession allocates a new browser session.
func (m *Manager) CreateSession(ctx context.Context, cfg SessionConfig) (BrowserSession, error) {
	if m == nil || m.runtime == nil {
		return nil, ErrUnavailable
	}
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	m.mu.Lock()
	if _, exists := m.sessions[cfg.SessionID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("session already exists: %s", cfg.SessionID)
	}
	m.mu.Unlock()

	sess, err := m.runtime.NewSession(ctx, cfg)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[cfg.SessionID] = sess
	m.mu.Unlock()
	return sess, nil
}

// GetSession returns a session by ID.
func (m *Manager) GetSession(sessionID string) (BrowserSession, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	return sess, ok
}

// CloseSession closes and removes a session.
func (m *Manager) CloseSession(sessionID string) error {
	if m == nil {
		return ErrUnavailable
	}
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if !ok || sess == nil {
		return ErrSessionClosed
	}
	return sess.Close()
}

// Close closes all sessions and releases the runtime.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	sessions := make([]BrowserSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.sessions = make(map[string]BrowserSession)
	m.mu.Unlock()

	var lastErr error
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		if err := sess.Close(); err != nil {
			lastErr = err
		}
	}
	if m.runtime != nil {
		if err := m.runtime.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
