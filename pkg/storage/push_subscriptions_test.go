package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPushSubscription_CRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tests := []struct {
		name      string
		principal string
		endpoint  string
		p256dh    string
		auth      string
		userAgent string
	}{
		{
			name:      "basic subscription",
			principal: "user@example.com",
			endpoint:  "https://fcm.googleapis.com/fcm/send/abc123",
			p256dh:    "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM=",
			auth:      "tBHItJI5svbpez7KI4CCXg==",
			userAgent: "Mozilla/5.0",
		},
		{
			name:      "subscription without user agent",
			principal: "admin@example.com",
			endpoint:  "https://updates.push.services.mozilla.com/wpush/v2/xyz789",
			p256dh:    "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcx8N15h4Sj1rq8",
			auth:      "8675309JennyDontChangeYourNumber",
			userAgent: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test Save (create)
			sub := &PushSubscription{
				ID:        generateID(),
				Endpoint:  tt.endpoint,
				P256dh:    tt.p256dh,
				Auth:      tt.auth,
				Principal: tt.principal,
				UserAgent: tt.userAgent,
				CreatedAt: time.Now().UTC(),
			}

			if err := store.SavePushSubscription(sub); err != nil {
				t.Fatalf("SavePushSubscription failed: %v", err)
			}

			// Test Get by ID
			retrieved, err := store.GetPushSubscription(sub.ID)
			if err != nil {
				t.Fatalf("GetPushSubscription failed: %v", err)
			}
			if retrieved == nil {
				t.Fatalf("GetPushSubscription returned nil")
			}
			if retrieved.ID != sub.ID {
				t.Errorf("ID mismatch: got %s, want %s", retrieved.ID, sub.ID)
			}
			if retrieved.Endpoint != sub.Endpoint {
				t.Errorf("Endpoint mismatch: got %s, want %s", retrieved.Endpoint, sub.Endpoint)
			}
			if retrieved.Principal != sub.Principal {
				t.Errorf("Principal mismatch: got %s, want %s", retrieved.Principal, sub.Principal)
			}

			// Test Get by Endpoint
			byEndpoint, err := store.GetPushSubscriptionByEndpoint(tt.endpoint)
			if err != nil {
				t.Fatalf("GetPushSubscriptionByEndpoint failed: %v", err)
			}
			if byEndpoint == nil {
				t.Fatalf("GetPushSubscriptionByEndpoint returned nil")
			}
			if byEndpoint.ID != sub.ID {
				t.Errorf("GetByEndpoint returned wrong ID: got %s, want %s", byEndpoint.ID, sub.ID)
			}

			// Test Update (upsert)
			sub.UserAgent = "Updated-Agent/1.0"
			if err := store.SavePushSubscription(sub); err != nil {
				t.Fatalf("SavePushSubscription (update) failed: %v", err)
			}
			updated, err := store.GetPushSubscription(sub.ID)
			if err != nil {
				t.Fatalf("GetPushSubscription after update failed: %v", err)
			}
			if updated == nil {
				t.Fatalf("GetPushSubscription after update returned nil")
			}
			if updated.UserAgent != "Updated-Agent/1.0" {
				t.Errorf("UserAgent not updated: got %s, want %s", updated.UserAgent, "Updated-Agent/1.0")
			}

			// Test Delete by ID
			if err := store.DeletePushSubscription(sub.ID); err != nil {
				t.Fatalf("DeletePushSubscription failed: %v", err)
			}
			deleted, err := store.GetPushSubscription(sub.ID)
			if err != nil {
				t.Fatalf("GetPushSubscription after delete returned error: %v", err)
			}
			if deleted != nil {
				t.Errorf("Expected nil after deletion, got subscription: %+v", deleted)
			}
		})
	}
}

func TestPushSubscription_ListByPrincipal(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	principal := "multidevice@example.com"
	endpoints := []string{
		"https://fcm.googleapis.com/fcm/send/device1",
		"https://fcm.googleapis.com/fcm/send/device2",
		"https://fcm.googleapis.com/fcm/send/device3",
	}

	// Create multiple subscriptions for same principal
	for i, endpoint := range endpoints {
		sub := &PushSubscription{
			ID:        generateID(),
			Endpoint:  endpoint,
			P256dh:    "test-p256dh-key",
			Auth:      "test-auth-secret",
			Principal: principal,
			UserAgent: "Device-" + string(rune('1'+i)),
			CreatedAt: time.Now().UTC(),
		}
		if err := store.SavePushSubscription(sub); err != nil {
			t.Fatalf("SavePushSubscription failed for endpoint %s: %v", endpoint, err)
		}
	}

	// Create subscription for different principal
	otherSub := &PushSubscription{
		ID:        generateID(),
		Endpoint:  "https://fcm.googleapis.com/fcm/send/other",
		P256dh:    "other-p256dh-key",
		Auth:      "other-auth-secret",
		Principal: "other@example.com",
		UserAgent: "OtherDevice",
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SavePushSubscription(otherSub); err != nil {
		t.Fatalf("SavePushSubscription failed for other principal: %v", err)
	}

	// Test GetPushSubscriptionsByPrincipal
	subs, err := store.GetPushSubscriptionsByPrincipal(principal)
	if err != nil {
		t.Fatalf("GetPushSubscriptionsByPrincipal failed: %v", err)
	}
	if len(subs) != 3 {
		t.Errorf("Expected 3 subscriptions for principal, got %d", len(subs))
	}

	// Verify all returned subscriptions belong to the correct principal
	endpointMap := make(map[string]bool)
	for _, sub := range subs {
		if sub.Principal != principal {
			t.Errorf("Got subscription for wrong principal: %s (want %s)", sub.Principal, principal)
		}
		endpointMap[sub.Endpoint] = true
	}

	// Verify all expected endpoints are present
	for _, endpoint := range endpoints {
		if !endpointMap[endpoint] {
			t.Errorf("Expected endpoint %s not found in results", endpoint)
		}
	}
}

func TestPushSubscription_DeleteByEndpoint(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	endpoint := "https://fcm.googleapis.com/fcm/send/delete-test"
	sub := &PushSubscription{
		ID:        generateID(),
		Endpoint:  endpoint,
		P256dh:    "test-key",
		Auth:      "test-auth",
		Principal: "delete@example.com",
		UserAgent: "TestAgent",
		CreatedAt: time.Now().UTC(),
	}

	if err := store.SavePushSubscription(sub); err != nil {
		t.Fatalf("SavePushSubscription failed: %v", err)
	}

	// Verify subscription exists
	retrieved, err := store.GetPushSubscriptionByEndpoint(endpoint)
	if err != nil {
		t.Fatalf("GetPushSubscriptionByEndpoint failed: %v", err)
	}
	if retrieved == nil {
		t.Fatalf("GetPushSubscriptionByEndpoint returned nil before deletion")
	}
	if retrieved.ID != sub.ID {
		t.Errorf("Retrieved wrong subscription: got ID %s, want %s", retrieved.ID, sub.ID)
	}

	// Delete by endpoint
	if err := store.DeletePushSubscriptionByEndpoint(endpoint); err != nil {
		t.Fatalf("DeletePushSubscriptionByEndpoint failed: %v", err)
	}

	// Verify deletion
	deleted, err := store.GetPushSubscriptionByEndpoint(endpoint)
	if err != nil {
		t.Fatalf("GetPushSubscriptionByEndpoint after delete returned error: %v", err)
	}
	if deleted != nil {
		t.Errorf("Expected nil after deletion by endpoint, got subscription: %+v", deleted)
	}
}

func TestPushSubscription_NilStore(t *testing.T) {
	var store *Store

	tests := []struct {
		name string
		fn   func() error
	}{
		{
			name: "SavePushSubscription",
			fn: func() error {
				return store.SavePushSubscription(&PushSubscription{})
			},
		},
		{
			name: "GetPushSubscription",
			fn: func() error {
				_, err := store.GetPushSubscription("test-id")
				return err
			},
		},
		{
			name: "GetPushSubscriptionByEndpoint",
			fn: func() error {
				_, err := store.GetPushSubscriptionByEndpoint("test-endpoint")
				return err
			},
		},
		{
			name: "GetPushSubscriptionsByPrincipal",
			fn: func() error {
				_, err := store.GetPushSubscriptionsByPrincipal("test-principal")
				return err
			},
		},
		{
			name: "DeletePushSubscription",
			fn: func() error {
				return store.DeletePushSubscription("test-id")
			},
		},
		{
			name: "DeletePushSubscriptionByEndpoint",
			fn: func() error {
				return store.DeletePushSubscriptionByEndpoint("test-endpoint")
			},
		},
		{
			name: "ListPushSubscriptions",
			fn: func() error {
				_, err := store.ListPushSubscriptions()
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if err != ErrStoreClosed {
				t.Errorf("Expected ErrStoreClosed, got %v", err)
			}
		})
	}
}
