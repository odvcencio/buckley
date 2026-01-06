package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestInitPushServiceEnablesVAPIDAndSubscribe(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	appCfg := &config.Config{IPC: config.IPCConfig{PushSubject: "mailto:test@example.com"}}
	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), appCfg, nil, nil)
	if err := server.InitPushService(); err != nil {
		t.Fatalf("InitPushService: %v", err)
	}

	vapidReq := httptest.NewRequest(http.MethodGet, "/api/push/vapid-public-key", nil)
	vapidRec := httptest.NewRecorder()
	server.handleVAPIDPublicKey(vapidRec, vapidReq)
	if vapidRec.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", vapidRec.Code, vapidRec.Body.String())
	}
	var vapidResp struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal(vapidRec.Body.Bytes(), &vapidResp); err != nil {
		t.Fatalf("decode vapid response: %v", err)
	}
	if strings.TrimSpace(vapidResp.PublicKey) == "" {
		t.Fatalf("expected publicKey in response")
	}

	subscribeReq := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(`{"endpoint":"https://example.test/push","keys":{"p256dh":"p","auth":"a"}}`))
	subscribeReq.Header.Set("Content-Type", "application/json")
	subscribeReq = subscribeReq.WithContext(context.WithValue(subscribeReq.Context(), principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember}))
	subscribeRec := httptest.NewRecorder()
	server.handlePushSubscribe(subscribeRec, subscribeReq)
	if subscribeRec.Code != http.StatusCreated {
		t.Fatalf("expected created, got %d: %s", subscribeRec.Code, subscribeRec.Body.String())
	}
	var subscribeResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(subscribeRec.Body.Bytes(), &subscribeResp); err != nil {
		t.Fatalf("decode subscribe response: %v", err)
	}
	if strings.TrimSpace(subscribeResp.ID) == "" {
		t.Fatalf("expected id in response")
	}

	sub, err := store.GetPushSubscription(subscribeResp.ID)
	if err != nil {
		t.Fatalf("GetPushSubscription: %v", err)
	}
	if sub == nil || sub.Principal != "alice" {
		t.Fatalf("expected stored subscription for alice, got %+v", sub)
	}
}
