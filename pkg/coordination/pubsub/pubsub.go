package pubsub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	// ErrEmptyTopic is returned when an empty topic is provided
	ErrEmptyTopic = errors.New("topic cannot be empty")

	// ErrNilHandler is returned when a nil handler is provided
	ErrNilHandler = errors.New("handler cannot be nil")

	// ErrSubscriptionNotFound is returned when trying to unsubscribe a non-existent subscription
	ErrSubscriptionNotFound = errors.New("subscription not found")
)

// MessageHandler is a function that processes published messages
type MessageHandler func(msg interface{})

// Subscription represents an active subscription to a topic
type Subscription interface {
	// ID returns the unique identifier for this subscription
	ID() string

	// Topic returns the topic pattern this subscription is for
	Topic() string
}

// PubSub defines the interface for publish/subscribe messaging
type PubSub interface {
	// Publish sends a message to all subscribers of the given topic
	Publish(ctx context.Context, topic string, message interface{}) error

	// Subscribe registers a handler for messages on the given topic pattern
	// Topic patterns support wildcards (*) for flexible matching
	Subscribe(ctx context.Context, topic string, handler MessageHandler) (Subscription, error)

	// Unsubscribe removes a subscription
	Unsubscribe(ctx context.Context, subscription Subscription) error
}

// subscription implements the Subscription interface
type subscription struct {
	id      string
	topic   string
	handler MessageHandler
	buffer  chan interface{}
	ctx     context.Context
	cancel  context.CancelFunc
}

func (s *subscription) ID() string {
	return s.id
}

func (s *subscription) Topic() string {
	return s.topic
}

// InMemoryPubSub is an in-memory implementation of PubSub
// Suitable for local development and testing
type InMemoryPubSub struct {
	mu            sync.RWMutex
	subscriptions map[string]*subscription
	nextID        int
}

// NewInMemoryPubSub creates a new in-memory pub/sub instance
func NewInMemoryPubSub() *InMemoryPubSub {
	return &InMemoryPubSub{
		subscriptions: make(map[string]*subscription),
		nextID:        1,
	}
}

// Publish sends a message to all subscribers whose topic patterns match
func (ps *InMemoryPubSub) Publish(ctx context.Context, topic string, message interface{}) error {
	if topic == "" {
		return ErrEmptyTopic
	}

	ps.mu.RLock()
	defer ps.mu.RUnlock()

	// Find all matching subscriptions
	for _, sub := range ps.subscriptions {
		if matchTopic(sub.topic, topic) {
			// Non-blocking send to buffer
			select {
			case sub.buffer <- message:
				// Message buffered
			case <-ctx.Done():
				return ctx.Err()
			default:
				// Buffer full, skip (could log warning in production)
			}
		}
	}

	return nil
}

// Subscribe creates a new subscription for the given topic pattern
func (ps *InMemoryPubSub) Subscribe(ctx context.Context, topic string, handler MessageHandler) (Subscription, error) {
	if topic == "" {
		return nil, ErrEmptyTopic
	}

	if handler == nil {
		return nil, ErrNilHandler
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Create subscription
	subCtx, cancel := context.WithCancel(ctx)
	sub := &subscription{
		id:      fmt.Sprintf("sub-%d", ps.nextID),
		topic:   topic,
		handler: handler,
		buffer:  make(chan interface{}, 100), // Buffered channel for burst handling
		ctx:     subCtx,
		cancel:  cancel,
	}
	ps.nextID++

	// Store subscription
	ps.subscriptions[sub.id] = sub

	// Start message delivery goroutine
	go ps.deliverMessages(sub)

	return sub, nil
}

// Unsubscribe removes an active subscription
func (ps *InMemoryPubSub) Unsubscribe(ctx context.Context, subscription Subscription) error {
	if subscription == nil {
		return nil
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	sub, exists := ps.subscriptions[subscription.ID()]
	if !exists {
		// Already unsubscribed, not an error
		return nil
	}

	// Cancel the subscription context
	sub.cancel()

	// Remove from map
	delete(ps.subscriptions, subscription.ID())

	return nil
}

// deliverMessages runs in a goroutine and delivers buffered messages to the handler
func (ps *InMemoryPubSub) deliverMessages(sub *subscription) {
	for {
		select {
		case msg := <-sub.buffer:
			// Deliver message to handler
			sub.handler(msg)
		case <-sub.ctx.Done():
			// Subscription canceled, drain remaining messages
			for {
				select {
				case msg := <-sub.buffer:
					sub.handler(msg)
				default:
					return
				}
			}
		}
	}
}

// matchTopic checks if a topic matches a subscription pattern
// Supports wildcard (*) matching for flexible topic patterns
//
// Examples:
//   - "task.progress.*" matches "task.progress.plan1" and "task.progress.plan2"
//   - "task.*.plan1.*" matches "task.progress.plan1.task1"
//   - "agent.*.*" matches "agent.started.agent1"
func matchTopic(pattern, topic string) bool {
	patternParts := strings.Split(pattern, ".")
	topicParts := strings.Split(topic, ".")

	// If pattern has no wildcards, must be exact match
	if !strings.Contains(pattern, "*") {
		return pattern == topic
	}

	// Both must have same number of parts for wildcard matching
	if len(patternParts) != len(topicParts) {
		return false
	}

	// Check each part
	for i := 0; i < len(patternParts); i++ {
		if patternParts[i] == "*" {
			// Wildcard matches anything
			continue
		}

		if patternParts[i] != topicParts[i] {
			// Mismatch
			return false
		}
	}

	return true
}
