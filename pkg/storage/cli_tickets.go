package storage

import (
	"database/sql"
	"strings"
	"time"
)

type CLITicket struct {
	ID           string
	SecretHash   string
	Label        string
	Approved     bool
	Principal    string
	Scope        string
	TokenID      string
	SessionToken string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	Consumed     bool
}

func (t *CLITicket) MatchesSecret(secret string) bool {
	if t == nil {
		return false
	}
	return t.SecretHash == hashSecret(secret)
}

func (s *Store) CreateCLITicket(id, secret, label string, expires time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`
        INSERT INTO cli_tickets (id, secret_hash, label, created_at, expires_at)
        VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?)
    `, id, hashSecret(secret), strings.TrimSpace(label), expires.UTC())
	return err
}

func (s *Store) GetCLITicket(id string) (*CLITicket, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	row := s.db.QueryRow(`
		SELECT id, secret_hash, label, approved, principal, scope, token_id, session_token, created_at, expires_at, consumed
		FROM cli_tickets WHERE id = ?
	`, id)
	var ticket CLITicket
	var approved, consumed int
	var principal, scope, tokenID, sessionToken sql.NullString
	if err := row.Scan(&ticket.ID, &ticket.SecretHash, &ticket.Label, &approved, &principal, &scope, &tokenID, &sessionToken, &ticket.CreatedAt, &ticket.ExpiresAt, &consumed); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if principal.Valid {
		ticket.Principal = principal.String
	}
	if scope.Valid {
		ticket.Scope = scope.String
	}
	if tokenID.Valid {
		ticket.TokenID = tokenID.String
	}
	if sessionToken.Valid {
		ticket.SessionToken = sessionToken.String
	}
	ticket.Approved = approved != 0
	ticket.Consumed = consumed != 0
	return &ticket, nil
}

func (s *Store) ApproveCLITicket(id string, principal, scope, tokenID, sessionToken string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`
        UPDATE cli_tickets
        SET approved = 1, principal = ?, scope = ?, token_id = ?, session_token = ?
        WHERE id = ?
    `, strings.TrimSpace(principal), strings.TrimSpace(scope), strings.TrimSpace(tokenID), sessionToken, id)
	return err
}

func (s *Store) ConsumeCLITicket(id string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`
        UPDATE cli_tickets SET consumed = 1, session_token = NULL WHERE id = ?
    `, id)
	return err
}

func (s *Store) CleanupExpiredCLITickets(now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreClosed
	}
	res, err := s.db.Exec(`DELETE FROM cli_tickets WHERE expires_at <= ?`, now.UTC())
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}

func (s *Store) CountPendingCLITickets(now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreClosed
	}
	var count int
	err := s.db.QueryRow(`
        SELECT COUNT(1) FROM cli_tickets
        WHERE approved = 0 AND consumed = 0 AND expires_at > ?
    `, now.UTC()).Scan(&count)
	return count, err
}
