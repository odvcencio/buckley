package bus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
)

// MemoryBus is an in-memory implementation of MessageBus for testing.
// It supports wildcards and request/reply but does not persist messages.
type MemoryBus struct {
	mu            sync.RWMutex
	subscriptions map[string][]*memorySubscription
	queues        map[string]*memoryQueue
	closed        atomic.Bool
	subCounter    atomic.Uint64
}

// NewMemoryBus creates a new in-memory message bus.
func NewMemoryBus() *MemoryBus {
	return &MemoryBus{
		subscriptions: make(map[string][]*memorySubscription),
		queues:        make(map[string]*memoryQueue),
	}
}

func (b *MemoryBus) Publish(ctx context.Context, subject string, data []byte) error {
	if b.closed.Load() {
		return ErrClosed
	}

	msg := &Message{
		Subject: subject,
		Data:    data,
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Find matching subscriptions
	for pattern, subs := range b.subscriptions {
		if matchSubject(pattern, subject) {
			for _, sub := range subs {
				if sub.closed.Load() {
					continue
				}
				// Non-blocking send to avoid deadlocks
				select {
				case sub.messages <- msg:
				default:
					// Buffer full, drop message (or could log warning)
				}
			}
		}
	}

	return nil
}

func (b *MemoryBus) Subscribe(ctx context.Context, subject string, handler MessageHandler) (Subscription, error) {
	if b.closed.Load() {
		return nil, ErrClosed
	}

	sub := &memorySubscription{
		id:       fmt.Sprintf("sub-%d", b.subCounter.Add(1)),
		subject:  subject,
		messages: make(chan *Message, 256),
		handler:  handler,
		bus:      b,
	}

	b.mu.Lock()
	b.subscriptions[subject] = append(b.subscriptions[subject], sub)
	b.mu.Unlock()

	// Start message delivery goroutine
	go sub.run(ctx)

	return sub, nil
}

func (b *MemoryBus) QueueSubscribe(ctx context.Context, subject, queue string, handler MessageHandler) (Subscription, error) {
	// For in-memory, queue subscribe is same as regular subscribe
	// (proper load balancing would need more sophisticated implementation)
	return b.Subscribe(ctx, subject, handler)
}

func (b *MemoryBus) Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	if b.closed.Load() {
		return nil, ErrClosed
	}

	replySubject := fmt.Sprintf("_INBOX.%s", ulid.Make().String())
	replyChan := make(chan []byte, 1)

	// Subscribe to reply
	sub, err := b.Subscribe(ctx, replySubject, func(msg *Message) []byte {
		select {
		case replyChan <- msg.Data:
		default:
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe()

	// Publish request with reply subject
	msg := &Message{
		Subject: subject,
		Data:    data,
		ReplyTo: replySubject,
	}

	b.mu.RLock()
	foundResponder := false
	for pattern, subs := range b.subscriptions {
		if matchSubject(pattern, subject) {
			for _, s := range subs {
				if s.closed.Load() {
					continue
				}
				foundResponder = true
				select {
				case s.messages <- msg:
				default:
				}
			}
		}
	}
	b.mu.RUnlock()

	if !foundResponder {
		return nil, ErrNoResponders
	}

	// Wait for reply
	select {
	case reply := <-replyChan:
		return reply, nil
	case <-time.After(timeout):
		return nil, ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *MemoryBus) Queue(name string) TaskQueue {
	b.mu.Lock()
	defer b.mu.Unlock()

	if q, ok := b.queues[name]; ok {
		return q
	}

	q := &memoryQueue{
		name:     name,
		pending:  make(chan *Task, 10000),
		inflight: make(map[string]*Task),
	}
	b.queues[name] = q
	return q
}

func (b *MemoryBus) Close() error {
	if b.closed.Swap(true) {
		return ErrClosed
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Close all subscriptions
	for _, subs := range b.subscriptions {
		for _, sub := range subs {
			sub.closed.Store(true)
			close(sub.messages)
		}
	}

	// Close all queues
	for _, q := range b.queues {
		close(q.pending)
	}

	return nil
}

// memorySubscription implements Subscription for MemoryBus.
type memorySubscription struct {
	id       string
	subject  string
	messages chan *Message
	handler  MessageHandler
	bus      *MemoryBus
	closed   atomic.Bool
}

func (s *memorySubscription) Unsubscribe() error {
	if s.closed.Swap(true) {
		return nil
	}

	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()

	subs := s.bus.subscriptions[s.subject]
	for i, sub := range subs {
		if sub.id == s.id {
			s.bus.subscriptions[s.subject] = append(subs[:i], subs[i+1:]...)
			break
		}
	}

	return nil
}

func (s *memorySubscription) Subject() string {
	return s.subject
}

func (s *memorySubscription) run(ctx context.Context) {
	for {
		select {
		case msg, ok := <-s.messages:
			if !ok {
				return
			}
			reply := s.handler(msg)
			// If handler returned data and there's a reply subject, send response
			if reply != nil && msg.ReplyTo != "" {
				_ = s.bus.Publish(ctx, msg.ReplyTo, reply)
			}
		case <-ctx.Done():
			return
		}
	}
}

// memoryQueue implements TaskQueue for MemoryBus.
type memoryQueue struct {
	name     string
	pending  chan *Task
	mu       sync.Mutex
	inflight map[string]*Task
}

func (q *memoryQueue) Push(ctx context.Context, data []byte) error {
	task := &Task{
		ID:   ulid.Make().String(),
		Data: data,
	}

	select {
	case q.pending <- task:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *memoryQueue) Pull(ctx context.Context) (*Task, error) {
	select {
	case task, ok := <-q.pending:
		if !ok {
			return nil, ErrClosed
		}
		q.mu.Lock()
		q.inflight[task.ID] = task
		q.mu.Unlock()
		return task, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *memoryQueue) Ack(ctx context.Context, taskID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.inflight, taskID)
	return nil
}

func (q *memoryQueue) Nack(ctx context.Context, taskID string) error {
	q.mu.Lock()
	task, ok := q.inflight[taskID]
	if ok {
		delete(q.inflight, taskID)
	}
	q.mu.Unlock()

	if ok {
		select {
		case q.pending <- task:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (q *memoryQueue) Len(ctx context.Context) (int, error) {
	return len(q.pending), nil
}

func (q *memoryQueue) Name() string {
	return q.name
}

// matchSubject checks if a subject matches a pattern with wildcards.
// Supports "*" for single token and ">" for multiple tokens.
func matchSubject(pattern, subject string) bool {
	if pattern == subject {
		return true
	}

	patternParts := strings.Split(pattern, ".")
	subjectParts := strings.Split(subject, ".")

	pi, si := 0, 0
	for pi < len(patternParts) && si < len(subjectParts) {
		switch patternParts[pi] {
		case "*":
			// Matches exactly one token
			pi++
			si++
		case ">":
			// Matches one or more tokens (must be last)
			return true
		default:
			if patternParts[pi] != subjectParts[si] {
				return false
			}
			pi++
			si++
		}
	}

	return pi == len(patternParts) && si == len(subjectParts)
}
