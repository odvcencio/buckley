// Package events provides Buckley-internal coordination event storage.
// It is not part of the ACP protocol surface.
package events

import (
	"context"
	"time"
)

// Event represents a single event in the event stream
type Event struct {
	StreamID  string
	Type      string
	Version   int64
	Data      map[string]interface{}
	Metadata  map[string]string
	Timestamp time.Time
}

// EventHandler processes events
type EventHandler func(ctx context.Context, event Event) error

// Subscription represents an active event subscription
type Subscription interface {
	Unsubscribe() error
}

// EventStore defines the interface for event storage
type EventStore interface {
	// Append adds events to a stream
	Append(ctx context.Context, streamID string, events []Event) error

	// Read retrieves events from a stream starting at a version
	Read(ctx context.Context, streamID string, fromVersion int64) ([]Event, error)

	// Subscribe to events in a stream
	Subscribe(ctx context.Context, streamID string, handler EventHandler) (Subscription, error)

	// Snapshot saves a state snapshot
	Snapshot(ctx context.Context, streamID string, version int64, state interface{}) error

	// LoadSnapshot retrieves the latest snapshot
	LoadSnapshot(ctx context.Context, streamID string) (state interface{}, version int64, err error)
}
