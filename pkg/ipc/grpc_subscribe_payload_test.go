package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/ipc/proto/ipcpbconnect"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestGRPCSubscribeEventPayloadJSONSafe(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionNow := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "viewer", CreatedAt: sessionNow, LastActive: sessionNow, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	svc := NewGRPCService(&Server{store: store})
	svc.subscribeLimiter = nil
	svc.maxSubscribersTotal = 10
	svc.maxSubscribersPerPrincipal = 10

	grpcPath, grpcHandler := ipcpbconnect.NewBuckleyIPCHandler(
		svc,
		connect.WithReadMaxBytes(maxConnectReadBytes),
	)

	router := chi.NewRouter()
	router.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), principalContextKey, &requestPrincipal{
				Name:  "viewer",
				Scope: storage.TokenScopeViewer,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}).Mount(grpcPath, grpcHandler)

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	client := ipcpbconnect.NewBuckleyIPCClient(ts.Client(), ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Subscribe(ctx, connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: "s1"}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if !stream.Receive() {
		t.Fatalf("expected hello event, got err=%v", stream.Err())
	}

	now := time.Date(2025, 12, 12, 13, 37, 0, 0, time.UTC)
	msg := storage.Message{
		SessionID: "s1",
		Role:      "assistant",
		Content:   "hello",
		Timestamp: now,
	}

	svc.BroadcastEvent(Event{
		Type:      "session.updated",
		SessionID: "s1",
		Payload: map[string]any{
			"lastActive":    now,
			"messageDelta":  1,
			"tokensDelta":   3,
			"latestMessage": msg,
		},
		Timestamp: now,
	})

	if !stream.Receive() {
		t.Fatalf("expected event, got err=%v", stream.Err())
	}
	event := stream.Msg()
	if event.GetPayload() == nil {
		t.Fatalf("expected payload, got nil (event=%+v)", event)
	}
	payload := event.GetPayload().AsMap()
	if payload["lastActive"] != now.Format(time.RFC3339Nano) {
		t.Fatalf("payload.lastActive=%v want %v", payload["lastActive"], now.Format(time.RFC3339Nano))
	}
	latest, ok := payload["latestMessage"].(map[string]any)
	if !ok {
		t.Fatalf("payload.latestMessage=%T want object", payload["latestMessage"])
	}
	if latest["content"] != "hello" {
		t.Fatalf("payload.latestMessage.content=%v want %q", latest["content"], "hello")
	}
	if latest["role"] != "assistant" {
		t.Fatalf("payload.latestMessage.role=%v want %q", latest["role"], "assistant")
	}
	if latest["timestamp"] != now.Format(time.RFC3339Nano) {
		t.Fatalf("payload.latestMessage.timestamp=%v want %v", latest["timestamp"], now.Format(time.RFC3339Nano))
	}
}
