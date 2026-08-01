package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"m31labs.dev/buckley/v2/pkg/headless"
	ipcpb "m31labs.dev/buckley/v2/pkg/ipc/proto"
	"m31labs.dev/buckley/v2/pkg/ipc/proto/ipcpbconnect"
	"m31labs.dev/buckley/v2/pkg/storage"
)

// TestSessionServiceRoundTrip exercises the session-service surface Pillar A
// depends on (ListSessions, GetSession, IssueSessionToken, Subscribe,
// SendCommand) against an in-process server backed by the existing
// pkg/headless Registry machinery (via fakeHeadlessRegistry), the same
// double pkg/ipc's own headless tests use. No new session store is
// involved: sessions live in the same storage.Store that backs the TUI and
// the REST handlers.
func TestSessionServiceRoundTrip(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	const sessionID = "rt-session"
	const principalName = "operator-1"
	if err := store.CreateSession(&storage.Session{
		ID:          sessionID,
		Principal:   principalName,
		ProjectPath: "/tmp/project",
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveMessage(&storage.Message{
		SessionID: sessionID,
		Role:      "user",
		Content:   "hello from the transcript",
		Timestamp: now,
	}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	registry := newFakeHeadlessRegistry()
	registry.sessions[sessionID] = &headless.SessionInfo{
		ID:         sessionID,
		Project:    "/tmp/project",
		State:      headless.StateProcessing,
		CreatedAt:  now,
		LastActive: now,
	}

	server := &Server{store: store, headlessRegistry: registry}
	svc := NewGRPCService(server)
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
				Name:  principalName,
				Scope: storage.TokenScopeMember,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}).Mount(grpcPath, grpcHandler)

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	client := ipcpbconnect.NewBuckleyIPCClient(ts.Client(), ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. ListSessions must surface the session created through the store
	// (the same store the headless Registry persists into).
	listResp, err := client.ListSessions(ctx, connect.NewRequest(&ipcpb.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(listResp.Msg.GetSessions()) != 1 || listResp.Msg.GetSessions()[0].GetId() != sessionID {
		t.Fatalf("ListSessions sessions=%+v want one session %q", listResp.Msg.GetSessions(), sessionID)
	}

	// 2. GetSession returns state plus the recent transcript.
	getResp, err := client.GetSession(ctx, connect.NewRequest(&ipcpb.GetSessionRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if getResp.Msg.GetSession().GetId() != sessionID {
		t.Fatalf("GetSession session id=%q want %q", getResp.Msg.GetSession().GetId(), sessionID)
	}
	if len(getResp.Msg.GetRecentMessages()) != 1 || getResp.Msg.GetRecentMessages()[0].GetContent() != "hello from the transcript" {
		t.Fatalf("GetSession recent messages=%+v want one seeded message", getResp.Msg.GetRecentMessages())
	}

	// 3. IssueSessionToken mints the per-session token SendCommand requires.
	tokenResp, err := client.IssueSessionToken(ctx, connect.NewRequest(&ipcpb.IssueSessionTokenRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}
	if tokenResp.Msg.GetToken() == "" {
		t.Fatalf("IssueSessionToken returned an empty token")
	}

	// 4. Subscribe(session_id) streams events scoped to that session.
	stream, err := client.Subscribe(ctx, connect.NewRequest(&ipcpb.SubscribeRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if !stream.Receive() {
		t.Fatalf("expected hello event, got err=%v", stream.Err())
	}
	if stream.Msg().GetType() != "server.hello" {
		t.Fatalf("first event type=%q want server.hello", stream.Msg().GetType())
	}

	svc.BroadcastEvent(Event{
		Type:      "message.created",
		SessionID: sessionID,
		Payload:   map[string]any{"role": "assistant", "content": "streamed"},
		Timestamp: time.Now(),
	})

	if !stream.Receive() {
		t.Fatalf("expected forwarded event, got err=%v", stream.Err())
	}
	if got := stream.Msg().GetType(); got != "message.created" {
		t.Fatalf("forwarded event type=%q want message.created", got)
	}

	// 5. SendCommand (type=input) drives the headless session through the
	// same Registry.DispatchCommand path CreateHeadlessSession-backed
	// sessions use; no parallel command path is introduced.
	sendResp, err := client.SendCommand(ctx, connect.NewRequest(&ipcpb.CommandRequest{
		SessionId:    sessionID,
		Type:         "input",
		Content:      "drive this session",
		SessionToken: tokenResp.Msg.GetToken(),
	}))
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if sendResp.Msg.GetStatus() != "accepted" {
		t.Fatalf("SendCommand status=%q want accepted (message=%q)", sendResp.Msg.GetStatus(), sendResp.Msg.GetMessage())
	}
	if registry.lastCommand.Content != "drive this session" || registry.lastCommand.Type != "input" {
		t.Fatalf("registry.lastCommand=%+v want content=%q type=input", registry.lastCommand, "drive this session")
	}
}

// TestServerDefaultBindIsLoopback asserts pkg/ipc keeps binding to loopback
// by default: Pillar A relies on this so `buckley attach`'s default address
// never reaches off-box without an explicit opt-in.
func TestServerDefaultBindIsLoopback(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{}, store, nil, nil, nil, nil, nil, nil)
	if !isLoopbackBindAddress(server.cfg.BindAddress) {
		t.Fatalf("default BindAddress=%q is not loopback", server.cfg.BindAddress)
	}
	if server.cfg.BindAddress != "127.0.0.1:4488" {
		t.Fatalf("default BindAddress=%q want 127.0.0.1:4488", server.cfg.BindAddress)
	}
}
