package browser

import (
	"testing"
	"time"
)

func TestMetrics_NewMetrics(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}
	snap := m.Snapshot()
	if snap.SessionsCreated != 0 || snap.NavigateCount != 0 || snap.ActionCount != 0 {
		t.Errorf("fresh metrics should have all zero counters: %+v", snap)
	}
}

func TestMetrics_NilReceiverSafety(t *testing.T) {
	var m *Metrics

	// None of these should panic
	m.RecordSessionCreated("s1")
	m.RecordSessionClosed("s1")
	m.RecordNavigate("s1", "http://example.com", time.Second)
	m.RecordObserve("s1", time.Second, ObserveOptions{})
	m.RecordAction("s1", ActionClick, true, time.Second)
	m.RecordAction("s1", ActionClick, false, time.Second)
	m.RecordFrameDelivered("s1", time.Second)
	m.RecordStreamEvent("s1", StreamEventFrame)
	m.EnableTelemetry(nil, "")

	snap := m.Snapshot()
	if snap.SessionsCreated != 0 {
		t.Error("nil metrics snapshot should be zero-valued")
	}
}

func TestMetrics_RecordSessionCreated(t *testing.T) {
	m := NewMetrics()

	m.RecordSessionCreated("s1")
	m.RecordSessionCreated("s2")

	snap := m.Snapshot()
	if snap.SessionsCreated != 2 {
		t.Errorf("SessionsCreated = %d, want 2", snap.SessionsCreated)
	}
	if snap.ActiveSessions != 2 {
		t.Errorf("ActiveSessions = %d, want 2", snap.ActiveSessions)
	}
}

func TestMetrics_RecordSessionClosed(t *testing.T) {
	m := NewMetrics()

	m.RecordSessionCreated("s1")
	m.RecordSessionCreated("s2")
	m.RecordSessionClosed("s1")

	snap := m.Snapshot()
	if snap.SessionsClosed != 1 {
		t.Errorf("SessionsClosed = %d, want 1", snap.SessionsClosed)
	}
	if snap.ActiveSessions != 1 {
		t.Errorf("ActiveSessions = %d, want 1", snap.ActiveSessions)
	}
}

func TestMetrics_RecordNavigate(t *testing.T) {
	m := NewMetrics()

	m.RecordNavigate("s1", "http://example.com", 100*time.Millisecond)
	m.RecordNavigate("s1", "http://example.org", 200*time.Millisecond)
	m.RecordNavigate("s2", "http://test.com", 50*time.Millisecond)

	snap := m.Snapshot()
	if snap.NavigateCount != 3 {
		t.Errorf("NavigateCount = %d, want 3", snap.NavigateCount)
	}
}

func TestMetrics_RecordObserve(t *testing.T) {
	m := NewMetrics()

	opts := ObserveOptions{IncludeFrame: true, IncludeDOMSnapshot: true}
	m.RecordObserve("s1", 50*time.Millisecond, opts)
	m.RecordObserve("s1", 60*time.Millisecond, ObserveOptions{})

	snap := m.Snapshot()
	if snap.ObserveCount != 2 {
		t.Errorf("ObserveCount = %d, want 2", snap.ObserveCount)
	}
}

func TestMetrics_RecordAction_Success(t *testing.T) {
	m := NewMetrics()

	m.RecordAction("s1", ActionClick, true, 10*time.Millisecond)
	m.RecordAction("s1", ActionTypeText, true, 20*time.Millisecond)

	snap := m.Snapshot()
	if snap.ActionCount != 2 {
		t.Errorf("ActionCount = %d, want 2", snap.ActionCount)
	}
	if snap.ActionSuccessCount != 2 {
		t.Errorf("ActionSuccessCount = %d, want 2", snap.ActionSuccessCount)
	}
	if snap.ActionFailureCount != 0 {
		t.Errorf("ActionFailureCount = %d, want 0", snap.ActionFailureCount)
	}
}

func TestMetrics_RecordAction_Failure(t *testing.T) {
	m := NewMetrics()

	m.RecordAction("s1", ActionClick, false, 10*time.Millisecond)

	snap := m.Snapshot()
	if snap.ActionCount != 1 {
		t.Errorf("ActionCount = %d, want 1", snap.ActionCount)
	}
	if snap.ActionSuccessCount != 0 {
		t.Errorf("ActionSuccessCount = %d, want 0", snap.ActionSuccessCount)
	}
	if snap.ActionFailureCount != 1 {
		t.Errorf("ActionFailureCount = %d, want 1", snap.ActionFailureCount)
	}
}

func TestMetrics_RecordAction_MixedOutcomes(t *testing.T) {
	m := NewMetrics()

	m.RecordAction("s1", ActionClick, true, 10*time.Millisecond)
	m.RecordAction("s1", ActionClick, true, 10*time.Millisecond)
	m.RecordAction("s1", ActionClick, false, 10*time.Millisecond)

	snap := m.Snapshot()
	if snap.ActionCount != 3 {
		t.Errorf("ActionCount = %d, want 3", snap.ActionCount)
	}
	if snap.ActionSuccessCount != 2 {
		t.Errorf("ActionSuccessCount = %d, want 2", snap.ActionSuccessCount)
	}
	if snap.ActionFailureCount != 1 {
		t.Errorf("ActionFailureCount = %d, want 1", snap.ActionFailureCount)
	}
}

func TestMetrics_RecordFrameDelivered(t *testing.T) {
	m := NewMetrics()

	m.RecordFrameDelivered("s1", 10*time.Millisecond)
	m.RecordFrameDelivered("s1", 20*time.Millisecond)
	m.RecordFrameDelivered("s1", 30*time.Millisecond)

	snap := m.Snapshot()
	if snap.FramesDelivered != 3 {
		t.Errorf("FramesDelivered = %d, want 3", snap.FramesDelivered)
	}
}

func TestMetrics_RecordStreamEvent(t *testing.T) {
	m := NewMetrics()

	m.RecordStreamEvent("s1", StreamEventFrame)
	m.RecordStreamEvent("s1", StreamEventDOMDiff)

	snap := m.Snapshot()
	if snap.StreamCount != 2 {
		t.Errorf("StreamCount = %d, want 2", snap.StreamCount)
	}
}

func TestMetrics_Snapshot_SuccessRate(t *testing.T) {
	tests := []struct {
		name        string
		successes   int
		failures    int
		wantRate    float64
		wantDefault bool // when no actions, rate defaults to 1.0
	}{
		{
			name:        "no actions defaults to 1.0",
			successes:   0,
			failures:    0,
			wantRate:    1.0,
			wantDefault: true,
		},
		{
			name:      "all success",
			successes: 5,
			failures:  0,
			wantRate:  1.0,
		},
		{
			name:      "all failure",
			successes: 0,
			failures:  3,
			wantRate:  0.0,
		},
		{
			name:      "mixed 75 percent",
			successes: 3,
			failures:  1,
			wantRate:  0.75,
		},
		{
			name:      "mixed 50 percent",
			successes: 2,
			failures:  2,
			wantRate:  0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMetrics()
			for i := 0; i < tt.successes; i++ {
				m.RecordAction("s1", ActionClick, true, time.Millisecond)
			}
			for i := 0; i < tt.failures; i++ {
				m.RecordAction("s1", ActionClick, false, time.Millisecond)
			}
			snap := m.Snapshot()
			if snap.ActionSuccessRate != tt.wantRate {
				t.Errorf("ActionSuccessRate = %f, want %f", snap.ActionSuccessRate, tt.wantRate)
			}
		})
	}
}

func TestMetrics_Snapshot_AverageFrameLatency(t *testing.T) {
	tests := []struct {
		name       string
		latencies  []time.Duration
		wantAvgNs  int64
	}{
		{
			name:      "no frames gives zero latency",
			latencies: nil,
			wantAvgNs: 0,
		},
		{
			name:      "single frame",
			latencies: []time.Duration{10 * time.Millisecond},
			wantAvgNs: (10 * time.Millisecond).Nanoseconds(),
		},
		{
			name:      "multiple frames averaged",
			latencies: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond},
			wantAvgNs: (20 * time.Millisecond).Nanoseconds(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMetrics()
			for _, lat := range tt.latencies {
				m.RecordFrameDelivered("s1", lat)
			}
			snap := m.Snapshot()
			gotNs := snap.AverageFrameLatency.Nanoseconds()
			if gotNs != tt.wantAvgNs {
				t.Errorf("AverageFrameLatency = %v (%d ns), want %d ns",
					snap.AverageFrameLatency, gotNs, tt.wantAvgNs)
			}
		})
	}
}

func TestMetrics_Snapshot_FullScenario(t *testing.T) {
	m := NewMetrics()

	// Simulate a realistic session
	m.RecordSessionCreated("s1")
	m.RecordNavigate("s1", "http://example.com", 100*time.Millisecond)
	m.RecordObserve("s1", 50*time.Millisecond, ObserveOptions{IncludeDOMSnapshot: true})
	m.RecordAction("s1", ActionClick, true, 10*time.Millisecond)
	m.RecordAction("s1", ActionTypeText, true, 20*time.Millisecond)
	m.RecordAction("s1", ActionScroll, false, 5*time.Millisecond)
	m.RecordFrameDelivered("s1", 16*time.Millisecond)
	m.RecordFrameDelivered("s1", 18*time.Millisecond)
	m.RecordStreamEvent("s1", StreamEventFrame)
	m.RecordSessionClosed("s1")

	snap := m.Snapshot()

	if snap.SessionsCreated != 1 {
		t.Errorf("SessionsCreated = %d, want 1", snap.SessionsCreated)
	}
	if snap.SessionsClosed != 1 {
		t.Errorf("SessionsClosed = %d, want 1", snap.SessionsClosed)
	}
	if snap.ActiveSessions != 0 {
		t.Errorf("ActiveSessions = %d, want 0", snap.ActiveSessions)
	}
	if snap.NavigateCount != 1 {
		t.Errorf("NavigateCount = %d, want 1", snap.NavigateCount)
	}
	if snap.ObserveCount != 1 {
		t.Errorf("ObserveCount = %d, want 1", snap.ObserveCount)
	}
	if snap.ActionCount != 3 {
		t.Errorf("ActionCount = %d, want 3", snap.ActionCount)
	}
	if snap.ActionSuccessCount != 2 {
		t.Errorf("ActionSuccessCount = %d, want 2", snap.ActionSuccessCount)
	}
	if snap.ActionFailureCount != 1 {
		t.Errorf("ActionFailureCount = %d, want 1", snap.ActionFailureCount)
	}
	if snap.StreamCount != 1 {
		t.Errorf("StreamCount = %d, want 1", snap.StreamCount)
	}
	if snap.FramesDelivered != 2 {
		t.Errorf("FramesDelivered = %d, want 2", snap.FramesDelivered)
	}

	// Average of 16ms and 18ms = 17ms
	expectedAvg := (16*time.Millisecond + 18*time.Millisecond) / 2
	if snap.AverageFrameLatency != expectedAvg {
		t.Errorf("AverageFrameLatency = %v, want %v", snap.AverageFrameLatency, expectedAvg)
	}

	// 2 success / 3 total = 0.666...
	expectedRate := float64(2) / float64(3)
	if snap.ActionSuccessRate != expectedRate {
		t.Errorf("ActionSuccessRate = %f, want %f", snap.ActionSuccessRate, expectedRate)
	}
}
