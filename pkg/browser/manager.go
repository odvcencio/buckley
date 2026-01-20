package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

// Manager tracks active browser sessions for a runtime.
type Manager struct {
	runtime  Runtime
	sessions map[string]BrowserSession
	mu       sync.Mutex
	metrics  *Metrics
}

// instrumentedSession wraps a BrowserSession to record metrics.
type instrumentedSession struct {
	inner   BrowserSession
	metrics *Metrics
}

func (s *instrumentedSession) ID() string {
	return s.inner.ID()
}

func (s *instrumentedSession) Navigate(ctx context.Context, url string) (*Observation, error) {
	start := time.Now()
	obs, err := s.inner.Navigate(ctx, url)
	if s.metrics != nil {
		s.metrics.RecordNavigate(s.inner.ID(), url, time.Since(start))
	}
	return obs, err
}

func (s *instrumentedSession) Observe(ctx context.Context, opts ObserveOptions) (*Observation, error) {
	start := time.Now()
	obs, err := s.inner.Observe(ctx, opts)
	latency := time.Since(start)
	if s.metrics != nil {
		s.metrics.RecordObserve(s.inner.ID(), latency, opts)
		if err == nil && obs != nil && obs.Frame != nil {
			s.metrics.RecordFrameDelivered(s.inner.ID(), latency)
		}
	}
	return obs, err
}

func (s *instrumentedSession) Act(ctx context.Context, action Action) (*ActionResult, error) {
	start := time.Now()
	result, err := s.inner.Act(ctx, action)
	if s.metrics != nil {
		s.metrics.RecordAction(s.inner.ID(), action.Type, err == nil, time.Since(start))
	}
	return result, err
}

func (s *instrumentedSession) Stream(ctx context.Context, opts StreamOptions) (<-chan StreamEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	events, err := s.inner.Stream(ctx, opts)
	if err != nil || s.metrics == nil {
		return events, err
	}
	out := make(chan StreamEvent)
	go func() {
		defer close(out)
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				s.metrics.RecordStreamEvent(s.inner.ID(), event.Type)
				if event.Type == StreamEventFrame && event.Frame != nil {
					latency := time.Duration(0)
					if !event.Frame.Timestamp.IsZero() {
						latency = time.Since(event.Frame.Timestamp)
						if latency < 0 {
							latency = 0
						}
					}
					s.metrics.RecordFrameDelivered(s.inner.ID(), latency)
				}
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (s *instrumentedSession) Close() error {
	return s.inner.Close()
}

// NewManager creates a Manager backed by the provided runtime.
func NewManager(runtime Runtime) *Manager {
	return &Manager{
		runtime:  runtime,
		sessions: make(map[string]BrowserSession),
		metrics:  NewMetrics(),
	}
}

// EnableTelemetry wires the manager to a telemetry hub for metrics reporting.
func (m *Manager) EnableTelemetry(hub *telemetry.Hub, sessionID string) {
	if m == nil || m.metrics == nil {
		return
	}
	m.metrics.EnableTelemetry(hub, sessionID)
}

// Metrics returns the current metrics snapshot.
func (m *Manager) Metrics() MetricsSnapshot {
	if m == nil || m.metrics == nil {
		return MetricsSnapshot{}
	}
	return m.metrics.Snapshot()
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

	// Wrap the session to record metrics
	wrapped := &instrumentedSession{inner: sess, metrics: m.metrics}

	m.mu.Lock()
	m.sessions[cfg.SessionID] = wrapped
	m.mu.Unlock()

	if m.metrics != nil {
		m.metrics.RecordSessionCreated(cfg.SessionID)
	}
	return wrapped, nil
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
	err := sess.Close()
	if m.metrics != nil {
		m.metrics.RecordSessionClosed(sessionID)
	}
	return err
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
