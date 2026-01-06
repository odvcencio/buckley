// Package push implements Web Push notification delivery.
package push

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/odvcencio/buckley/pkg/storage"
)

// NotificationType represents the type of push notification.
type NotificationType string

const (
	// NotificationApproval is sent when a tool call requires approval.
	NotificationApproval NotificationType = "approval"
	// NotificationUpdate is sent for general status updates.
	NotificationUpdate NotificationType = "update"
	// NotificationComplete is sent when a task completes.
	NotificationComplete NotificationType = "complete"
)

// Payload represents the data sent in a push notification.
type Payload struct {
	Title              string           `json:"title"`
	Body               string           `json:"body"`
	Type               NotificationType `json:"type"`
	Tag                string           `json:"tag,omitempty"`
	URL                string           `json:"url,omitempty"`
	SessionID          string           `json:"sessionId,omitempty"`
	ApprovalID         string           `json:"approvalId,omitempty"`
	RequireInteraction bool             `json:"requireInteraction,omitempty"`
}

// VAPIDKeyPair holds the VAPID key pair for Web Push.
type VAPIDKeyPair struct {
	PublicKey  string
	PrivateKey string
}

// Worker sends push notifications to subscribed browsers.
type Worker struct {
	store    *storage.Store
	mu       sync.RWMutex
	vapidKey *VAPIDKeyPair
	subject  string // mailto: or https:// URL
	running  bool
	done     chan struct{}
	sendFn   func(context.Context, *storage.PushSubscription, []byte) error
}

// Config holds configuration for the push worker.
type Config struct {
	// Subject is the mailto: or https:// URL for VAPID
	Subject string
}

// NewWorker creates a new push notification worker.
func NewWorker(store *storage.Store, cfg *Config) (*Worker, error) {
	if cfg == nil {
		cfg = &Config{}
	}

	subject := cfg.Subject
	if subject == "" {
		subject = "mailto:admin@buckley.dev"
	}

	w := &Worker{
		store:   store,
		subject: subject,
		done:    make(chan struct{}),
	}

	// Load or generate VAPID keys
	if err := w.ensureVAPIDKeys(); err != nil {
		return nil, fmt.Errorf("vapid keys: %w", err)
	}

	return w, nil
}

// ensureVAPIDKeys loads existing keys or generates new ones.
func (w *Worker) ensureVAPIDKeys() error {
	keys, err := w.store.GetVAPIDKeys()
	if err != nil {
		return fmt.Errorf("get vapid keys: %w", err)
	}

	if keys != nil && keys.PrivateKey != "" && keys.PublicKey != "" {
		w.vapidKey = &VAPIDKeyPair{
			PrivateKey: keys.PrivateKey,
			PublicKey:  keys.PublicKey,
		}
		return nil
	}

	// Generate new VAPID keys
	privKey, pubKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("generate vapid keys: %w", err)
	}

	// Save to storage
	if err := w.store.SaveVAPIDKeys(pubKey, privKey); err != nil {
		return fmt.Errorf("save vapid keys: %w", err)
	}

	w.vapidKey = &VAPIDKeyPair{
		PrivateKey: privKey,
		PublicKey:  pubKey,
	}

	log.Printf("[push] Generated new VAPID keys")
	return nil
}

// PublicKey returns the VAPID public key for client subscription.
func (w *Worker) PublicKey() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.vapidKey == nil {
		return ""
	}
	return w.vapidKey.PublicKey
}

// Start begins processing storage events for notifications.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	w.running = true
	w.mu.Unlock()

	// Register as observer for storage events
	w.store.AddObserver(storage.ObserverFunc(func(event storage.Event) {
		w.handleEvent(ctx, event)
	}))

	// Monitor for shutdown
	go func() {
		select {
		case <-ctx.Done():
		case <-w.done:
		}
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
	}()

	return nil
}

// Stop stops the push worker.
func (w *Worker) Stop() {
	w.mu.Lock()
	if w.running {
		close(w.done)
	}
	w.mu.Unlock()
}

// handleEvent processes storage events and sends notifications.
func (w *Worker) handleEvent(ctx context.Context, event storage.Event) {
	switch event.Type {
	case storage.EventApprovalCreated:
		w.handleApprovalCreated(ctx, event)
	case storage.EventApprovalExpired:
		w.handleApprovalExpired(ctx, event)
	}
}

// handleApprovalCreated sends a push notification for a new approval request.
func (w *Worker) handleApprovalCreated(ctx context.Context, event storage.Event) {
	data, ok := event.Data.(map[string]any)
	if !ok {
		return
	}

	toolName, _ := data["tool_name"].(string)
	riskScore, _ := data["risk_score"].(int)

	payload := &Payload{
		Title:              "Approval Required",
		Body:               fmt.Sprintf("Tool: %s (risk: %d)", toolName, riskScore),
		Type:               NotificationApproval,
		Tag:                "approval-" + event.EntityID,
		SessionID:          event.SessionID,
		ApprovalID:         event.EntityID,
		RequireInteraction: true,
		URL:                fmt.Sprintf("/?session=%s&approval=%s", event.SessionID, event.EntityID),
	}

	// Send to subscribers for this session's principal.
	if err := w.SendToSession(ctx, event.SessionID, payload); err != nil {
		log.Printf("[push] Failed to send approval notification: %v", err)
	}
}

// handleApprovalExpired sends a notification when an approval expires.
func (w *Worker) handleApprovalExpired(ctx context.Context, event storage.Event) {
	payload := &Payload{
		Title:     "Approval Expired",
		Body:      "A tool call approval request has expired",
		Type:      NotificationUpdate,
		Tag:       "expired-" + event.EntityID,
		SessionID: event.SessionID,
	}

	if err := w.SendToSession(ctx, event.SessionID, payload); err != nil {
		log.Printf("[push] Failed to send expiry notification: %v", err)
	}
}

func (w *Worker) sendToSubscriptions(ctx context.Context, subs []*storage.PushSubscription, payload *Payload) error {
	if len(subs) == 0 {
		return nil
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	sendFn := w.sendFn
	if sendFn == nil {
		sendFn = w.send
	}

	var wg sync.WaitGroup
	var failures int
	var failureMu sync.Mutex

	for _, sub := range subs {
		wg.Add(1)
		go func(sub *storage.PushSubscription) {
			defer wg.Done()

			if err := sendFn(ctx, sub, payloadBytes); err != nil {
				preview := sub.Endpoint
				if len(preview) > 50 {
					preview = preview[:50]
				}
				log.Printf("[push] Failed to send to %s: %v", preview, err)
				failureMu.Lock()
				failures++
				failureMu.Unlock()

				// Remove invalid subscriptions
				if isGone(err) {
					w.store.DeletePushSubscriptionByEndpoint(sub.Endpoint)
				}
			}
		}(sub)
	}

	wg.Wait()

	if failures == len(subs) {
		return fmt.Errorf("all %d notifications failed", failures)
	}

	return nil
}

// SendToAll sends a notification to all subscribed browsers.
func (w *Worker) SendToAll(ctx context.Context, payload *Payload) error {
	subs, err := w.store.ListPushSubscriptions()
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	return w.sendToSubscriptions(ctx, subs, payload)
}

// SendToPrincipal sends a notification to browsers subscribed under a principal.
func (w *Worker) SendToPrincipal(ctx context.Context, principal string, payload *Payload) error {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return nil
	}

	subs, err := w.store.GetPushSubscriptionsByPrincipal(principal)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}

	return w.sendToSubscriptions(ctx, subs, payload)
}

// SendToSession sends a notification to browsers subscribed to a session.
func (w *Worker) SendToSession(ctx context.Context, sessionID string, payload *Payload) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}

	session, err := w.store.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}
	if session == nil {
		return nil
	}

	return w.SendToPrincipal(ctx, session.Principal, payload)
}

// send delivers a push notification to a single subscription.
func (w *Worker) send(ctx context.Context, sub *storage.PushSubscription, payload []byte) error {
	w.mu.RLock()
	vapidKey := w.vapidKey
	subject := w.subject
	w.mu.RUnlock()

	if vapidKey == nil {
		return fmt.Errorf("no VAPID keys configured")
	}

	subscription := &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{
			P256dh: sub.P256dh,
			Auth:   sub.Auth,
		},
	}

	options := &webpush.Options{
		Subscriber:      subject,
		VAPIDPublicKey:  vapidKey.PublicKey,
		VAPIDPrivateKey: vapidKey.PrivateKey,
		TTL:             300, // 5 minutes
		Urgency:         webpush.UrgencyHigh,
	}

	resp, err := webpush.SendNotificationWithContext(ctx, payload, subscription, options)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("push failed with status %d", resp.StatusCode)
	}

	return nil
}

// isGone checks if the error indicates the subscription is no longer valid.
func isGone(err error) bool {
	if err == nil {
		return false
	}
	// Check for 410 Gone or 404 Not Found status
	errStr := err.Error()
	return contains(errStr, "410") || contains(errStr, "404") || contains(errStr, "gone")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// NotifyApprovalRequired sends a push notification for a pending approval.
func (w *Worker) NotifyApprovalRequired(ctx context.Context, approval *storage.PendingApproval) error {
	payload := &Payload{
		Title:              "Approval Required",
		Body:               fmt.Sprintf("%s needs approval", approval.ToolName),
		Type:               NotificationApproval,
		Tag:                "approval-" + approval.ID,
		SessionID:          approval.SessionID,
		ApprovalID:         approval.ID,
		RequireInteraction: true,
		URL:                fmt.Sprintf("/?session=%s&approval=%s", approval.SessionID, approval.ID),
	}

	// Optionally include TTL based on expiry
	ttl := time.Until(approval.ExpiresAt)
	if ttl <= 0 {
		return nil // Already expired
	}

	return w.SendToSession(ctx, approval.SessionID, payload)
}
