package storage

import (
	"fmt"
	"time"
)

// EventType represents the type of storage event emitted.
type EventType string

// Storage event type constants.
const (
	EventSessionCreated EventType = "session.created"
	EventSessionUpdated EventType = "session.updated"
	EventSessionDeleted EventType = "session.deleted"

	EventMessageCreated EventType = "message.created"

	EventTodoCreated EventType = "todo.created"
	EventTodoUpdated EventType = "todo.updated"
	EventTodoDeleted EventType = "todo.deleted"
	EventTodoCleared EventType = "todo.cleared"

	EventSkillActivated   EventType = "skill.activated"
	EventSkillDeactivated EventType = "skill.deactivated"

	EventApprovalCreated EventType = "approval.created"
	EventApprovalDecided EventType = "approval.decided"
	EventApprovalExpired EventType = "approval.expired"
)

// Event represents a change inside the storage layer that other subsystems can react to.
type Event struct {
	Type      EventType `json:"type"`
	SessionID string    `json:"sessionId,omitempty"`
	EntityID  string    `json:"entityId,omitempty"`
	Data      any       `json:"data,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Observer reacts to storage events.
//
//go:generate mockgen -package=storage -destination=mock_observer_test.go github.com/odvcencio/buckley/pkg/storage Observer
type Observer interface {
	HandleStorageEvent(Event)
}

// ObserverFunc is a helper to turn a function into an Observer.
type ObserverFunc func(Event)

// HandleStorageEvent implements the Observer interface.
func (f ObserverFunc) HandleStorageEvent(e Event) {
	f(e)
}

// newEvent is a helper to build a storage event.
func newEvent(eventType EventType, sessionID string, entityID any, data any) Event {
	entity := ""
	if entityID != nil {
		entity = fmt.Sprintf("%v", entityID)
	}
	return Event{
		Type:      eventType,
		SessionID: sessionID,
		EntityID:  entity,
		Data:      data,
		Timestamp: time.Now(),
	}
}
