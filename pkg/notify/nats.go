package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSPublisher publishes events to NATS.
type NATSPublisher struct {
	conn    *nats.Conn
	subject string
}

// NATSConfig configures the NATS connection.
type NATSConfig struct {
	// URL is the NATS server URL
	URL string

	// Subject is the base subject for events
	Subject string

	// ConnectTimeout is the connection timeout
	ConnectTimeout time.Duration
}

// NewNATSPublisher creates a NATS publisher.
func NewNATSPublisher(cfg NATSConfig) (*NATSPublisher, error) {
	if cfg.URL == "" {
		cfg.URL = nats.DefaultURL
	}
	if cfg.Subject == "" {
		cfg.Subject = "buckley.notify"
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}

	conn, err := nats.Connect(cfg.URL,
		nats.Timeout(cfg.ConnectTimeout),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}

	return &NATSPublisher{
		conn:    conn,
		subject: cfg.Subject,
	}, nil
}

// Publish publishes an event to NATS.
func (p *NATSPublisher) Publish(ctx context.Context, event *Event) error {
	subject := fmt.Sprintf("%s.%s", p.subject, event.Type)

	// If event needs a reply, create an inbox
	if event.ReplyTo == "" {
		event.ReplyTo = p.conn.NewRespInbox()
	}

	return p.conn.Publish(subject, event.JSON())
}

// Close closes the NATS connection.
func (p *NATSPublisher) Close() error {
	p.conn.Close()
	return nil
}

// NATSSubscriber subscribes to events from NATS.
type NATSSubscriber struct {
	conn    *nats.Conn
	subject string
	sub     *nats.Subscription
}

// NewNATSSubscriber creates a NATS subscriber.
func NewNATSSubscriber(cfg NATSConfig) (*NATSSubscriber, error) {
	if cfg.URL == "" {
		cfg.URL = nats.DefaultURL
	}
	if cfg.Subject == "" {
		cfg.Subject = "buckley.notify"
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}

	conn, err := nats.Connect(cfg.URL,
		nats.Timeout(cfg.ConnectTimeout),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}

	return &NATSSubscriber{
		conn:    conn,
		subject: cfg.Subject,
	}, nil
}

// Subscribe starts receiving events.
func (s *NATSSubscriber) Subscribe(ctx context.Context, handler func(*Event)) error {
	subject := s.subject + ".>"

	sub, err := s.conn.Subscribe(subject, func(msg *nats.Msg) {
		event, err := ParseEvent(msg.Data)
		if err != nil {
			return
		}
		handler(event)
	})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	s.sub = sub

	// Wait for context cancellation
	<-ctx.Done()

	return nil
}

// Close closes the subscription and connection.
func (s *NATSSubscriber) Close() error {
	if s.sub != nil {
		s.sub.Unsubscribe()
	}
	s.conn.Close()
	return nil
}

// RequestResponse sends a request and waits for a response.
func RequestResponse(conn *nats.Conn, event *Event, timeout time.Duration) (*Response, error) {
	subject := fmt.Sprintf("buckley.notify.%s", event.Type)

	msg, err := conn.Request(subject, event.JSON(), timeout)
	if err != nil {
		return nil, err
	}

	var resp Response
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
