package ipc

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// Event represents a message sent to WebSocket clients.
type Event struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionId,omitempty"`
	Payload   any       `json:"payload,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// EventForwarder receives events from the hub.
type EventForwarder interface {
	BroadcastEvent(event Event)
}

// Hub fan-outs events to connected WebSocket clients and gRPC subscribers.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]struct{}
	forwarders []EventForwarder
}

// NewHub creates a Hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*client]struct{}),
		forwarders: nil,
	}
}

// AddForwarder registers an EventForwarder to receive all events.
func (h *Hub) AddForwarder(f EventForwarder) {
	h.mu.Lock()
	h.forwarders = append(h.forwarders, f)
	h.mu.Unlock()
}

// Broadcast sends an event to all clients, dropping slow consumers.
func (h *Hub) Broadcast(event Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Forward to WebSocket clients
	for c := range h.clients {
		if !c.enqueue(event) {
			go h.removeClient(c)
		}
	}

	// Forward to gRPC/other subscribers
	for _, f := range h.forwarders {
		f.BroadcastEvent(event)
	}
}

// register adds a new client to the hub.
func (h *Hub) register(conn wsConn, filter func(Event) bool) *client {
	c := &client{
		conn:   conn,
		send:   make(chan Event, 64),
		filter: filter,
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

// removeClient disconnects and removes a client.
func (h *Hub) removeClient(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

type wsConn interface {
	Write(ctx context.Context, msgType websocket.MessageType, data []byte) error
	Close(status websocket.StatusCode, reason string) error
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
}

type client struct {
	conn   wsConn
	send   chan Event
	filter func(Event) bool
}

func (c *client) enqueue(event Event) bool {
	if c.filter != nil && !c.filter(event) {
		return true
	}
	select {
	case c.send <- event:
		return true
	default:
		return false
	}
}

func (c *client) writeLoop(ctx context.Context) error {
	for {
		select {
		case event, ok := <-c.send:
			if !ok {
				return nil
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = c.conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *client) close(status websocket.StatusCode, reason string) {
	_ = c.conn.Close(status, reason)
}
