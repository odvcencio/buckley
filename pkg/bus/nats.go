package bus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATSBus implements MessageBus using NATS.
type NATSBus struct {
	conn   *nats.Conn
	js     jetstream.JetStream
	config Config
	mu     sync.RWMutex
	queues map[string]*natsQueue
	closed atomic.Bool
}

// NewNATSBus creates a new NATS-backed message bus.
func NewNATSBus(cfg Config) (*NATSBus, error) {
	if cfg.URL == "" {
		cfg.URL = nats.DefaultURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.Timeout(cfg.Timeout),
		nats.ReconnectWait(time.Second),
		nats.MaxReconnects(-1), // Unlimited reconnects
	}

	conn, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	// Initialize JetStream for persistent queues
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	return &NATSBus{
		conn:   conn,
		js:     js,
		config: cfg,
		queues: make(map[string]*natsQueue),
	}, nil
}

// NewNATSBusFromConn creates a NATSBus from an existing connection.
// Useful for testing with embedded NATS server.
func NewNATSBusFromConn(conn *nats.Conn) (*NATSBus, error) {
	js, err := jetstream.New(conn)
	if err != nil {
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	return &NATSBus{
		conn:   conn,
		js:     js,
		config: DefaultConfig(),
		queues: make(map[string]*natsQueue),
	}, nil
}

func (b *NATSBus) Publish(ctx context.Context, subject string, data []byte) error {
	if b.closed.Load() {
		return ErrClosed
	}
	return b.conn.Publish(subject, data)
}

func (b *NATSBus) Subscribe(ctx context.Context, subject string, handler MessageHandler) (Subscription, error) {
	if b.closed.Load() {
		return nil, ErrClosed
	}

	sub, err := b.conn.Subscribe(subject, func(msg *nats.Msg) {
		m := &Message{
			Subject: msg.Subject,
			Data:    msg.Data,
			ReplyTo: msg.Reply,
		}
		reply := handler(m)
		if reply != nil && msg.Reply != "" {
			_ = msg.Respond(reply)
		}
	})
	if err != nil {
		return nil, err
	}

	return &natsSubscription{sub: sub}, nil
}

func (b *NATSBus) QueueSubscribe(ctx context.Context, subject, queue string, handler MessageHandler) (Subscription, error) {
	if b.closed.Load() {
		return nil, ErrClosed
	}

	sub, err := b.conn.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		m := &Message{
			Subject: msg.Subject,
			Data:    msg.Data,
			ReplyTo: msg.Reply,
		}
		reply := handler(m)
		if reply != nil && msg.Reply != "" {
			_ = msg.Respond(reply)
		}
	})
	if err != nil {
		return nil, err
	}

	return &natsSubscription{sub: sub}, nil
}

func (b *NATSBus) Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	if b.closed.Load() {
		return nil, ErrClosed
	}

	msg, err := b.conn.RequestWithContext(ctx, subject, data)
	if err != nil {
		if err == nats.ErrNoResponders {
			return nil, ErrNoResponders
		}
		if err == nats.ErrTimeout || err == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, err
	}

	return msg.Data, nil
}

func (b *NATSBus) Queue(name string) TaskQueue {
	b.mu.Lock()
	defer b.mu.Unlock()

	if q, ok := b.queues[name]; ok {
		return q
	}

	q := &natsQueue{
		name: name,
		js:   b.js,
	}
	b.queues[name] = q
	return q
}

func (b *NATSBus) Close() error {
	if b.closed.Swap(true) {
		return ErrClosed
	}
	b.conn.Close()
	return nil
}

// Conn returns the underlying NATS connection.
// Useful for advanced operations not exposed by MessageBus.
func (b *NATSBus) Conn() *nats.Conn {
	return b.conn
}

// JetStream returns the JetStream context.
func (b *NATSBus) JetStream() jetstream.JetStream {
	return b.js
}

// natsSubscription wraps a NATS subscription.
type natsSubscription struct {
	sub *nats.Subscription
}

func (s *natsSubscription) Unsubscribe() error {
	return s.sub.Unsubscribe()
}

func (s *natsSubscription) Subject() string {
	return s.sub.Subject
}

// natsQueue implements TaskQueue using JetStream.
type natsQueue struct {
	name     string
	js       jetstream.JetStream
	stream   jetstream.Stream
	consumer jetstream.Consumer
	mu       sync.Mutex
	init     sync.Once
	initErr  error
}

func (q *natsQueue) ensureStream(ctx context.Context) error {
	q.init.Do(func() {
		streamName := fmt.Sprintf("BUCKLEY_QUEUE_%s", q.name)

		// Create or get existing stream
		q.stream, q.initErr = q.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:        streamName,
			Subjects:    []string{fmt.Sprintf("buckley.queue.%s", q.name)},
			Retention:   jetstream.WorkQueuePolicy,
			MaxMsgs:     100000,
			MaxBytes:    1024 * 1024 * 1024, // 1GB
			Discard:     jetstream.DiscardOld,
			MaxAge:      24 * time.Hour,
			Storage:     jetstream.FileStorage,
			Replicas:    1,
			AllowDirect: true,
		})
		if q.initErr != nil {
			return
		}

		// Create durable consumer
		consumerName := fmt.Sprintf("buckley_worker_%s", q.name)
		q.consumer, q.initErr = q.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
			Durable:       consumerName,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       5 * time.Minute,
			MaxDeliver:    5,
			MaxAckPending: 1000,
		})
	})
	return q.initErr
}

func (q *natsQueue) Push(ctx context.Context, data []byte) error {
	if err := q.ensureStream(ctx); err != nil {
		return err
	}

	subject := fmt.Sprintf("buckley.queue.%s", q.name)
	_, err := q.js.Publish(ctx, subject, data)
	return err
}

func (q *natsQueue) Pull(ctx context.Context) (*Task, error) {
	if err := q.ensureStream(ctx); err != nil {
		return nil, err
	}

	msgs, err := q.consumer.Fetch(1, jetstream.FetchMaxWait(30*time.Second))
	if err != nil {
		return nil, err
	}

	for msg := range msgs.Messages() {
		meta, err := msg.Metadata()
		if err != nil {
			continue
		}
		return &Task{
			ID:   fmt.Sprintf("%d:%d", meta.Sequence.Stream, meta.Sequence.Consumer),
			Data: msg.Data(),
		}, nil
	}

	if msgs.Error() != nil {
		return nil, msgs.Error()
	}

	return nil, ErrQueueEmpty
}

func (q *natsQueue) Ack(ctx context.Context, taskID string) error {
	// In JetStream, ack is done on the message itself during Pull
	// This is a simplified implementation; real impl would track messages
	return nil
}

func (q *natsQueue) Nack(ctx context.Context, taskID string) error {
	// Similarly, nack/nak is done on the message
	return nil
}

func (q *natsQueue) Len(ctx context.Context) (int, error) {
	if err := q.ensureStream(ctx); err != nil {
		return 0, err
	}

	info, err := q.stream.Info(ctx)
	if err != nil {
		return 0, err
	}
	return int(info.State.Msgs), nil
}

func (q *natsQueue) Name() string {
	return q.name
}
