package ipc

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
)

// BusBridge connects the MessageBus to the IPC Hub, enabling browser/CLI
// clients to observe agent communication in real-time.
type BusBridge struct {
	bus  bus.MessageBus
	hub  *Hub
	subs []bus.Subscription
	mu   sync.Mutex
}

// NewBusBridge creates a bridge between MessageBus and IPC Hub.
func NewBusBridge(b bus.MessageBus, h *Hub) *BusBridge {
	return &BusBridge{
		bus: b,
		hub: h,
	}
}

// Start subscribes to MessageBus events and forwards them to the Hub.
func (br *BusBridge) Start(ctx context.Context) error {
	// Subscribe to all agent events
	agentSub, err := br.bus.Subscribe(ctx, "buckley.agent.>", br.forwardToHub("agent"))
	if err != nil {
		return err
	}
	br.mu.Lock()
	br.subs = append(br.subs, agentSub)
	br.mu.Unlock()

	// Subscribe to all task events
	taskSub, err := br.bus.Subscribe(ctx, "buckley.task.>", br.forwardToHub("task"))
	if err != nil {
		return err
	}
	br.mu.Lock()
	br.subs = append(br.subs, taskSub)
	br.mu.Unlock()

	// Subscribe to pool events
	poolSub, err := br.bus.Subscribe(ctx, "buckley.pool.>", br.forwardToHub("pool"))
	if err != nil {
		return err
	}
	br.mu.Lock()
	br.subs = append(br.subs, poolSub)
	br.mu.Unlock()

	// Subscribe to plan events
	planSub, err := br.bus.Subscribe(ctx, "buckley.plan.>", br.forwardToHub("plan"))
	if err != nil {
		return err
	}
	br.mu.Lock()
	br.subs = append(br.subs, planSub)
	br.mu.Unlock()

	// Subscribe to P2P events (for observability)
	p2pSub, err := br.bus.Subscribe(ctx, "buckley.p2p.>", br.forwardToHub("p2p"))
	if err != nil {
		return err
	}
	br.mu.Lock()
	br.subs = append(br.subs, p2pSub)
	br.mu.Unlock()

	return nil
}

// Stop unsubscribes from all MessageBus subjects.
func (br *BusBridge) Stop() {
	br.mu.Lock()
	defer br.mu.Unlock()

	for _, sub := range br.subs {
		sub.Unsubscribe()
	}
	br.subs = nil
}

// forwardToHub returns a MessageHandler that forwards messages to the Hub.
func (br *BusBridge) forwardToHub(category string) bus.MessageHandler {
	return func(msg *bus.Message) []byte {
		// Parse the message data
		var payload map[string]any
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			// Raw data, wrap it
			payload = map[string]any{
				"raw":     string(msg.Data),
				"subject": msg.Subject,
			}
		}

		// Extract type if present, otherwise use subject
		eventType := category
		if t, ok := payload["type"].(string); ok {
			eventType = category + "." + t
		}

		// Extract session ID if present
		sessionID := ""
		if sid, ok := payload["session_id"].(string); ok {
			sessionID = sid
		} else if sid, ok := payload["task_id"].(string); ok {
			sessionID = sid
		}

		// Broadcast to Hub
		br.hub.Broadcast(Event{
			Type:      eventType,
			SessionID: sessionID,
			Payload:   payload,
			Timestamp: time.Now(),
		})

		return nil
	}
}

// PublishHubEvent publishes an IPC Hub event to the MessageBus.
// This enables the reverse flow: browser actions -> agents.
func (br *BusBridge) PublishHubEvent(ctx context.Context, event Event) error {
	data, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}

	subject := "buckley.ipc." + event.Type
	if event.SessionID != "" {
		subject = "buckley.ipc." + event.SessionID + "." + event.Type
	}

	return br.bus.Publish(ctx, subject, data)
}

// BusForwarder implements EventForwarder to forward Hub events to MessageBus.
type BusForwarder struct {
	bus bus.MessageBus
	ctx context.Context
}

// NewBusForwarder creates a forwarder that sends Hub events to MessageBus.
func NewBusForwarder(ctx context.Context, b bus.MessageBus) *BusForwarder {
	return &BusForwarder{
		bus: b,
		ctx: ctx,
	}
}

// BroadcastEvent implements EventForwarder.
func (bf *BusForwarder) BroadcastEvent(event Event) {
	data, err := json.Marshal(map[string]any{
		"type":       event.Type,
		"session_id": event.SessionID,
		"payload":    event.Payload,
		"timestamp":  event.Timestamp,
	})
	if err != nil {
		return
	}

	subject := "buckley.ipc.events"
	if event.SessionID != "" {
		subject = "buckley.ipc." + event.SessionID + ".events"
	}

	_ = bf.bus.Publish(bf.ctx, subject, data)
}
