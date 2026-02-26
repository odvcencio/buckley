package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GetSettings loads settings for the provided keys.
func (s *Store) GetSettings(keys []string) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	result := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	query := "SELECT key, value FROM settings WHERE key IN (?" + strings.Repeat(",?", len(keys)-1) + ")"
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying settings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scanning setting: %w", err)
		}
		result[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating settings: %w", err)
	}
	return result, nil
}

// SetSetting upserts a setting value. Empty value deletes the row.
func (s *Store) SetSetting(key, value string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
		if err != nil {
			return fmt.Errorf("deleting setting %s: %w", key, err)
		}
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP
	`, key, value)
	if err != nil {
		return fmt.Errorf("setting %s: %w", key, err)
	}
	return nil
}

// RecordAuditLog stores an operator action for later review.
func (s *Store) RecordAuditLog(actor, scope, action string, payload any) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	data := ""
	if payload != nil {
		if buf, err := json.Marshal(payload); err == nil {
			data = string(buf)
		}
	}
	_, err := s.db.Exec(`
		INSERT INTO audit_logs (actor, scope, action, payload, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, strings.TrimSpace(actor), strings.TrimSpace(scope), strings.TrimSpace(action), data, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("recording audit log: %w", err)
	}
	return nil
}

// ListAuditLogs returns recent audit entries.
func (s *Store) ListAuditLogs(limit int) ([]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT actor, scope, action, payload, created_at
		FROM audit_logs
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("querying audit logs: %w", err)
	}
	defer rows.Close()

	var entries []map[string]any
	for rows.Next() {
		var actor, scope, action, payload string
		var created time.Time
		if err := rows.Scan(&actor, &scope, &action, &payload, &created); err != nil {
			return nil, fmt.Errorf("scanning audit log: %w", err)
		}
		var data any
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &data)
		}
		entries = append(entries, map[string]any{
			"actor":     actor,
			"scope":     scope,
			"action":    action,
			"payload":   data,
			"createdAt": created,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating audit logs: %w", err)
	}
	return entries, nil
}
