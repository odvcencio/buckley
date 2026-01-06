package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// StreamEvent is the unified event format for SSE streaming.
type StreamEvent struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// handleStream provides a unified SSE stream of all events.
// Clients can filter by event type using query params.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.eventBus == nil {
		writeError(w, http.StatusServiceUnavailable, "event bus not configured")
		return
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ctx := r.Context()
	events := make(chan StreamEvent, 128)

	// Parse filter from query params
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "buckley.>" // All events
	}

	// Subscribe to filtered events
	sub, err := s.eventBus.Subscribe(ctx, filter, func(msg *bus.Message) []byte {
		event := StreamEvent{
			Type:      msg.Subject,
			Timestamp: time.Now(),
		}

		// Try to parse payload
		var payload map[string]any
		if json.Unmarshal(msg.Data, &payload) == nil {
			event.Data = payload
			if id, ok := payload["id"].(string); ok {
				event.ID = id
			} else if id, ok := payload["task_id"].(string); ok {
				event.ID = id
			} else if id, ok := payload["agent_id"].(string); ok {
				event.ID = id
			}
			if t, ok := payload["type"].(string); ok {
				event.Type = t
			}
		}

		select {
		case events <- event:
		default:
			// Drop if channel full
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe: "+err.Error())
		return
	}
	defer sub.Unsubscribe()

	// Send initial connection event
	connectEvent := StreamEvent{
		Type:      "connected",
		Timestamp: time.Now(),
		Data:      map[string]any{"filter": filter},
	}
	data, _ := json.Marshal(connectEvent)
	w.Write([]byte("data: " + string(data) + "\n\n"))
	flusher.Flush()

	// Heartbeat ticker to keep connection alive
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Stream events
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Send heartbeat
			heartbeat := StreamEvent{
				Type:      "heartbeat",
				Timestamp: time.Now(),
			}
			data, _ := json.Marshal(heartbeat)
			_, err := w.Write([]byte("data: " + string(data) + "\n\n"))
			if err != nil {
				return
			}
			flusher.Flush()
		case event := <-events:
			data, _ := json.Marshal(event)
			_, err := w.Write([]byte("data: " + string(data) + "\n\n"))
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// WebSocketMessage represents a message received from WebSocket clients.
type WebSocketMessage struct {
	Type    string         `json:"type"`
	ID      string         `json:"id,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// handleWebSocket provides bidirectional WebSocket communication.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.eventBus == nil {
		writeError(w, http.StatusServiceUnavailable, "event bus not configured")
		return
	}

	// Accept WebSocket connection
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"}, // Configure as needed for security
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to upgrade to WebSocket: "+err.Error())
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "connection closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Parse filter from query params
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "buckley.>" // All events
	}

	events := make(chan StreamEvent, 128)

	// Subscribe to filtered events
	sub, err := s.eventBus.Subscribe(ctx, filter, func(msg *bus.Message) []byte {
		event := StreamEvent{
			Type:      msg.Subject,
			Timestamp: time.Now(),
		}

		var payload map[string]any
		if json.Unmarshal(msg.Data, &payload) == nil {
			event.Data = payload
			if id, ok := payload["id"].(string); ok {
				event.ID = id
			} else if id, ok := payload["task_id"].(string); ok {
				event.ID = id
			} else if id, ok := payload["agent_id"].(string); ok {
				event.ID = id
			}
			if t, ok := payload["type"].(string); ok {
				event.Type = t
			}
		}

		select {
		case events <- event:
		default:
			// Drop if channel full
		}
		return nil
	})
	if err != nil {
		conn.Close(websocket.StatusInternalError, "subscription failed")
		return
	}
	defer sub.Unsubscribe()

	// Send initial connection event
	connectEvent := StreamEvent{
		Type:      "connected",
		Timestamp: time.Now(),
		Data:      map[string]any{"filter": filter, "protocol": "websocket"},
	}
	if err := wsjson.Write(ctx, conn, connectEvent); err != nil {
		return
	}

	// Heartbeat ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Handle incoming messages in a goroutine
	go func() {
		for {
			var msg WebSocketMessage
			err := wsjson.Read(ctx, conn, &msg)
			if err != nil {
				cancel()
				return
			}
			// Handle client messages (e.g., filter changes, commands)
			switch msg.Type {
			case "ping":
				wsjson.Write(ctx, conn, StreamEvent{
					Type:      "pong",
					Timestamp: time.Now(),
				})
			case "filter":
				// Could implement dynamic filter changes here
			}
		}
	}()

	// Stream events to client
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeat := StreamEvent{
				Type:      "heartbeat",
				Timestamp: time.Now(),
			}
			if err := wsjson.Write(ctx, conn, heartbeat); err != nil {
				return
			}
		case event := <-events:
			if err := wsjson.Write(ctx, conn, event); err != nil {
				return
			}
		}
	}
}
