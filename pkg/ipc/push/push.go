// Package push provides Web Push notification functionality for the Buckley PWA.
package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Subscription represents a push subscription from a browser.
type Subscription struct {
	ID        string    `json:"id"`
	Endpoint  string    `json:"endpoint"`
	Keys      Keys      `json:"keys"`
	Principal string    `json:"principal"`
	CreatedAt time.Time `json:"createdAt"`
	UserAgent string    `json:"userAgent,omitempty"`
}

// Keys contains the subscription encryption keys.
type Keys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// Notification represents a push notification payload.
type Notification struct {
	Title   string               `json:"title"`
	Body    string               `json:"body"`
	Icon    string               `json:"icon,omitempty"`
	Badge   string               `json:"badge,omitempty"`
	Tag     string               `json:"tag,omitempty"`
	Data    map[string]any       `json:"data,omitempty"`
	Actions []NotificationAction `json:"actions,omitempty"`
}

// NotificationAction represents an action button on a notification.
type NotificationAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	Icon   string `json:"icon,omitempty"`
}

// Store defines the interface for persisting push subscriptions.
type Store interface {
	SaveSubscription(sub *Subscription) error
	GetSubscription(id string) (*Subscription, error)
	GetSubscriptionsByPrincipal(principal string) ([]*Subscription, error)
	DeleteSubscription(id string) error
	ListSubscriptions() ([]*Subscription, error)
}

// Service manages push notifications.
type Service struct {
	mu sync.RWMutex

	store        Store
	vapidPublic  string
	vapidPrivate string
	subject      string // mailto: or https: URL for VAPID
}

// Config configures the push notification service.
type Config struct {
	Store        Store
	VAPIDPublic  string // Base64 encoded public key
	VAPIDPrivate string // Base64 encoded private key
	Subject      string // mailto: or https: URL
}

// NewService creates a new push notification service.
func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("store required")
	}

	// If no VAPID keys provided, generate new ones
	vapidPublic := cfg.VAPIDPublic
	vapidPrivate := cfg.VAPIDPrivate

	if vapidPublic == "" || vapidPrivate == "" {
		pub, priv, err := GenerateVAPIDKeys()
		if err != nil {
			return nil, fmt.Errorf("failed to generate VAPID keys: %w", err)
		}
		vapidPublic = pub
		vapidPrivate = priv
	}

	subject := cfg.Subject
	if subject == "" {
		return nil, fmt.Errorf("push subject required: set BUCKLEY_PUSH_SUBJECT env var (e.g., mailto:admin@example.com)")
	}

	return &Service{
		store:        cfg.Store,
		vapidPublic:  vapidPublic,
		vapidPrivate: vapidPrivate,
		subject:      subject,
	}, nil
}

// VAPIDPublicKey returns the VAPID public key for client subscriptions.
func (s *Service) VAPIDPublicKey() string {
	return s.vapidPublic
}

// Subscribe registers a new push subscription.
func (s *Service) Subscribe(sub *Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub.CreatedAt = time.Now()
	return s.store.SaveSubscription(sub)
}

// Unsubscribe removes a push subscription.
func (s *Service) Unsubscribe(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.store.DeleteSubscription(id)
}

// SendToSubscription sends a notification to a specific subscription.
func (s *Service) SendToSubscription(ctx context.Context, sub *Subscription, notification *Notification) error {
	payload, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	wpSub := &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{
			P256dh: sub.Keys.P256dh,
			Auth:   sub.Keys.Auth,
		},
	}

	resp, err := webpush.SendNotificationWithContext(ctx, payload, wpSub, &webpush.Options{
		Subscriber:      s.subject,
		VAPIDPublicKey:  s.vapidPublic,
		VAPIDPrivateKey: s.vapidPrivate,
		TTL:             3600, // 1 hour
		Urgency:         webpush.UrgencyHigh,
	})
	if err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}
	defer resp.Body.Close()

	// Handle subscription expiration
	if resp.StatusCode == 410 || resp.StatusCode == 404 {
		_ = s.store.DeleteSubscription(sub.ID)
		return fmt.Errorf("subscription expired or invalid")
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("push service returned status %d", resp.StatusCode)
	}

	return nil
}

// SendToPrincipal sends a notification to all subscriptions for a principal.
func (s *Service) SendToPrincipal(ctx context.Context, principal string, notification *Notification) error {
	subs, err := s.store.GetSubscriptionsByPrincipal(principal)
	if err != nil {
		return err
	}

	var lastErr error
	for _, sub := range subs {
		if err := s.SendToSubscription(ctx, sub, notification); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// Broadcast sends a notification to all subscriptions.
func (s *Service) Broadcast(ctx context.Context, notification *Notification) error {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		return err
	}

	var lastErr error
	for _, sub := range subs {
		if err := s.SendToSubscription(ctx, sub, notification); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// GenerateVAPIDKeys generates a new VAPID key pair.
func GenerateVAPIDKeys() (publicKey, privateKey string, err error) {
	curve := elliptic.P256()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		return "", "", err
	}

	// Encode private key
	privBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	privateKey = base64.RawURLEncoding.EncodeToString(privBytes)

	// Encode public key (uncompressed point format)
	pubBytes := elliptic.Marshal(curve, key.PublicKey.X, key.PublicKey.Y)
	publicKey = base64.RawURLEncoding.EncodeToString(pubBytes)

	return publicKey, privateKey, nil
}

// Common notification templates

// ToolApprovalNotification creates a notification for tool approval requests.
func ToolApprovalNotification(toolName, sessionID string, args map[string]any) *Notification {
	body := fmt.Sprintf("Tool '%s' requires approval", toolName)
	if desc, ok := args["description"].(string); ok && desc != "" {
		body = desc
	}

	return &Notification{
		Title: "Approval Required",
		Body:  body,
		Icon:  "/icons/buckley-192.png",
		Badge: "/icons/buckley-badge.png",
		Tag:   fmt.Sprintf("approval-%s", sessionID),
		Data: map[string]any{
			"type":      "approval",
			"sessionId": sessionID,
			"toolName":  toolName,
		},
		Actions: []NotificationAction{
			{Action: "approve", Title: "Approve"},
			{Action: "reject", Title: "Reject"},
		},
	}
}

// SessionCompleteNotification creates a notification for completed sessions.
func SessionCompleteNotification(sessionID, message string) *Notification {
	return &Notification{
		Title: "Task Complete",
		Body:  message,
		Icon:  "/icons/buckley-192.png",
		Tag:   fmt.Sprintf("complete-%s", sessionID),
		Data: map[string]any{
			"type":      "complete",
			"sessionId": sessionID,
		},
	}
}

// ErrorNotification creates a notification for errors.
func ErrorNotification(sessionID, message string) *Notification {
	return &Notification{
		Title: "Error",
		Body:  message,
		Icon:  "/icons/buckley-192.png",
		Tag:   fmt.Sprintf("error-%s", sessionID),
		Data: map[string]any{
			"type":      "error",
			"sessionId": sessionID,
		},
	}
}
