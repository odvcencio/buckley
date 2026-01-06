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

func TestGRPCSubscribeAllSessionsFiltersByPrincipal(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "viewer", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.CreateSession(&storage.Session{ID: "s2", Principal: "other", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
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

	stream, err := client.Subscribe(ctx, connect.NewRequest(&ipcpb.SubscribeRequest{}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	if !stream.Receive() {
		t.Fatalf("expected hello event, got err=%v", stream.Err())
	}
	if stream.Msg().GetType() != "server.hello" {
		t.Fatalf("expected server.hello, got=%q", stream.Msg().GetType())
	}

	if !stream.Receive() {
		t.Fatalf("expected snapshot event, got err=%v", stream.Err())
	}
	if stream.Msg().GetType() != "sessions.snapshot" {
		t.Fatalf("expected sessions.snapshot, got=%q", stream.Msg().GetType())
	}
	payload := stream.Msg().GetPayload().AsMap()
	rawSessions, ok := payload["sessions"].([]any)
	if !ok {
		t.Fatalf("payload.sessions=%T want []any", payload["sessions"])
	}
	if len(rawSessions) != 1 {
		t.Fatalf("len(payload.sessions)=%d want 1", len(rawSessions))
	}
	sess, ok := rawSessions[0].(map[string]any)
	if !ok {
		t.Fatalf("payload.sessions[0]=%T want map", rawSessions[0])
	}
	if sess["id"] != "s1" {
		t.Fatalf("payload.sessions[0].id=%v want %q", sess["id"], "s1")
	}

	updateTime := time.Date(2025, 12, 12, 13, 37, 0, 0, time.UTC)
	svc.BroadcastEvent(Event{
		Type:      "session.updated",
		SessionID: "s2",
		Payload:   map[string]any{"lastActive": updateTime},
		Timestamp: updateTime,
	})
	svc.BroadcastEvent(Event{
		Type:      "session.updated",
		SessionID: "s1",
		Payload:   map[string]any{"lastActive": updateTime},
		Timestamp: updateTime,
	})

	if !stream.Receive() {
		t.Fatalf("expected forwarded event, got err=%v", stream.Err())
	}
	event := stream.Msg()
	if event.GetSessionId() != "s1" {
		t.Fatalf("event.sessionId=%q want %q (type=%q)", event.GetSessionId(), "s1", event.GetType())
	}
}
