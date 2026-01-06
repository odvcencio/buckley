// Package bus provides a message bus abstraction for agent communication.
// It supports publish/subscribe, request/reply, and task queue patterns.
// The default implementation uses NATS, with an in-memory option for testing.
package bus

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrTimeout is returned when a request times out waiting for a response.
	ErrTimeout = errors.New("request timeout")

	// ErrNoResponders is returned when no subscribers are available to handle a request.
	ErrNoResponders = errors.New("no responders available")

	// ErrClosed is returned when operating on a closed bus or subscription.
	ErrClosed = errors.New("bus or subscription closed")

	// ErrQueueEmpty is returned when pulling from an empty queue with no waiters.
	ErrQueueEmpty = errors.New("queue empty")
)

// MessageBus is the core interface for agent communication.
// Implementations must be safe for concurrent use.
type MessageBus interface {
	// Publish sends a message to all subscribers of the given subject.
	// Returns immediately; does not wait for message delivery.
	Publish(ctx context.Context, subject string, data []byte) error

	// Subscribe registers a handler for messages on the given subject.
	// The handler is called in a separate goroutine for each message.
	// Supports wildcards: "buckley.agent.*" matches "buckley.agent.abc".
	Subscribe(ctx context.Context, subject string, handler MessageHandler) (Subscription, error)

	// Request sends a message and waits for a single response (request/reply pattern).
	// Useful for synchronous agent-to-agent communication.
	Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)

	// QueueSubscribe creates a queue subscription where messages are load-balanced
	// across subscribers in the same queue group.
	QueueSubscribe(ctx context.Context, subject, queue string, handler MessageHandler) (Subscription, error)

	// Queue returns a TaskQueue for the given name, backed by this bus.
	Queue(name string) TaskQueue

	// Close shuts down the bus and all subscriptions.
	Close() error
}

// MessageHandler processes incoming messages.
// For request/reply, return data to send as response; return nil for no response.
type MessageHandler func(msg *Message) []byte

// Message represents an incoming message from the bus.
type Message struct {
	Subject string
	Data    []byte
	ReplyTo string // Set if sender expects a response
}

// Subscription represents an active subscription that can be cancelled.
type Subscription interface {
	// Unsubscribe stops receiving messages and cleans up resources.
	Unsubscribe() error

	// Subject returns the subject pattern this subscription is for.
	Subject() string
}

// TaskQueue provides a persistent work queue for task distribution.
// Tasks are distributed to workers and must be explicitly acknowledged.
type TaskQueue interface {
	// Push adds a task to the queue.
	Push(ctx context.Context, data []byte) error

	// Pull retrieves the next task from the queue.
	// Blocks until a task is available or context is cancelled.
	Pull(ctx context.Context) (*Task, error)

	// Ack acknowledges successful processing of a task.
	Ack(ctx context.Context, taskID string) error

	// Nack negatively acknowledges a task, returning it to the queue for retry.
	Nack(ctx context.Context, taskID string) error

	// Len returns the approximate number of pending tasks.
	Len(ctx context.Context) (int, error)

	// Name returns the queue name.
	Name() string
}

// Task represents a unit of work pulled from a TaskQueue.
type Task struct {
	ID   string
	Data []byte
}

// Config holds configuration for creating a MessageBus.
type Config struct {
	// URL is the NATS server URL (e.g., "nats://localhost:4222").
	// Ignored for in-memory bus.
	URL string

	// Name is a client identifier for debugging/monitoring.
	Name string

	// Timeout is the default timeout for operations.
	Timeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		URL:     "nats://localhost:4222",
		Name:    "buckley",
		Timeout: 30 * time.Second,
	}
}
