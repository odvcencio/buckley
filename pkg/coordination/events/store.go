package events

import (
	"context"
	"fmt"
	"sync"
)

// InMemoryStore is a simple in-memory event store for testing
type InMemoryStore struct {
	mu          sync.RWMutex
	streams     map[string][]Event
	snapshots   map[string]snapshot
	subscribers map[string][]*inMemorySubscription
	nextSubID   int
}

type snapshot struct {
	Version int64
	State   interface{}
}

// NewInMemoryStore creates a new in-memory event store
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		streams:     make(map[string][]Event),
		snapshots:   make(map[string]snapshot),
		subscribers: make(map[string][]*inMemorySubscription),
	}
}

// Append adds events to a stream
func (s *InMemoryStore) Append(ctx context.Context, streamID string, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Add StreamID and Version to events
	currentLen := len(s.streams[streamID])
	for i := range events {
		events[i].StreamID = streamID
		events[i].Version = int64(currentLen + i + 1)
	}

	s.streams[streamID] = append(s.streams[streamID], events...)

	// Notify subscribers
	subscribers := append([]*inMemorySubscription{}, s.subscribers[streamID]...)
	wildcardSubscribers := append([]*inMemorySubscription{}, s.subscribers["*"]...)
	allSubscribers := append(subscribers, wildcardSubscribers...)

	// Notify subscribers synchronously to ensure delivery before Append returns
	for _, sub := range allSubscribers {
		for _, event := range events {
			// Call handler synchronously to ensure delivery
			sub.handler(ctx, event)
		}
	}

	return nil
}

// Read retrieves events from a stream starting at a version
func (s *InMemoryStore) Read(ctx context.Context, streamID string, fromVersion int64) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	events, exists := s.streams[streamID]
	if !exists {
		return []Event{}, nil
	}

	var result []Event
	for _, e := range events {
		if e.Version > fromVersion {
			result = append(result, e)
		}
	}

	return result, nil
}

// Subscribe to events in a stream
func (s *InMemoryStore) Subscribe(ctx context.Context, streamID string, handler EventHandler) (Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub := &inMemorySubscription{
		id:      s.nextSubID,
		store:   s,
		stream:  streamID,
		handler: handler,
	}
	s.nextSubID++

	s.subscribers[streamID] = append(s.subscribers[streamID], sub)
	return sub, nil
}

// Snapshot saves a state snapshot
func (s *InMemoryStore) Snapshot(ctx context.Context, streamID string, version int64, state interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.snapshots[streamID] = snapshot{
		Version: version,
		State:   state,
	}
	return nil
}

// LoadSnapshot retrieves the latest snapshot
func (s *InMemoryStore) LoadSnapshot(ctx context.Context, streamID string) (interface{}, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap, exists := s.snapshots[streamID]
	if !exists {
		return nil, 0, fmt.Errorf("snapshot not found for stream %s", streamID)
	}

	return snap.State, snap.Version, nil
}

type inMemorySubscription struct {
	id      int
	store   *InMemoryStore
	stream  string
	handler EventHandler
}

func (sub *inMemorySubscription) Unsubscribe() error {
	sub.store.mu.Lock()
	defer sub.store.mu.Unlock()

	subs := sub.store.subscribers[sub.stream]
	for i, s := range subs {
		if s.id == sub.id {
			// Remove from slice
			sub.store.subscribers[sub.stream] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	return nil
}

// noopSubscription is a no-op subscription for stores that don't support real-time subscriptions
type noopSubscription struct{}

func (n *noopSubscription) Unsubscribe() error {
	return nil
}
