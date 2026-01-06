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

func TestGRPCSubscribeEnforcesSubscriberLimit(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "viewer", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	svc := NewGRPCService(&Server{store: store})
	svc.subscribeLimiter = nil
	svc.maxSubscribersTotal = 1
	svc.maxSubscribersPerPrincipal = 1

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
	ctx := context.Background()

	stream, err := client.Subscribe(ctx, connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: "s1"}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if !stream.Receive() {
		t.Fatalf("expected hello event, got err=%v", stream.Err())
	}

	stream2, err := client.Subscribe(ctx, connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: "s1"}))
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	t.Cleanup(func() { _ = stream2.Close() })
	if stream2.Receive() {
		t.Fatalf("expected subscriber limit error, got event=%+v", stream2.Msg())
	}
	assertConnectCode(t, stream2.Err(), connect.CodeResourceExhausted)
}

func TestGRPCSubscribeRateLimited(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: "s1", Principal: "viewer", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	svc := NewGRPCService(&Server{store: store})
	svc.subscribeLimiter = newRateLimiter(1 * time.Minute)
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
	ctx := context.Background()

	stream, err := client.Subscribe(ctx, connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: "s1"}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if !stream.Receive() {
		t.Fatalf("expected hello event, got err=%v", stream.Err())
	}

	stream2, err := client.Subscribe(ctx, connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: "s1"}))
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	t.Cleanup(func() { _ = stream2.Close() })
	if stream2.Receive() {
		t.Fatalf("expected rate limit error, got event=%+v", stream2.Msg())
	}
	assertConnectCode(t, stream2.Err(), connect.CodeResourceExhausted)
}
