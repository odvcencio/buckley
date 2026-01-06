package storage

import (
	"database/sql"
	"strings"
	"time"
)

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
	_, err := s.db.Exec(`
        INSERT INTO web_sessions (id, principal, scope, token_id, expires_at, created_at, last_seen_at)
        VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
    `, id, strings.TrimSpace(principal), strings.TrimSpace(scope), strings.TrimSpace(tokenID), expires.UTC())
	return err
}

func (s *Store) TouchAuthSession(id string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`UPDATE web_sessions SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (s *Store) GetAuthSession(id string) (*AuthSession, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	row := s.db.QueryRow(`SELECT id, principal, scope, token_id, expires_at, created_at FROM web_sessions WHERE id = ?`, id)
	var sess AuthSession
	if err := row.Scan(&sess.ID, &sess.Principal, &sess.Scope, &sess.TokenID, &sess.ExpiresAt, &sess.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sess, nil
}

func (s *Store) DeleteAuthSession(id string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`DELETE FROM web_sessions WHERE id = ?`, id)
	return err
}

func (s *Store) CleanupExpiredAuthSessions(now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreClosed
	}
	res, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at <= ?`, now.UTC())
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}

func (s *Store) CountActiveAuthSessions(now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreClosed
	}
	var count int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM web_sessions WHERE expires_at > ?`, now.UTC()).Scan(&count)
	return count, err
}
