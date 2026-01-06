package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// APIToken represents an operator-managed API token.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Owner      string     `json:"owner,omitempty"`
	Scope      string     `json:"scope"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	Revoked    bool       `json:"revoked"`
}

const (
	TokenScopeOperator = "operator"
	TokenScopeMember   = "member"
	TokenScopeViewer   = "viewer"
)

// GenerateAPITokenValue creates a random token string suitable for CLI clients.
func GenerateAPITokenValue() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func normalizeScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case TokenScopeOperator:
		return TokenScopeOperator
	case TokenScopeViewer:
		return TokenScopeViewer
	default:
		return TokenScopeMember
	}
}

// CreateAPIToken stores a new API token record, hashing the provided secret.
func (s *Store) CreateAPIToken(name, owner, scope, secret string) (*APIToken, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "token-" + ulid.Make().String()
	}

	now := time.Now().UTC()
	id := strings.ToLower(ulid.Make().String())
	prefix := tokenPrefix(secret)
	hash := hashSecret(secret)
	scope = normalizeScope(scope)

	_, err := s.db.Exec(`
		INSERT INTO api_tokens (id, name, token_hash, token_prefix, created_at, revoked)
		VALUES (?, ?, ?, ?, ?, 0)
	`, id, name, hash, prefix, now)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`
		INSERT INTO api_token_metadata (token_id, owner, scope)
		VALUES (?, ?, ?)
	`, id, strings.TrimSpace(owner), scope); err != nil {
		return nil, err
	}

	return &APIToken{
		ID:        id,
		Name:      name,
		Owner:     strings.TrimSpace(owner),
		Scope:     scope,
		Prefix:    prefix,
		CreatedAt: now,
		Revoked:   false,
	}, nil
}

// ListAPITokens returns active and revoked tokens for operator review.
func (s *Store) ListAPITokens() ([]APIToken, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query(`
		SELECT t.id, t.name, m.owner, m.scope, t.token_prefix, t.created_at, t.last_used_at, t.revoked
		FROM api_tokens t
		LEFT JOIN api_token_metadata m ON m.token_id = t.id
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []APIToken
	for rows.Next() {
		var tok APIToken
		var lastUsed sql.NullTime
		if err := rows.Scan(&tok.ID, &tok.Name, &tok.Owner, &tok.Scope, &tok.Prefix, &tok.CreatedAt, &lastUsed, &tok.Revoked); err != nil {
			return nil, err
		}
		if tok.Scope == "" {
			tok.Scope = TokenScopeMember
		}
		if lastUsed.Valid {
			ts := lastUsed.Time
			tok.LastUsedAt = &ts
		}
		tokens = append(tokens, tok)
	}
	return tokens, rows.Err()
}

// RevokeAPIToken marks the token as revoked.
func (s *Store) RevokeAPIToken(id string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`UPDATE api_tokens SET revoked = 1 WHERE id = ?`, strings.TrimSpace(id))
	return err
}

// ValidateAPIToken verifies a token secret and updates last_used_at.
func (s *Store) ValidateAPIToken(secret string) (*APIToken, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	hash := hashSecret(secret)
	var tok APIToken
	var lastUsed sql.NullTime
	err := s.db.QueryRow(`
		SELECT t.id, t.name, m.owner, m.scope, t.token_prefix, t.created_at, t.last_used_at
		FROM api_tokens t
		LEFT JOIN api_token_metadata m ON m.token_id = t.id
		WHERE token_hash = ? AND revoked = 0
	`, hash).Scan(&tok.ID, &tok.Name, &tok.Owner, &tok.Scope, &tok.Prefix, &tok.CreatedAt, &lastUsed)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if lastUsed.Valid {
		ts := lastUsed.Time
		tok.LastUsedAt = &ts
	}
	tok.Revoked = false
	if err := s.touchAPIToken(tok.ID); err != nil {
		return &tok, err
	}
	if tok.Scope == "" {
		tok.Scope = TokenScopeMember
	}
	return &tok, nil
}

func (s *Store) touchAPIToken(id string) error {
	_, err := s.db.Exec(`UPDATE api_tokens SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func tokenPrefix(secret string) string {
	secret = strings.TrimSpace(secret)
	if len(secret) <= 8 {
		return secret
	}
	return secret[:8]
}

// ExportAPITokens encodes token metadata for backups.
func (s *Store) ExportAPITokens() ([]byte, error) {
	tokens, err := s.ListAPITokens()
	if err != nil {
		return nil, err
	}
	return json.Marshal(tokens)
}
