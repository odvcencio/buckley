package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const maxStoredIPCEventsPerSession = 5000

// IPCEvent is a durable event-stream entry used to resume disconnected clients.
type IPCEvent struct {
	ID        string
	SessionID string
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// SaveIPCEvent appends an event and bounds retained history per session.
func (s *Store) SaveIPCEvent(event IPCEvent) error {
	event.ID = strings.TrimSpace(event.ID)
	event.Type = strings.TrimSpace(event.Type)
	if event.ID == "" || event.Type == "" {
		return fmt.Errorf("event id and type required")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO ipc_events (event_id, session_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		event.ID, event.SessionID, event.Type, []byte(event.Payload), event.CreatedAt); err != nil {
		return fmt.Errorf("save ipc event: %w", err)
	}
	_, err := s.db.Exec(`DELETE FROM ipc_events
		WHERE session_id = ? AND event_id IN (
			SELECT event_id FROM ipc_events WHERE session_id = ? ORDER BY event_id DESC LIMIT -1 OFFSET ?
		)`, event.SessionID, event.SessionID, maxStoredIPCEventsPerSession)
	if err != nil {
		return fmt.Errorf("prune ipc events: %w", err)
	}
	return nil
}

// ListIPCEventsAfter returns ordered session events newer than an event ID.
func (s *Store) ListIPCEventsAfter(sessionID, afterID string, limit int) ([]IPCEvent, error) {
	sessionID = strings.TrimSpace(sessionID)
	afterID = strings.TrimSpace(afterID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	if limit <= 0 || limit > maxStoredIPCEventsPerSession {
		limit = maxStoredIPCEventsPerSession
	}
	rows, err := s.db.Query(`SELECT event_id, session_id, event_type, payload_json, created_at
		FROM ipc_events WHERE session_id = ? AND event_id > ? ORDER BY event_id ASC LIMIT ?`, sessionID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list ipc events: %w", err)
	}
	defer rows.Close()

	events := make([]IPCEvent, 0)
	for rows.Next() {
		var event IPCEvent
		var payload []byte
		if err := rows.Scan(&event.ID, &event.SessionID, &event.Type, &payload, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ipc event: %w", err)
		}
		event.Payload = append(json.RawMessage(nil), payload...)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ipc events: %w", err)
	}
	return events, nil
}
