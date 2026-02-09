package storage

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// PushSubscription represents a Web Push subscription.
type PushSubscription struct {
	ID        string    `json:"id"`
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	Principal string    `json:"principal"`
	UserAgent string    `json:"userAgent,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// SavePushSubscription creates or updates a push subscription.
func (s *Store) SavePushSubscription(sub *PushSubscription) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}

	_, err := s.db.Exec(`
		INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, principal, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET
			p256dh = excluded.p256dh,
			auth = excluded.auth,
			principal = excluded.principal,
			user_agent = excluded.user_agent
	`, sub.ID, sub.Endpoint, sub.P256dh, sub.Auth, sub.Principal, sub.UserAgent, sub.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("saving push subscription: %w", err)
	}
	return nil
}

// GetPushSubscription retrieves a subscription by ID.
func (s *Store) GetPushSubscription(id string) (*PushSubscription, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}

	row := s.db.QueryRow(`
		SELECT id, endpoint, p256dh, auth, principal, user_agent, created_at
		FROM push_subscriptions WHERE id = ?
	`, id)

	var sub PushSubscription
	var userAgent sql.NullString
	err := row.Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Principal, &userAgent, &sub.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning push subscription: %w", err)
	}

	if userAgent.Valid {
		sub.UserAgent = userAgent.String
	}

	return &sub, nil
}

// GetPushSubscriptionByEndpoint retrieves a subscription by endpoint.
func (s *Store) GetPushSubscriptionByEndpoint(endpoint string) (*PushSubscription, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}

	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}

	row := s.db.QueryRow(`
		SELECT id, endpoint, p256dh, auth, principal, user_agent, created_at
		FROM push_subscriptions WHERE endpoint = ?
	`, endpoint)

	var sub PushSubscription
	var userAgent sql.NullString
	err := row.Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Principal, &userAgent, &sub.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning push subscription by endpoint: %w", err)
	}

	if userAgent.Valid {
		sub.UserAgent = userAgent.String
	}

	return &sub, nil
}

// GetPushSubscriptionsByPrincipal retrieves all subscriptions for a principal.
func (s *Store) GetPushSubscriptionsByPrincipal(principal string) ([]*PushSubscription, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}

	rows, err := s.db.Query(`
		SELECT id, endpoint, p256dh, auth, principal, user_agent, created_at
		FROM push_subscriptions WHERE principal = ?
	`, principal)
	if err != nil {
		return nil, fmt.Errorf("querying push subscriptions by principal: %w", err)
	}
	defer rows.Close()

	var subs []*PushSubscription
	for rows.Next() {
		var sub PushSubscription
		var userAgent sql.NullString
		if err := rows.Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Principal, &userAgent, &sub.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning push subscription: %w", err)
		}
		if userAgent.Valid {
			sub.UserAgent = userAgent.String
		}
		subs = append(subs, &sub)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating push subscriptions: %w", err)
	}
	return subs, nil
}

// DeletePushSubscription removes a subscription by ID.
func (s *Store) DeletePushSubscription(id string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}

	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting push subscription: %w", err)
	}
	return nil
}

// DeletePushSubscriptionByEndpoint removes a subscription by endpoint.
func (s *Store) DeletePushSubscriptionByEndpoint(endpoint string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}

	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	if err != nil {
		return fmt.Errorf("deleting push subscription by endpoint: %w", err)
	}
	return nil
}

// ListPushSubscriptions retrieves all push subscriptions.
func (s *Store) ListPushSubscriptions() ([]*PushSubscription, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}

	rows, err := s.db.Query(`
		SELECT id, endpoint, p256dh, auth, principal, user_agent, created_at
		FROM push_subscriptions
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing push subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []*PushSubscription
	for rows.Next() {
		var sub PushSubscription
		var userAgent sql.NullString
		if err := rows.Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Principal, &userAgent, &sub.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning push subscription: %w", err)
		}
		if userAgent.Valid {
			sub.UserAgent = userAgent.String
		}
		subs = append(subs, &sub)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating push subscriptions: %w", err)
	}
	return subs, nil
}

// VAPIDKeys represents the VAPID key pair for Web Push.
type VAPIDKeys struct {
	PublicKey  string    `json:"publicKey"`
	PrivateKey string    `json:"privateKey"`
	CreatedAt  time.Time `json:"createdAt"`
}

// GetVAPIDKeys retrieves the VAPID keys.
func (s *Store) GetVAPIDKeys() (*VAPIDKeys, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}

	row := s.db.QueryRow(`SELECT public_key, private_key, created_at FROM vapid_keys WHERE id = 1`)

	var keys VAPIDKeys
	err := row.Scan(&keys.PublicKey, &keys.PrivateKey, &keys.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning vapid keys: %w", err)
	}

	return &keys, nil
}

// SaveVAPIDKeys saves the VAPID keys (single row, replaces if exists).
func (s *Store) SaveVAPIDKeys(publicKey, privateKey string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}

	_, err := s.db.Exec(`
		INSERT INTO vapid_keys (id, public_key, private_key, created_at)
		VALUES (1, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			public_key = excluded.public_key,
			private_key = excluded.private_key,
			created_at = excluded.created_at
	`, publicKey, privateKey)
	if err != nil {
		return fmt.Errorf("saving vapid keys: %w", err)
	}
	return nil
}

// CreatePushSubscription creates a new push subscription and returns the ID.
func (s *Store) CreatePushSubscription(principal, endpoint, p256dh, auth, userAgent string) (string, error) {
	if s == nil || s.db == nil {
		return "", ErrStoreClosed
	}

	id := generateID()
	sub := &PushSubscription{
		ID:        id,
		Principal: principal,
		Endpoint:  endpoint,
		P256dh:    p256dh,
		Auth:      auth,
		UserAgent: userAgent,
		CreatedAt: time.Now(),
	}

	if err := s.SavePushSubscription(sub); err != nil {
		return "", fmt.Errorf("creating push subscription: %w", err)
	}

	return id, nil
}

// GetVAPIDPublicKey returns only the public key for sharing with clients.
func (s *Store) GetVAPIDPublicKey() (string, error) {
	keys, err := s.GetVAPIDKeys()
	if err != nil {
		return "", fmt.Errorf("getting vapid public key: %w", err)
	}
	if keys == nil {
		return "", nil
	}
	return keys.PublicKey, nil
}

// generateID generates a unique ID for push subscriptions
func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if crypto/rand fails
		return time.Now().Format("20060102150405.000000000")
	}
	return fmt.Sprintf("%x", b)
}
