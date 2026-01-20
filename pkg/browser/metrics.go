package browser

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

// Metrics tracks browser runtime performance counters.
type Metrics struct {
	// Session counts
	SessionsCreated atomic.Int64
	SessionsClosed  atomic.Int64
	ActiveSessions  atomic.Int64

	// Operation counts
	NavigateCount atomic.Int64
	ObserveCount  atomic.Int64
	ActionCount   atomic.Int64
	StreamCount   atomic.Int64

	// Action outcomes
	ActionSuccessCount atomic.Int64
	ActionFailureCount atomic.Int64

	// Frame metrics
	FramesDelivered   atomic.Int64
	FrameLatencySum   atomic.Int64 // nanoseconds sum for averaging
	FrameLatencyCount atomic.Int64

	// Telemetry integration
	mu        sync.RWMutex
	hub       *telemetry.Hub
	sessionID string
}

// NewMetrics creates a new metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// EnableTelemetry wires the metrics collector to a telemetry hub.
func (m *Metrics) EnableTelemetry(hub *telemetry.Hub, sessionID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.hub = hub
	m.sessionID = sessionID
	m.mu.Unlock()
}

// RecordSessionCreated increments session creation counter.
func (m *Metrics) RecordSessionCreated(browserSessionID string) {
	if m == nil {
		return
	}
	m.SessionsCreated.Add(1)
	m.ActiveSessions.Add(1)
	m.publishEvent(telemetry.EventBrowserSessionCreated, map[string]any{
		"browser_session_id": browserSessionID,
	})
}

// RecordSessionClosed increments session close counter.
func (m *Metrics) RecordSessionClosed(browserSessionID string) {
	if m == nil {
		return
	}
	m.SessionsClosed.Add(1)
	m.ActiveSessions.Add(-1)
	m.publishEvent(telemetry.EventBrowserSessionClosed, map[string]any{
		"browser_session_id": browserSessionID,
	})
}

// RecordNavigate increments navigation counter.
func (m *Metrics) RecordNavigate(browserSessionID, url string, latency time.Duration) {
	if m == nil {
		return
	}
	m.NavigateCount.Add(1)
	m.publishEvent(telemetry.EventBrowserNavigate, map[string]any{
		"browser_session_id": browserSessionID,
		"url":                url,
		"latency_ms":         latency.Milliseconds(),
	})
}

// RecordObserve increments observe counter.
func (m *Metrics) RecordObserve(browserSessionID string, latency time.Duration, opts ObserveOptions) {
	if m == nil {
		return
	}
	m.ObserveCount.Add(1)
	m.publishEvent(telemetry.EventBrowserObserve, map[string]any{
		"browser_session_id":     browserSessionID,
		"latency_ms":             latency.Milliseconds(),
		"include_frame":          opts.IncludeFrame,
		"include_dom_snapshot":   opts.IncludeDOMSnapshot,
		"include_accessibility":  opts.IncludeAccessibility,
		"include_hit_test":       opts.IncludeHitTest,
	})
}

// RecordAction increments action counter and tracks success/failure.
func (m *Metrics) RecordAction(browserSessionID string, actionType ActionType, success bool, latency time.Duration) {
	if m == nil {
		return
	}
	m.ActionCount.Add(1)
	if success {
		m.ActionSuccessCount.Add(1)
		m.publishEvent(telemetry.EventBrowserAction, map[string]any{
			"browser_session_id": browserSessionID,
			"action_type":        string(actionType),
			"success":            true,
			"latency_ms":         latency.Milliseconds(),
		})
	} else {
		m.ActionFailureCount.Add(1)
		m.publishEvent(telemetry.EventBrowserActionFailed, map[string]any{
			"browser_session_id": browserSessionID,
			"action_type":        string(actionType),
			"success":            false,
			"latency_ms":         latency.Milliseconds(),
		})
	}
}

// RecordFrameDelivered tracks frame delivery latency.
func (m *Metrics) RecordFrameDelivered(browserSessionID string, latency time.Duration) {
	if m == nil {
		return
	}
	m.FramesDelivered.Add(1)
	m.FrameLatencySum.Add(latency.Nanoseconds())
	m.FrameLatencyCount.Add(1)
	m.publishEvent(telemetry.EventBrowserFrameDelivered, map[string]any{
		"browser_session_id": browserSessionID,
		"latency_ms":         latency.Milliseconds(),
	})
}

// RecordStreamEvent increments stream event counter.
func (m *Metrics) RecordStreamEvent(browserSessionID string, eventType StreamEventType) {
	if m == nil {
		return
	}
	m.StreamCount.Add(1)
	m.publishEvent(telemetry.EventBrowserStreamEvent, map[string]any{
		"browser_session_id": browserSessionID,
		"event_type":         string(eventType),
	})
}

// Snapshot returns a point-in-time snapshot of all metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	avgFrameLatency := time.Duration(0)
	count := m.FrameLatencyCount.Load()
	if count > 0 {
		avgFrameLatency = time.Duration(m.FrameLatencySum.Load() / count)
	}
	successCount := m.ActionSuccessCount.Load()
	failCount := m.ActionFailureCount.Load()
	total := successCount + failCount
	successRate := float64(1.0)
	if total > 0 {
		successRate = float64(successCount) / float64(total)
	}
	return MetricsSnapshot{
		SessionsCreated:        m.SessionsCreated.Load(),
		SessionsClosed:         m.SessionsClosed.Load(),
		ActiveSessions:         m.ActiveSessions.Load(),
		NavigateCount:          m.NavigateCount.Load(),
		ObserveCount:           m.ObserveCount.Load(),
		ActionCount:            m.ActionCount.Load(),
		StreamCount:            m.StreamCount.Load(),
		ActionSuccessCount:     successCount,
		ActionFailureCount:     failCount,
		ActionSuccessRate:      successRate,
		FramesDelivered:        m.FramesDelivered.Load(),
		AverageFrameLatency:    avgFrameLatency,
	}
}

func (m *Metrics) publishEvent(eventType telemetry.EventType, data map[string]any) {
	m.mu.RLock()
	hub := m.hub
	sessionID := m.sessionID
	m.mu.RUnlock()
	if hub == nil {
		return
	}
	hub.Publish(telemetry.Event{
		Type:      eventType,
		Timestamp: time.Now(),
		SessionID: sessionID,
		Data:      data,
	})
}

// MetricsSnapshot is a point-in-time copy of browser metrics.
type MetricsSnapshot struct {
	SessionsCreated     int64
	SessionsClosed      int64
	ActiveSessions      int64
	NavigateCount       int64
	ObserveCount        int64
	ActionCount         int64
	StreamCount         int64
	ActionSuccessCount  int64
	ActionFailureCount  int64
	ActionSuccessRate   float64
	FramesDelivered     int64
	AverageFrameLatency time.Duration
}
