package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/odvcencio/buckley/pkg/storage"
)

// NATSPublisher publishes approval events to NATS JetStream for distributed processing.
type NATSPublisher struct {
	mu      sync.RWMutex
	nc      *nats.Conn
	js      jetstream.JetStream
	stream  jetstream.Stream
	store   *storage.Store
	config  NATSConfig
	running bool
	done    chan struct{}
}

// NATSConfig holds NATS connection configuration.
type NATSConfig struct {
	URL            string        `yaml:"url" json:"url"`
	StreamName     string        `yaml:"stream_name" json:"stream_name"`
	SubjectPrefix  string        `yaml:"subject_prefix" json:"subject_prefix"`
	ConnectTimeout time.Duration `yaml:"connect_timeout" json:"connect_timeout"`
}

// DefaultNATSConfig returns sensible defaults.
func DefaultNATSConfig() NATSConfig {
	return NATSConfig{
		URL:            "nats://localhost:4222",
		StreamName:     "BUCKLEY_APPROVALS",
		SubjectPrefix:  "buckley.approvals",
		ConnectTimeout: 5 * time.Second,
	}
}

// ApprovalEvent is the event structure published to NATS.
type ApprovalEvent struct {
	Type       string         `json:"type"`
	ApprovalID string         `json:"approval_id"`
	SessionID  string         `json:"session_id"`
	ToolName   string         `json:"tool_name,omitempty"`
	RiskScore  int            `json:"risk_score,omitempty"`
	Status     string         `json:"status,omitempty"`
	DecidedBy  string         `json:"decided_by,omitempty"`
	ExpiresAt  time.Time      `json:"expires_at,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	Data       map[string]any `json:"data,omitempty"`
}

// NewNATSPublisher creates a new NATS publisher for approval events.
func NewNATSPublisher(store *storage.Store, cfg NATSConfig) (*NATSPublisher, error) {
	if cfg.URL == "" {
		cfg = DefaultNATSConfig()
	}
	if cfg.StreamName == "" {
		cfg.StreamName = "BUCKLEY_APPROVALS"
	}
	if cfg.SubjectPrefix == "" {
		cfg.SubjectPrefix = "buckley.approvals"
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}

	return &NATSPublisher{
		store:  store,
		config: cfg,
		done:   make(chan struct{}),
	}, nil
}

// Connect establishes connection to NATS and sets up JetStream.
func (p *NATSPublisher) Connect(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	opts := []nats.Option{
		nats.Name("buckley-approval-publisher"),
		nats.Timeout(p.config.ConnectTimeout),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1), // Infinite reconnects
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Printf("[nats] Disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("[nats] Reconnected to %s", nc.ConnectedUrl())
		}),
	}

	nc, err := nats.Connect(p.config.URL, opts...)
	if err != nil {
		return fmt.Errorf("connect to nats: %w", err)
	}
	p.nc = nc

	// Create JetStream context
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return fmt.Errorf("create jetstream context: %w", err)
	}
	p.js = js

	// Create or get the stream
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        p.config.StreamName,
		Description: "Buckley approval events for distributed processing",
		Subjects:    []string{p.config.SubjectPrefix + ".>"},
		Retention:   jetstream.WorkQueuePolicy,
		MaxAge:      24 * time.Hour, // Keep events for 24 hours max
		MaxMsgs:     100000,
		Storage:     jetstream.FileStorage,
		Replicas:    1,
	})
	if err != nil {
		nc.Close()
		return fmt.Errorf("create stream: %w", err)
	}
	p.stream = stream

	log.Printf("[nats] Connected to %s, stream: %s", p.config.URL, p.config.StreamName)
	return nil
}

// Start begins listening for storage events and publishing to NATS.
func (p *NATSPublisher) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = true
	p.mu.Unlock()

	// Connect if not already connected
	if p.nc == nil {
		if err := p.Connect(ctx); err != nil {
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
			return err
		}
	}

	// Register as observer for storage events
	p.store.AddObserver(storage.ObserverFunc(func(event storage.Event) {
		p.handleStorageEvent(ctx, event)
	}))

	// Monitor for shutdown
	go func() {
		select {
		case <-ctx.Done():
		case <-p.done:
		}
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()

	return nil
}

// Stop stops the publisher and closes the NATS connection.
func (p *NATSPublisher) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		close(p.done)
		p.running = false
	}

	if p.nc != nil {
		p.nc.Drain()
		p.nc.Close()
		p.nc = nil
	}
}

// handleStorageEvent processes storage events and publishes to NATS.
func (p *NATSPublisher) handleStorageEvent(ctx context.Context, event storage.Event) {
	switch event.Type {
	case storage.EventApprovalCreated:
		p.publishApprovalCreated(ctx, event)
	case storage.EventApprovalDecided:
		p.publishApprovalDecided(ctx, event)
	case storage.EventApprovalExpired:
		p.publishApprovalExpired(ctx, event)
	}
}

func (p *NATSPublisher) publishApprovalCreated(ctx context.Context, event storage.Event) {
	data, _ := event.Data.(map[string]any)

	toolName, _ := data["tool_name"].(string)
	riskScore, _ := data["risk_score"].(int)
	expiresAt, _ := data["expires_at"].(time.Time)

	approvalEvent := ApprovalEvent{
		Type:       "approval.created",
		ApprovalID: event.EntityID,
		SessionID:  event.SessionID,
		ToolName:   toolName,
		RiskScore:  riskScore,
		ExpiresAt:  expiresAt,
		Timestamp:  event.Timestamp,
		Data:       data,
	}

	subject := fmt.Sprintf("%s.created.%s", p.config.SubjectPrefix, event.SessionID)
	p.publish(ctx, subject, approvalEvent)
}

func (p *NATSPublisher) publishApprovalDecided(ctx context.Context, event storage.Event) {
	data, _ := event.Data.(map[string]any)

	status, _ := data["status"].(string)
	decidedBy, _ := data["decided_by"].(string)

	approvalEvent := ApprovalEvent{
		Type:       "approval.decided",
		ApprovalID: event.EntityID,
		SessionID:  event.SessionID,
		Status:     status,
		DecidedBy:  decidedBy,
		Timestamp:  event.Timestamp,
		Data:       data,
	}

	subject := fmt.Sprintf("%s.decided.%s", p.config.SubjectPrefix, event.SessionID)
	p.publish(ctx, subject, approvalEvent)
}

func (p *NATSPublisher) publishApprovalExpired(ctx context.Context, event storage.Event) {
	approvalEvent := ApprovalEvent{
		Type:       "approval.expired",
		ApprovalID: event.EntityID,
		SessionID:  event.SessionID,
		Timestamp:  event.Timestamp,
	}

	subject := fmt.Sprintf("%s.expired.%s", p.config.SubjectPrefix, event.SessionID)
	p.publish(ctx, subject, approvalEvent)
}

func (p *NATSPublisher) publish(ctx context.Context, subject string, event ApprovalEvent) {
	p.mu.RLock()
	js := p.js
	p.mu.RUnlock()

	if js == nil {
		log.Printf("[nats] Not connected, skipping publish to %s", subject)
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("[nats] Failed to marshal event: %v", err)
		return
	}

	ack, err := js.Publish(ctx, subject, data)
	if err != nil {
		log.Printf("[nats] Failed to publish to %s: %v", subject, err)
		return
	}

	log.Printf("[nats] Published %s to %s (seq: %d)", event.Type, subject, ack.Sequence)
}

// CreateConsumer creates a durable consumer for processing approval events.
// This can be used by push notification workers in a distributed setup.
func (p *NATSPublisher) CreateConsumer(ctx context.Context, name string, filterSubject string) (jetstream.Consumer, error) {
	p.mu.RLock()
	stream := p.stream
	p.mu.RUnlock()

	if stream == nil {
		return nil, fmt.Errorf("not connected")
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          name,
		Durable:       name,
		FilterSubject: filterSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    3,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer: %w", err)
	}

	return consumer, nil
}

// SubscribeApprovals subscribes to approval events and calls the handler for each.
// This is used by distributed push workers to receive events.
func (p *NATSPublisher) SubscribeApprovals(ctx context.Context, consumerName string, handler func(ApprovalEvent) error) error {
	consumer, err := p.CreateConsumer(ctx, consumerName, p.config.SubjectPrefix+".created.>")
	if err != nil {
		return err
	}

	// Consume messages
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.done:
				return
			default:
			}

			msgs, err := consumer.Fetch(10, jetstream.FetchMaxWait(5*time.Second))
			if err != nil {
				if err != context.DeadlineExceeded && err != nats.ErrTimeout {
					log.Printf("[nats] Fetch error: %v", err)
				}
				continue
			}

			for msg := range msgs.Messages() {
				var event ApprovalEvent
				if err := json.Unmarshal(msg.Data(), &event); err != nil {
					log.Printf("[nats] Failed to unmarshal event: %v", err)
					msg.Nak()
					continue
				}

				if err := handler(event); err != nil {
					log.Printf("[nats] Handler error for %s: %v", event.ApprovalID, err)
					msg.Nak()
					continue
				}

				msg.Ack()
			}
		}
	}()

	return nil
}
