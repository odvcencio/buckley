package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/coordination/events"
	"github.com/gorilla/websocket"
)

// SubscribeMessage represents a subscription request from a client
type SubscribeMessage struct {
	Action     string   `json:"action"` // "subscribe" or "unsubscribe"
	EventTypes []string `json:"event_types,omitempty"`
}

// EventMessage represents an event sent to clients
type EventMessage struct {
	Type      string          `json:"type"`
	StreamID  string          `json:"stream_id"`
	Version   int64           `json:"version"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// EventStream manages ACP observability streaming over Buckley-internal coordination events.
type EventStream struct {
	eventStore events.EventStore // Buckley coordination event store (distinct from the ACP protocol).
	logger     *Logger

	mu          sync.RWMutex
	subscribers map[*subscriber]bool
	upgrader    websocket.Upgrader
	auth        func(*http.Request) error
}

type subscriber struct {
	conn       *websocket.Conn
	eventTypes map[string]bool // Filter for specific event types
	subscribed bool            // Whether actively subscribed
	send       chan EventMessage
	mu         sync.RWMutex
	writeMu    sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewEventStream creates a new event stream
func NewEventStream(eventStore events.EventStore) *EventStream {
	return NewEventStreamWithAuth(eventStore, nil)
}

// NewEventStreamWithAuth allows callers to enforce authentication on WebSocket upgrades.
func NewEventStreamWithAuth(eventStore events.EventStore, auth func(*http.Request) error) *EventStream {
	stream := &EventStream{
		eventStore:  eventStore,
		logger:      NewLogger("event_stream", slog.LevelInfo),
		subscribers: make(map[*subscriber]bool),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for now
			},
		},
		auth: auth,
	}

	// Start event broadcaster
	go stream.broadcastEvents()

	return stream
}

// HandleWebSocket handles WebSocket connections
func (s *EventStream) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		if err := s.auth(r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("Failed to upgrade WebSocket connection", slog.String("error", err.Error()))
		return
	}

	// Use background context instead of request context (which gets cancelled after upgrade)
	ctx, cancel := context.WithCancel(context.Background())
	sub := &subscriber{
		conn:       conn,
		eventTypes: make(map[string]bool),
		send:       make(chan EventMessage, 100), // Buffer for backpressure
		ctx:        ctx,
		cancel:     cancel,
	}

	s.mu.Lock()
	s.subscribers[sub] = true
	s.mu.Unlock()

	s.logger.Info("WebSocket connection established",
		slog.String("remote_addr", r.RemoteAddr),
	)

	// Record metric
	ActiveEventStreamConnections.Inc()

	// Start goroutines for reading and writing
	go sub.writePump()
	go s.readPump(sub)
}

// readPump handles incoming messages from the client
func (s *EventStream) readPump(sub *subscriber) {
	defer func() {
		s.removeSubscriber(sub)
		sub.writeMu.Lock()
		sub.conn.Close()
		sub.writeMu.Unlock()
		ActiveEventStreamConnections.Dec()
	}()

	sub.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	sub.conn.SetPongHandler(func(string) error {
		sub.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		var msg SubscribeMessage
		err := sub.conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.logger.Error("WebSocket read error", slog.String("error", err.Error()))
			}
			// Send error message to client
			errorMsg := map[string]string{"error": "Invalid message format"}
			sub.writeMu.Lock()
			_ = sub.conn.WriteJSON(errorMsg)
			sub.writeMu.Unlock()
			return
		}
		switch msg.Action {
		case "subscribe":
			sub.mu.Lock()
			sub.subscribed = true
			for _, eventType := range msg.EventTypes {
				sub.eventTypes[eventType] = true
			}
			sub.mu.Unlock()
			s.logger.Debug("Client subscribed to events",
				slog.Any("event_types", msg.EventTypes),
			)

		case "unsubscribe":
			sub.mu.Lock()
			sub.subscribed = false
			sub.eventTypes = make(map[string]bool)
			sub.mu.Unlock()
			s.logger.Debug("Client unsubscribed from all events")

		default:
			s.logger.Warn("Unknown action", slog.String("action", msg.Action))
		}
	}
}

// writePump sends events to the client
func (sub *subscriber) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		sub.cancel()
	}()

	for {
		select {
		case event, ok := <-sub.send:
			if !ok {
				sub.writeMu.Lock()
				_ = sub.conn.WriteMessage(websocket.CloseMessage, []byte{})
				sub.writeMu.Unlock()
				return
			}

			sub.writeMu.Lock()
			sub.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := sub.conn.WriteJSON(event)
			sub.writeMu.Unlock()
			if err != nil {
				return
			}

			EventStreamMessagesSent.Inc()

		case <-ticker.C:
			sub.writeMu.Lock()
			sub.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := sub.conn.WriteMessage(websocket.PingMessage, nil)
			sub.writeMu.Unlock()
			if err != nil {
				return
			}

		case <-sub.ctx.Done():
			return
		}
	}
}

// broadcastEvents subscribes to event store and broadcasts to all subscribers
func (s *EventStream) broadcastEvents() {
	// Subscribe to all streams using the event handler pattern
	subscription, err := s.eventStore.Subscribe(context.Background(), "*", func(ctx context.Context, event events.Event) error {
		s.logger.Debug("Broadcasting event",
			slog.String("type", event.Type),
			slog.String("stream_id", event.StreamID),
		)

		// Convert event to message format
		data, err := json.Marshal(event.Data)
		if err != nil {
			s.logger.Error("Failed to marshal event data",
				slog.String("error", err.Error()),
				slog.String("event_type", event.Type),
			)
			return err
		}

		msg := EventMessage{
			Type:      event.Type,
			StreamID:  event.StreamID,
			Version:   event.Version,
			Timestamp: event.Timestamp,
			Data:      data,
		}

		// Send to all interested subscribers
		s.mu.RLock()
		for sub := range s.subscribers {
			sub.mu.RLock()
			subscribed := sub.subscribed
			// Interested if subscribed AND (subscribed to all OR subscribed to this specific type)
			interested := subscribed && (len(sub.eventTypes) == 0 || sub.eventTypes[event.Type])
			sub.mu.RUnlock()

			if interested {
				select {
				case sub.send <- msg:
					// Event sent successfully
				default:
					// Channel full, skip (backpressure)
					s.logger.Warn("Event stream backpressure, dropping event",
						slog.String("event_type", event.Type),
					)
					EventStreamBackpressureDrops.Inc()
				}
			}
		}
		s.mu.RUnlock()

		EventStreamEventsBroadcast.Inc()
		return nil
	})

	if err != nil {
		s.logger.Error("Failed to subscribe to event store", slog.String("error", err.Error()))
		return
	}
	defer subscription.Unsubscribe()

	// Keep the subscription alive
	select {}
}

// removeSubscriber removes a subscriber from the list
func (s *EventStream) removeSubscriber(sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.subscribers[sub] {
		delete(s.subscribers, sub)
		close(sub.send)
		s.logger.Info("WebSocket connection closed")
	}
}

// ActiveConnections returns the number of active WebSocket connections
func (s *EventStream) ActiveConnections() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subscribers)
}

// Shutdown gracefully closes all connections
func (s *EventStream) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for sub := range s.subscribers {
		sub.cancel()
		sub.conn.Close()
		close(sub.send)
	}
	s.subscribers = make(map[*subscriber]bool)
}
