package ipc

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/odvcencio/buckley/pkg/ipc/push"
	"github.com/odvcencio/buckley/pkg/storage"
)

// pushStoreAdapter adapts storage.Store to push.Store interface.
type pushStoreAdapter struct {
	store *storage.Store
}

func (a *pushStoreAdapter) SaveSubscription(sub *push.Subscription) error {
	storageSub := &storage.PushSubscription{
		ID:        sub.ID,
		Endpoint:  sub.Endpoint,
		P256dh:    sub.Keys.P256dh,
		Auth:      sub.Keys.Auth,
		Principal: sub.Principal,
		UserAgent: sub.UserAgent,
		CreatedAt: sub.CreatedAt,
	}
	return a.store.SavePushSubscription(storageSub)
}

func (a *pushStoreAdapter) GetSubscription(id string) (*push.Subscription, error) {
	storageSub, err := a.store.GetPushSubscription(id)
	if err != nil {
		return nil, err
	}
	if storageSub == nil {
		return nil, nil
	}
	return &push.Subscription{
		ID:        storageSub.ID,
		Endpoint:  storageSub.Endpoint,
		Keys:      push.Keys{P256dh: storageSub.P256dh, Auth: storageSub.Auth},
		Principal: storageSub.Principal,
		UserAgent: storageSub.UserAgent,
		CreatedAt: storageSub.CreatedAt,
	}, nil
}

func (a *pushStoreAdapter) GetSubscriptionsByPrincipal(principal string) ([]*push.Subscription, error) {
	storageSubs, err := a.store.GetPushSubscriptionsByPrincipal(principal)
	if err != nil {
		return nil, err
	}
	subs := make([]*push.Subscription, len(storageSubs))
	for i, s := range storageSubs {
		subs[i] = &push.Subscription{
			ID:        s.ID,
			Endpoint:  s.Endpoint,
			Keys:      push.Keys{P256dh: s.P256dh, Auth: s.Auth},
			Principal: s.Principal,
			UserAgent: s.UserAgent,
			CreatedAt: s.CreatedAt,
		}
	}
	return subs, nil
}

func (a *pushStoreAdapter) DeleteSubscription(id string) error {
	return a.store.DeletePushSubscription(id)
}

func (a *pushStoreAdapter) ListSubscriptions() ([]*push.Subscription, error) {
	storageSubs, err := a.store.ListPushSubscriptions()
	if err != nil {
		return nil, err
	}
	subs := make([]*push.Subscription, len(storageSubs))
	for i, s := range storageSubs {
		subs[i] = &push.Subscription{
			ID:        s.ID,
			Endpoint:  s.Endpoint,
			Keys:      push.Keys{P256dh: s.P256dh, Auth: s.Auth},
			Principal: s.Principal,
			UserAgent: s.UserAgent,
			CreatedAt: s.CreatedAt,
		}
	}
	return subs, nil
}

// setupPushRoutes adds push notification routes to the API router.
func (s *Server) setupPushRoutes(r chi.Router) {
	r.Route("/push", func(r chi.Router) {
		r.Get("/vapid-public-key", s.handleVAPIDPublicKey)
		r.Post("/subscribe", s.handlePushSubscribe)
		r.Delete("/subscribe/{id}", s.handlePushUnsubscribe)
		r.Get("/subscriptions", s.handleListPushSubscriptions)
		r.Post("/test", s.handleTestPush)
	})
}

// InitPushService initializes the push notification service.
func (s *Server) InitPushService() error {
	if s.store == nil {
		return fmt.Errorf("storage required for push service")
	}

	// Try to load existing VAPID keys from storage
	keys, err := s.store.GetVAPIDKeys()
	if err != nil {
		return fmt.Errorf("failed to get VAPID keys: %w", err)
	}

	var vapidPublic, vapidPrivate string
	if keys != nil {
		vapidPublic = keys.PublicKey
		vapidPrivate = keys.PrivateKey
	} else {
		// Generate new keys and persist them
		vapidPublic, vapidPrivate, err = push.GenerateVAPIDKeys()
		if err != nil {
			return fmt.Errorf("failed to generate VAPID keys: %w", err)
		}
		if err := s.store.SaveVAPIDKeys(vapidPublic, vapidPrivate); err != nil {
			return fmt.Errorf("failed to save VAPID keys: %w", err)
		}
	}

	// Get push subject from config (set via BUCKLEY_PUSH_SUBJECT env var or ipc.push_subject in config)
	var subject string
	if s.appConfig != nil {
		subject = s.appConfig.IPC.PushSubject
	}

	adapter := &pushStoreAdapter{store: s.store}
	svc, err := push.NewService(push.Config{
		Store:        adapter,
		VAPIDPublic:  vapidPublic,
		VAPIDPrivate: vapidPrivate,
		Subject:      subject,
	})
	if err != nil {
		return err
	}

	s.pushService = svc
	return nil
}

// GetPushService returns the push notification service, if initialized.
func (s *Server) GetPushService() *push.Service {
	if s == nil {
		return nil
	}
	return s.pushService
}

// handleVAPIDPublicKey returns the VAPID public key for client subscription.
func (s *Server) handleVAPIDPublicKey(w http.ResponseWriter, r *http.Request) {
	if s.pushService == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("push notifications not enabled"))
		return
	}

	respondJSON(w, map[string]string{
		"publicKey": s.pushService.VAPIDPublicKey(),
	})
}

// PushSubscribeRequest is the request body for subscribing to push notifications.
type PushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	UserAgent string `json:"userAgent,omitempty"`
}

// handlePushSubscribe registers a new push subscription.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.pushService == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("push notifications not enabled"))
		return
	}

	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	var req PushSubscribeRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesSmall, false); err != nil {
		respondError(w, status, err)
		return
	}

	if req.Endpoint == "" || req.Keys.P256dh == "" || req.Keys.Auth == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("endpoint and keys required"))
		return
	}

	sub := &push.Subscription{
		ID:        uuid.New().String(),
		Endpoint:  req.Endpoint,
		Keys:      push.Keys{P256dh: req.Keys.P256dh, Auth: req.Keys.Auth},
		Principal: principal.Name,
		UserAgent: req.UserAgent,
		CreatedAt: time.Now(),
	}

	if err := s.pushService.Subscribe(sub); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	respondJSON(w, map[string]any{
		"id":        sub.ID,
		"principal": sub.Principal,
		"createdAt": sub.CreatedAt,
	})
}

// handlePushUnsubscribe removes a push subscription.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.pushService == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("push notifications not enabled"))
		return
	}

	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("subscription id required"))
		return
	}

	// Verify ownership (optional: admins could delete any)
	adapter := &pushStoreAdapter{store: s.store}
	sub, err := adapter.GetSubscription(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if sub == nil {
		respondError(w, http.StatusNotFound, fmt.Errorf("subscription not found"))
		return
	}
	if sub.Principal != principal.Name && principal.Scope != storage.TokenScopeOperator {
		respondError(w, http.StatusForbidden, fmt.Errorf("cannot delete another user's subscription"))
		return
	}

	if err := s.pushService.Unsubscribe(id); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListPushSubscriptions lists subscriptions for the current principal.
func (s *Server) handleListPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	if s.pushService == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("push notifications not enabled"))
		return
	}

	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	adapter := &pushStoreAdapter{store: s.store}
	subs, err := adapter.GetSubscriptionsByPrincipal(principal.Name)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	// Remove sensitive keys from response
	result := make([]map[string]any, len(subs))
	for i, sub := range subs {
		result[i] = map[string]any{
			"id":        sub.ID,
			"endpoint":  sub.Endpoint,
			"userAgent": sub.UserAgent,
			"createdAt": sub.CreatedAt,
		}
	}

	respondJSON(w, map[string]any{
		"subscriptions": result,
	})
}

// handleTestPush sends a test notification to the current user.
func (s *Server) handleTestPush(w http.ResponseWriter, r *http.Request) {
	if s.pushService == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("push notifications not enabled"))
		return
	}

	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	notification := &push.Notification{
		Title: "Test Notification",
		Body:  "Push notifications are working!",
		Icon:  "/icons/buckley-192.png",
		Tag:   "test",
		Data: map[string]any{
			"type": "test",
		},
	}

	if err := s.pushService.SendToPrincipal(r.Context(), principal.Name, notification); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	respondJSON(w, map[string]string{"status": "sent"})
}
