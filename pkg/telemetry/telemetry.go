package telemetry

import (
	"sync"
	"time"
)

// EventType identifies the kind of telemetry event.
type EventType string

const (
	EventPlanCreated                EventType = "plan.created"
	EventPlanUpdated                EventType = "plan.updated"
	EventTaskStarted                EventType = "task.started"
	EventTaskCompleted              EventType = "task.completed"
	EventTaskFailed                 EventType = "task.failed"
	EventResearchStarted            EventType = "research.started"
	EventResearchCompleted          EventType = "research.completed"
	EventResearchFailed             EventType = "research.failed"
	EventBuilderStarted             EventType = "builder.started"
	EventBuilderCompleted           EventType = "builder.completed"
	EventBuilderFailed              EventType = "builder.failed"
	EventCostUpdated                EventType = "cost.updated"
	EventTokenUsageUpdated          EventType = "tokens.updated"
	EventShellCommandStarted        EventType = "shell.started"
	EventShellCommandCompleted      EventType = "shell.completed"
	EventShellCommandFailed         EventType = "shell.failed"
	EventToolStarted                EventType = "tool.started"
	EventToolCompleted              EventType = "tool.completed"
	EventToolFailed                 EventType = "tool.failed"
	EventModelStreamStarted         EventType = "model.stream_start"
	EventModelStreamEnded           EventType = "model.stream_end"
	EventIndexStarted               EventType = "index.started"
	EventIndexCompleted             EventType = "index.completed"
	EventIndexFailed                EventType = "index.failed"
	EventEditorInline               EventType = "editor.inline"
	EventEditorPropose              EventType = "editor.propose"
	EventEditorApply                EventType = "editor.apply"
	EventUICommand                  EventType = "ui.command"
	EventExperimentStarted          EventType = "experiment.started"
	EventExperimentCompleted        EventType = "experiment.completed"
	EventExperimentFailed           EventType = "experiment.failed"
	EventExperimentVariantStarted   EventType = "experiment.variant.started"
	EventExperimentVariantCompleted EventType = "experiment.variant.completed"
	EventExperimentVariantFailed    EventType = "experiment.variant.failed"
	EventRLMIteration               EventType = "rlm.iteration"
	EventCircuitFailure             EventType = "circuit.failure"
	EventCircuitStateChange         EventType = "circuit.state_change"

	// RLM transparency events
	EventRLMEscalation    EventType = "rlm.escalation"     // Weight tier escalation
	EventRLMToolCall      EventType = "rlm.tool_call"      // Sub-agent tool execution
	EventRLMReasoning     EventType = "rlm.reasoning"      // Coordinator reasoning trace
	EventRLMBudgetWarning EventType = "rlm.budget_warning" // Token/time budget alerts
)

// Event describes workflow telemetry that UIs and IPC clients can consume.
type Event struct {
	Type      EventType      `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	SessionID string         `json:"sessionId,omitempty"`
	PlanID    string         `json:"planId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Hub fan-outs telemetry events to any number of subscribers.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
	closed      bool
}

// NewHub constructs a telemetry hub.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan Event]struct{})}
}

// Publish notifies all subscribers of an event. Non-blocking; drops if buffer full.
func (h *Hub) Publish(event Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	for ch := range h.subscribers {
		select {
		case ch <- event:
		default:
			// Drop if subscriber can't keep up; prevents blocking workflow.
		}
	}
}

// Subscribe returns a channel that will receive future events and a cleanup func.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		empty := make(chan Event)
		close(empty)
		return empty, func() {}
	}
	ch := make(chan Event, 64)
	h.subscribers[ch] = struct{}{}
	unsubscribe := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subscribers[ch]; ok {
			delete(h.subscribers, ch)
			close(ch)
		}
	}
	return ch, unsubscribe
}

// Close unsubscribes all listeners and prevents future publications.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subscribers {
		close(ch)
		delete(h.subscribers, ch)
	}
}
