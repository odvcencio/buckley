package push

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockStore implements Store interface for testing
type mockStore struct {
	subscriptions map[string]*Subscription
	saveErr       error
	getErr        error
	deleteErr     error
	listErr       error
}

func newMockStore() *mockStore {
	return &mockStore{
		subscriptions: make(map[string]*Subscription),
	}
}

func (m *mockStore) SaveSubscription(sub *Subscription) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.subscriptions[sub.ID] = sub
	return nil
}

func (m *mockStore) GetSubscription(id string) (*Subscription, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.subscriptions[id], nil
}

func (m *mockStore) GetSubscriptionsByPrincipal(principal string) ([]*Subscription, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	var result []*Subscription
	for _, sub := range m.subscriptions {
		if sub.Principal == principal {
			result = append(result, sub)
		}
	}
	return result, nil
}

func (m *mockStore) DeleteSubscription(id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.subscriptions, id)
	return nil
}

func (m *mockStore) ListSubscriptions() ([]*Subscription, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	result := make([]*Subscription, 0, len(m.subscriptions))
	for _, sub := range m.subscriptions {
		result = append(result, sub)
	}
	return result, nil
}

func TestGenerateVAPIDKeys(t *testing.T) {
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys() error = %v", err)
	}

	if pub == "" {
		t.Error("GenerateVAPIDKeys() public key is empty")
	}
	if priv == "" {
		t.Error("GenerateVAPIDKeys() private key is empty")
	}

	// Keys should be different
	if pub == priv {
		t.Error("GenerateVAPIDKeys() public and private keys should be different")
	}

	// Generate another pair - should be different
	pub2, priv2, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys() second call error = %v", err)
	}

	if pub == pub2 {
		t.Error("GenerateVAPIDKeys() should generate unique keys")
	}
	if priv == priv2 {
		t.Error("GenerateVAPIDKeys() should generate unique private keys")
	}
}

func TestNewService(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil store",
			config:  Config{Subject: "mailto:test@example.com"},
			wantErr: true,
			errMsg:  "store required",
		},
		{
			name: "missing subject",
			config: Config{
				Store: newMockStore(),
			},
			wantErr: true,
			errMsg:  "push subject required",
		},
		{
			name: "auto-generate VAPID keys",
			config: Config{
				Store:   newMockStore(),
				Subject: "mailto:test@example.com",
			},
			wantErr: false,
		},
		{
			name: "with provided VAPID keys",
			config: Config{
				Store:        newMockStore(),
				Subject:      "mailto:test@example.com",
				VAPIDPublic:  "test-public",
				VAPIDPrivate: "test-private",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := NewService(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewService() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("NewService() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
			if !tt.wantErr && svc == nil {
				t.Error("NewService() returned nil service without error")
			}
		})
	}
}

func TestService_VAPIDPublicKey(t *testing.T) {
	store := newMockStore()
	svc, err := NewService(Config{
		Store:        store,
		Subject:      "mailto:test@example.com",
		VAPIDPublic:  "test-public-key",
		VAPIDPrivate: "test-private-key",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if got := svc.VAPIDPublicKey(); got != "test-public-key" {
		t.Errorf("VAPIDPublicKey() = %q, want %q", got, "test-public-key")
	}
}

func TestService_Subscribe(t *testing.T) {
	store := newMockStore()
	svc, _ := NewService(Config{
		Store:        store,
		Subject:      "mailto:test@example.com",
		VAPIDPublic:  "pub",
		VAPIDPrivate: "priv",
	})

	sub := &Subscription{
		ID:        "sub-1",
		Endpoint:  "https://push.example.com/send/abc",
		Keys:      Keys{P256dh: "key1", Auth: "auth1"},
		Principal: "user@example.com",
	}

	err := svc.Subscribe(sub)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	// Verify subscription was stored
	stored, _ := store.GetSubscription("sub-1")
	if stored == nil {
		t.Fatal("Subscribe() subscription not stored")
	}
	if stored.CreatedAt.IsZero() {
		t.Error("Subscribe() should set CreatedAt")
	}
}

func TestService_Subscribe_StoreError(t *testing.T) {
	store := newMockStore()
	store.saveErr = errors.New("database error")

	svc, _ := NewService(Config{
		Store:        store,
		Subject:      "mailto:test@example.com",
		VAPIDPublic:  "pub",
		VAPIDPrivate: "priv",
	})

	sub := &Subscription{ID: "sub-1", Endpoint: "https://push.example.com"}
	err := svc.Subscribe(sub)
	if err == nil {
		t.Error("Subscribe() should return error when store fails")
	}
}

func TestService_Unsubscribe(t *testing.T) {
	store := newMockStore()
	store.subscriptions["sub-1"] = &Subscription{ID: "sub-1"}

	svc, _ := NewService(Config{
		Store:        store,
		Subject:      "mailto:test@example.com",
		VAPIDPublic:  "pub",
		VAPIDPrivate: "priv",
	})

	err := svc.Unsubscribe("sub-1")
	if err != nil {
		t.Fatalf("Unsubscribe() error = %v", err)
	}

	if _, exists := store.subscriptions["sub-1"]; exists {
		t.Error("Unsubscribe() should remove subscription from store")
	}
}

func TestService_Unsubscribe_StoreError(t *testing.T) {
	store := newMockStore()
	store.deleteErr = errors.New("delete failed")

	svc, _ := NewService(Config{
		Store:        store,
		Subject:      "mailto:test@example.com",
		VAPIDPublic:  "pub",
		VAPIDPrivate: "priv",
	})

	err := svc.Unsubscribe("sub-1")
	if err == nil {
		t.Error("Unsubscribe() should return error when store fails")
	}
}

func TestToolApprovalNotification(t *testing.T) {
	notification := ToolApprovalNotification("run_shell", "session-123", map[string]any{
		"description": "Execute: ls -la",
	})

	if notification.Title != "Approval Required" {
		t.Errorf("Title = %q, want %q", notification.Title, "Approval Required")
	}
	if notification.Body != "Execute: ls -la" {
		t.Errorf("Body = %q, want %q", notification.Body, "Execute: ls -la")
	}
	if len(notification.Actions) != 2 {
		t.Errorf("Actions len = %d, want 2", len(notification.Actions))
	}
	if notification.Data["type"] != "approval" {
		t.Errorf("Data[type] = %v, want %q", notification.Data["type"], "approval")
	}
	if notification.Data["toolName"] != "run_shell" {
		t.Errorf("Data[toolName] = %v, want %q", notification.Data["toolName"], "run_shell")
	}
}

func TestToolApprovalNotification_NoDescription(t *testing.T) {
	notification := ToolApprovalNotification("edit_file", "session-456", nil)

	if notification.Body != "Tool 'edit_file' requires approval" {
		t.Errorf("Body = %q, want default message", notification.Body)
	}
}

func TestSessionCompleteNotification(t *testing.T) {
	notification := SessionCompleteNotification("session-789", "Build succeeded!")

	if notification.Title != "Task Complete" {
		t.Errorf("Title = %q, want %q", notification.Title, "Task Complete")
	}
	if notification.Body != "Build succeeded!" {
		t.Errorf("Body = %q, want %q", notification.Body, "Build succeeded!")
	}
	if notification.Data["type"] != "complete" {
		t.Errorf("Data[type] = %v, want %q", notification.Data["type"], "complete")
	}
}

func TestErrorNotification(t *testing.T) {
	notification := ErrorNotification("session-error", "Build failed: exit code 1")

	if notification.Title != "Error" {
		t.Errorf("Title = %q, want %q", notification.Title, "Error")
	}
	if notification.Body != "Build failed: exit code 1" {
		t.Errorf("Body = %q, want %q", notification.Body, "Build failed: exit code 1")
	}
	if notification.Data["type"] != "error" {
		t.Errorf("Data[type] = %v, want %q", notification.Data["type"], "error")
	}
}

func TestService_SendToPrincipal_StoreError(t *testing.T) {
	store := newMockStore()
	store.getErr = errors.New("fetch failed")

	svc, _ := NewService(Config{
		Store:        store,
		Subject:      "mailto:test@example.com",
		VAPIDPublic:  "pub",
		VAPIDPrivate: "priv",
	})

	err := svc.SendToPrincipal(context.Background(), "user@example.com", &Notification{
		Title: "Test",
		Body:  "Test message",
	})
	if err == nil {
		t.Error("SendToPrincipal() should return error when store fails")
	}
}

func TestService_Broadcast_StoreError(t *testing.T) {
	store := newMockStore()
	store.listErr = errors.New("list failed")

	svc, _ := NewService(Config{
		Store:        store,
		Subject:      "mailto:test@example.com",
		VAPIDPublic:  "pub",
		VAPIDPrivate: "priv",
	})

	err := svc.Broadcast(context.Background(), &Notification{
		Title: "Test",
		Body:  "Broadcast message",
	})
	if err == nil {
		t.Error("Broadcast() should return error when store fails")
	}
}

func TestSubscription_Fields(t *testing.T) {
	sub := &Subscription{
		ID:        "test-id",
		Endpoint:  "https://push.example.com/send/xyz",
		Keys:      Keys{P256dh: "key", Auth: "auth"},
		Principal: "user@example.com",
		CreatedAt: time.Now(),
		UserAgent: "Mozilla/5.0",
	}

	if sub.ID != "test-id" {
		t.Errorf("ID = %q, want %q", sub.ID, "test-id")
	}
	if sub.Endpoint != "https://push.example.com/send/xyz" {
		t.Error("Endpoint not set correctly")
	}
	if sub.Keys.P256dh != "key" || sub.Keys.Auth != "auth" {
		t.Error("Keys not set correctly")
	}
}

func TestNotification_WithActions(t *testing.T) {
	notification := &Notification{
		Title: "Test",
		Body:  "Test body",
		Actions: []NotificationAction{
			{Action: "action1", Title: "Action 1", Icon: "/icon1.png"},
			{Action: "action2", Title: "Action 2"},
		},
	}

	if len(notification.Actions) != 2 {
		t.Errorf("Actions len = %d, want 2", len(notification.Actions))
	}
	if notification.Actions[0].Action != "action1" {
		t.Error("First action not set correctly")
	}
	if notification.Actions[0].Icon != "/icon1.png" {
		t.Error("First action icon not set correctly")
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
