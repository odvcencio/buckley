// Package notify provides async notifications for human-in-the-loop workflows.
// When Buckley gets stuck or needs guidance, it can notify the user via
// Telegram, Slack, Signal, or other channels.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// EventType defines the type of notification event.
type EventType string

const (
	// EventStuck is sent when Buckley needs human guidance
	EventStuck EventType = "stuck"

	// EventQuestion is sent when Buckley has a question
	EventQuestion EventType = "question"

	// EventProgress is sent for progress updates
	EventProgress EventType = "progress"

	// EventComplete is sent when a task completes
	EventComplete EventType = "complete"

	// EventError is sent on errors
	EventError EventType = "error"
)

// Event is a notification event.
type Event struct {
	// ID is the unique event identifier
	ID string `json:"id"`

	// Type is the event type
	Type EventType `json:"type"`

	// SessionID is the Buckley session this event relates to
	SessionID string `json:"session_id"`

	// TaskID is the task this event relates to (optional)
	TaskID string `json:"task_id,omitempty"`

	// Title is a short summary
	Title string `json:"title"`

	// Message is the detailed message
	Message string `json:"message"`

	// Options are available response options (for questions)
	Options []ResponseOption `json:"options,omitempty"`

	// Metadata contains additional context
	Metadata map[string]interface{} `json:"metadata,omitempty"`

	// Timestamp is when the event occurred
	Timestamp time.Time `json:"timestamp"`

	// ReplyTo is the NATS subject to reply to
	ReplyTo string `json:"reply_to,omitempty"`
}

// ResponseOption is an option the user can select.
type ResponseOption struct {
	// ID is the option identifier
	ID string `json:"id"`

	// Label is the display text
	Label string `json:"label"`

	// Description provides more context
	Description string `json:"description,omitempty"`
}

// Response is a user's response to an event.
type Response struct {
	// EventID is the event being responded to
	EventID string `json:"event_id"`

	// OptionID is the selected option (if any)
	OptionID string `json:"option_id,omitempty"`

	// Text is free-form text response
	Text string `json:"text,omitempty"`

	// UserID identifies the responder
	UserID string `json:"user_id,omitempty"`

	// Timestamp is when the response was sent
	Timestamp time.Time `json:"timestamp"`
}

// Publisher publishes notification events.
type Publisher interface {
	// Publish sends an event to the notification system
	Publish(ctx context.Context, event *Event) error

	// Close closes the publisher
	Close() error
}

// Subscriber receives notification events.
type Subscriber interface {
	// Subscribe starts receiving events
	Subscribe(ctx context.Context, handler func(*Event)) error

	// Close closes the subscriber
	Close() error
}

// Adapter sends notifications to a specific channel (Telegram, Slack, etc).
type Adapter interface {
	// Name returns the adapter name
	Name() string

	// Send sends a notification
	Send(ctx context.Context, event *Event) error

	// ReceiveResponses returns a channel of responses
	ReceiveResponses(ctx context.Context) (<-chan *Response, error)

	// Close closes the adapter
	Close() error
}

// Manager manages notification adapters and event routing.
type Manager struct {
	adapters   []Adapter
	publisher  Publisher
	subscriber Subscriber
}

// NewManager creates a notification manager.
func NewManager(publisher Publisher, adapters ...Adapter) *Manager {
	return &Manager{
		adapters:  adapters,
		publisher: publisher,
	}
}

// Notify sends a notification via all configured adapters.
func (m *Manager) Notify(ctx context.Context, event *Event) error {
	// Publish to event bus
	if m.publisher != nil {
		if err := m.publisher.Publish(ctx, event); err != nil {
			return fmt.Errorf("publish event: %w", err)
		}
	}

	// Send via all adapters
	var lastErr error
	for _, adapter := range m.adapters {
		if err := adapter.Send(ctx, event); err != nil {
			lastErr = fmt.Errorf("%s: %w", adapter.Name(), err)
		}
	}

	return lastErr
}

// NotifyStuck sends a "stuck" notification.
func (m *Manager) NotifyStuck(ctx context.Context, sessionID, title, message string, options ...ResponseOption) error {
	return m.Notify(ctx, &Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:      EventStuck,
		SessionID: sessionID,
		Title:     title,
		Message:   message,
		Options:   options,
		Timestamp: time.Now(),
	})
}

// NotifyQuestion sends a question notification.
func (m *Manager) NotifyQuestion(ctx context.Context, sessionID, question string, options ...ResponseOption) error {
	return m.Notify(ctx, &Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:      EventQuestion,
		SessionID: sessionID,
		Title:     "Question",
		Message:   question,
		Options:   options,
		Timestamp: time.Now(),
	})
}

// NotifyProgress sends a progress notification.
func (m *Manager) NotifyProgress(ctx context.Context, sessionID, title, message string) error {
	return m.Notify(ctx, &Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:      EventProgress,
		SessionID: sessionID,
		Title:     title,
		Message:   message,
		Timestamp: time.Now(),
	})
}

// NotifyComplete sends a completion notification.
func (m *Manager) NotifyComplete(ctx context.Context, sessionID, title, message string) error {
	return m.Notify(ctx, &Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:      EventComplete,
		SessionID: sessionID,
		Title:     title,
		Message:   message,
		Timestamp: time.Now(),
	})
}

// NotifyError sends an error notification.
func (m *Manager) NotifyError(ctx context.Context, sessionID, title string, err error) error {
	return m.Notify(ctx, &Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:      EventError,
		SessionID: sessionID,
		Title:     title,
		Message:   err.Error(),
		Timestamp: time.Now(),
	})
}

// Close closes all adapters.
func (m *Manager) Close() error {
	var lastErr error
	for _, adapter := range m.adapters {
		if err := adapter.Close(); err != nil {
			lastErr = err
		}
	}
	if m.publisher != nil {
		if err := m.publisher.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// JSON helpers
func (e *Event) JSON() []byte {
	data, _ := json.Marshal(e)
	return data
}

func ParseEvent(data []byte) (*Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}
