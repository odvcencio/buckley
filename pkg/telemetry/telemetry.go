package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
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

	// Browser runtime events
	EventBrowserSessionCreated EventType = "browser.session_created"
	EventBrowserSessionClosed  EventType = "browser.session_closed"
	EventBrowserNavigate       EventType = "browser.navigate"
	EventBrowserObserve        EventType = "browser.observe"
	EventBrowserAction         EventType = "browser.action"
	EventBrowserActionFailed   EventType = "browser.action_failed"
	EventBrowserFrameDelivered EventType = "browser.frame_delivered"
	EventBrowserStreamEvent    EventType = "browser.stream_event"
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

const (
	// DefaultEventQueueSize is the default buffer size for the event queue.
	DefaultEventQueueSize = 1000
	// DefaultBatchSize is the default number of events to batch before flushing.
	DefaultBatchSize = 100
	// DefaultFlushInterval is the default interval to flush batched events.
	DefaultFlushInterval = 100 * time.Millisecond
	// DefaultRateLimit is the default rate limit for events per second.
	DefaultRateLimit = 1000
	// DefaultSubscriberChannelSize is the default buffer size for subscriber channels.
	DefaultSubscriberChannelSize = 64
)

// subscriber represents a single subscriber with an ID and channel.
type subscriber struct {
	id string
	ch chan Event
}

// Config holds configuration options for the Hub.
type Config struct {
	// EventQueueSize is the buffer size for the internal event queue.
	// Events are dropped if the queue is full.
	EventQueueSize int
	// BatchSize is the number of events to accumulate before flushing to subscribers.
	BatchSize int
	// FlushInterval is the maximum time to wait before flushing batched events.
	FlushInterval time.Duration
	// RateLimit is the maximum number of events per second.
	// Events exceeding this rate are dropped.
	RateLimit int
	// SubscriberChannelSize is the buffer size for individual subscriber channels.
	SubscriberChannelSize int
}

// DefaultConfig returns a default configuration.
func DefaultConfig() *Config {
	return &Config{
		EventQueueSize:        DefaultEventQueueSize,
		BatchSize:             DefaultBatchSize,
		FlushInterval:         DefaultFlushInterval,
		RateLimit:             DefaultRateLimit,
		SubscriberChannelSize: DefaultSubscriberChannelSize,
	}
}

// Hub is an optimized telemetry event hub that supports:
// - Non-blocking event publishing with buffered queue
// - Event batching for high-frequency scenarios
// - Rate limiting using token bucket algorithm
// - Thread-safe subscriber management
// - Graceful shutdown with event flushing
type Hub struct {
	config *Config

	// Event queue for non-blocking publish
	eventCh chan Event

	// Subscriber management
	subscribers   map[string]*subscriber
	subscriberMu  sync.RWMutex
	subscriberSeq int

	// Batching
	batch     []Event
	batchMu   sync.Mutex
	flushTick *time.Ticker

	// Rate limiting
	rateLimiter *rate.Limiter

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
	closeMu sync.RWMutex
}

// NewHub creates a new telemetry hub with default configuration.
func NewHub() *Hub {
	return NewHubWithConfig(DefaultConfig())
}

// NewHubWithConfig creates a new telemetry hub with custom configuration.
func NewHubWithConfig(config *Config) *Hub {
	if config.EventQueueSize <= 0 {
		config.EventQueueSize = DefaultEventQueueSize
	}
	if config.BatchSize <= 0 {
		config.BatchSize = DefaultBatchSize
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = DefaultFlushInterval
	}
	if config.RateLimit <= 0 {
		config.RateLimit = DefaultRateLimit
	}
	if config.SubscriberChannelSize <= 0 {
		config.SubscriberChannelSize = DefaultSubscriberChannelSize
	}

	ctx, cancel := context.WithCancel(context.Background())

	h := &Hub{
		config:      config,
		eventCh:     make(chan Event, config.EventQueueSize),
		subscribers: make(map[string]*subscriber),
		batch:       make([]Event, 0, config.BatchSize),
		flushTick:   time.NewTicker(config.FlushInterval),
		rateLimiter: rate.NewLimiter(rate.Limit(config.RateLimit), config.RateLimit),
		ctx:         ctx,
		cancel:      cancel,
	}

	// Start the event processing loop
	h.wg.Add(1)
	go h.processEvents()

	return h
}

// processEvents is the main event processing loop.
// It handles event batching, rate limiting, and dispatching to subscribers.
func (h *Hub) processEvents() {
	defer h.wg.Done()

	for {
		select {
		case <-h.ctx.Done():
			// Flush remaining events before exiting
			h.flushBatch()
			return

		case event := <-h.eventCh:
			h.handleEvent(event)

		case <-h.flushTick.C:
			h.flushBatch()
		}
	}
}

// handleEvent processes a single event with batching and rate limiting.
func (h *Hub) handleEvent(event Event) {
	// Check rate limit
	if !h.rateLimiter.Allow() {
		// Rate limit exceeded, drop event
		return
	}

	h.batchMu.Lock()
	h.batch = append(h.batch, event)
	shouldFlush := len(h.batch) >= h.config.BatchSize
	h.batchMu.Unlock()

	if shouldFlush {
		h.flushBatch()
	}
}

// flushBatch dispatches all batched events to subscribers.
// Uses non-blocking sends to prevent slow subscribers from blocking the hub.
func (h *Hub) flushBatch() {
	h.batchMu.Lock()
	if len(h.batch) == 0 {
		h.batchMu.Unlock()
		return
	}

	// Copy batch to local variable and reset
	batch := h.batch
	h.batch = make([]Event, 0, h.config.BatchSize)
	h.batchMu.Unlock()

	// Dispatch to all subscribers
	h.subscriberMu.RLock()
	subscribers := make([]*subscriber, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		subscribers = append(subscribers, sub)
	}
	h.subscriberMu.RUnlock()

	for _, sub := range subscribers {
		for _, event := range batch {
			select {
			case sub.ch <- event:
			default:
				// Drop if subscriber's buffer is full
				// This prevents slow subscribers from blocking the entire hub
			}
		}
	}
}

// Flush forces an immediate flush of batched events.
// This is useful for tests or when immediate delivery is required.
func (h *Hub) Flush() {
	h.flushBatch()
}

// Publish notifies all subscribers of an event.
// This method is non-blocking; events are dropped if the queue is full.
// Maintains backward compatibility with the original Hub.Publish signature.
func (h *Hub) Publish(event Event) {
	h.closeMu.RLock()
	if h.closed {
		h.closeMu.RUnlock()
		return
	}
	h.closeMu.RUnlock()

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// Non-blocking send to event queue
	select {
	case h.eventCh <- event:
	default:
		// Queue is full, drop event
	}
}

// Subscribe returns a channel that will receive future events and a cleanup func.
// Maintains backward compatibility with the original Hub.Subscribe signature.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	id := h.generateSubscriberID()
	ch := make(chan Event, h.config.SubscriberChannelSize)

	h.subscriberMu.Lock()
	if h.isClosed() {
		h.subscriberMu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subscribers[id] = &subscriber{id: id, ch: ch}
	h.subscriberMu.Unlock()

	unsubscribe := func() {
		h.Unsubscribe(id)
	}

	return ch, unsubscribe
}

// SubscribeWithID returns a channel that will receive future events and a subscriber ID.
// The returned ID can be used with Unsubscribe(id) for explicit unsubscription.
func (h *Hub) SubscribeWithID() (<-chan Event, string) {
	id := h.generateSubscriberID()
	ch := make(chan Event, h.config.SubscriberChannelSize)

	h.subscriberMu.Lock()
	if h.isClosed() {
		h.subscriberMu.Unlock()
		close(ch)
		return ch, id
	}
	h.subscribers[id] = &subscriber{id: id, ch: ch}
	h.subscriberMu.Unlock()

	return ch, id
}

// Unsubscribe removes a subscriber by ID and closes its channel.
// This is thread-safe and can be called concurrently.
func (h *Hub) Unsubscribe(id string) {
	h.subscriberMu.Lock()
	defer h.subscriberMu.Unlock()

	if sub, ok := h.subscribers[id]; ok {
		delete(h.subscribers, id)
		close(sub.ch)
	}
}

// generateSubscriberID generates a unique subscriber ID.
func (h *Hub) generateSubscriberID() string {
	h.subscriberMu.Lock()
	defer h.subscriberMu.Unlock()
	h.subscriberSeq++
	return fmt.Sprintf("sub-%d-%d", time.Now().UnixNano(), h.subscriberSeq)
}

// isClosed returns true if the hub is closed.
func (h *Hub) isClosed() bool {
	h.closeMu.RLock()
	defer h.closeMu.RUnlock()
	return h.closed
}

// Stop initiates graceful shutdown and flushes remaining events.
// This method returns immediately; use Wait() to block until complete.
func (h *Hub) Stop() {
	h.closeMu.Lock()
	if h.closed {
		h.closeMu.Unlock()
		return
	}
	h.closed = true
	h.closeMu.Unlock()

	// Stop the flush ticker
	h.flushTick.Stop()

	// Cancel context to signal shutdown
	h.cancel()
}

// Wait blocks until the hub has finished processing all events and shut down.
// Call Stop() before Wait() to initiate graceful shutdown.
func (h *Hub) Wait() {
	h.wg.Wait()
}

// Close unsubscribes all listeners and prevents future publications.
// This is an alias for Stop() + Wait() for backward compatibility.
func (h *Hub) Close() {
	h.Stop()
	h.Wait()

	// Close all subscriber channels
	h.subscriberMu.Lock()
	for _, sub := range h.subscribers {
		close(sub.ch)
	}
	h.subscribers = make(map[string]*subscriber)
	h.subscriberMu.Unlock()
}

// Stats returns current hub statistics for monitoring.
type Stats struct {
	SubscriberCount int
	QueueSize       int
	BatchSize       int
	RateLimit       int
}

// GetStats returns current hub statistics.
func (h *Hub) GetStats() Stats {
	h.subscriberMu.RLock()
	subscriberCount := len(h.subscribers)
	h.subscriberMu.RUnlock()

	return Stats{
		SubscriberCount: subscriberCount,
		QueueSize:       len(h.eventCh),
		BatchSize:       len(h.batch),
		RateLimit:       h.config.RateLimit,
	}
}
