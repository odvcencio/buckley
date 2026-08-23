package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrAuthSessionSourceTokenInactive = errors.New("storage: auth session source token inactive")

type AuthSession struct {
	ID        string
	Principal string
	Scope     string
	TokenID   string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (s *Store) CreateAuthSession(id, principal, scope, tokenID string, expires time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	principal = strings.TrimSpace(principal)
	scope = strings.TrimSpace(scope)
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		_, err := s.db.Exec(`
			INSERT INTO web_sessions (id, principal, scope, token_id, expires_at, created_at, last_seen_at)
			VALUES (?, ?, ?, '', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		`, id, principal, scope, expires.UTC())
		if err != nil {
			return fmt.Errorf("creating auth session: %w", err)
		}
		return nil
	}

	res, err := s.db.Exec(`
		INSERT INTO web_sessions (id, principal, scope, token_id, expires_at, created_at, last_seen_at)
		SELECT ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		FROM api_tokens
		WHERE id = ? AND revoked = 0
	`, id, principal, scope, tokenID, expires.UTC(), tokenID)
	if err != nil {
		return fmt.Errorf("creating auth session: %w", err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking auth session creation: %w", err)
	}
	if created != 1 {
		return ErrAuthSessionSourceTokenInactive
	}
	return nil
}

func (s *Store) TouchAuthSession(id string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`UPDATE web_sessions SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("touching auth session: %w", err)
	}
	return nil
}

func (s *Store) GetAuthSession(id string) (*AuthSession, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	row := s.db.QueryRow(`
		SELECT ws.id, ws.principal, ws.scope, COALESCE(ws.token_id, ''), ws.expires_at, ws.created_at
		FROM web_sessions ws
		LEFT JOIN api_tokens source ON source.id = ws.token_id
		WHERE ws.id = ?
		  AND (
			COALESCE(ws.token_id, '') = ''
			OR (source.id IS NOT NULL AND source.revoked = 0)
		  )
	`, id)
	var sess AuthSession
	if err := row.Scan(&sess.ID, &sess.Principal, &sess.Scope, &sess.TokenID, &sess.ExpiresAt, &sess.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning auth session: %w", err)
	}
	return &sess, nil
}

func (s *Store) DeleteAuthSession(id string) error {
	return s.DeleteAuthSessions([]string{id})
}

func (s *Store) DeleteAuthSessions(ids []string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	if len(ids) > 2 {
		return fmt.Errorf("deleting auth sessions: too many session ids")
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 256 {
			return fmt.Errorf("deleting auth sessions: invalid session id")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning auth session deletion: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, id := range unique {
		if _, err := tx.Exec(`DELETE FROM web_sessions WHERE id = ?`, id); err != nil {
			return fmt.Errorf("deleting auth session: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing auth session deletion: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) CleanupExpiredAuthSessions(now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreClosed
	}
	res, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at <= ?`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("cleaning up expired auth sessions: %w", err)
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}

func (s *Store) CountActiveAuthSessions(now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreClosed
	}
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(1)
		FROM web_sessions ws
		LEFT JOIN api_tokens source ON source.id = ws.token_id
		WHERE ws.expires_at > ?
		  AND (
			COALESCE(ws.token_id, '') = ''
			OR (source.id IS NOT NULL AND source.revoked = 0)
		  )
	`, now.UTC()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting active auth sessions: %w", err)
	}
	return count, nil
}
