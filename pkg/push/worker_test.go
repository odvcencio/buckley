package push

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

func TestWorker_SendToSession_FiltersByPrincipal(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "alice",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	aliceEndpoints := []string{
		"https://example.com/push/alice/subscription/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"https://example.com/push/alice/subscription/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	for _, endpoint := range aliceEndpoints {
		if _, err := store.CreatePushSubscription("alice", endpoint, "p256dh", "auth", "test-agent"); err != nil {
			t.Fatalf("CreatePushSubscription(alice): %v", err)
		}
	}
	if _, err := store.CreatePushSubscription(
		"bob",
		"https://example.com/push/bob/subscription/cccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"p256dh",
		"auth",
		"test-agent",
	); err != nil {
		t.Fatalf("CreatePushSubscription(bob): %v", err)
	}

	var mu sync.Mutex
	var called int
	var principals []string

	worker := &Worker{store: store}
	worker.sendFn = func(ctx context.Context, sub *storage.PushSubscription, payload []byte) error {
		mu.Lock()
		called++
		principals = append(principals, sub.Principal)
		mu.Unlock()
		return nil
	}

	if err := worker.SendToSession(context.Background(), "s1", &Payload{Title: "test"}); err != nil {
		t.Fatalf("SendToSession: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if called != len(aliceEndpoints) {
		t.Fatalf("send calls=%d want %d", called, len(aliceEndpoints))
	}
	for _, principal := range principals {
		if principal != "alice" {
			t.Fatalf("sent to principal=%q want alice", principal)
		}
	}
}

func TestWorker_SendToSession_NoPrincipal_NoSend(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.CreatePushSubscription(
		"alice",
		"https://example.com/push/alice/subscription/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"p256dh",
		"auth",
		"test-agent",
	); err != nil {
		t.Fatalf("CreatePushSubscription(alice): %v", err)
	}

	var mu sync.Mutex
	var called int

	worker := &Worker{store: store}
	worker.sendFn = func(ctx context.Context, sub *storage.PushSubscription, payload []byte) error {
		mu.Lock()
		called++
		mu.Unlock()
		return nil
	}

	if err := worker.SendToSession(context.Background(), "s1", &Payload{Title: "test"}); err != nil {
		t.Fatalf("SendToSession: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if called != 0 {
		t.Fatalf("send calls=%d want 0", called)
	}
}
