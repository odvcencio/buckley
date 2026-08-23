package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/fluffyui/backend/sim"
)

func TestToolLoopOpenRouterImplicitRetentionStopsBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"stealth/ox-alpha","name":"Ox Alpha","context_length":128000}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "test-key"
	cfg.Providers.OpenRouter.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openrouter"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	requests.Store(0)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	ctrl := &Controller{app: app, cfg: cfg, modelMgr: mgr, workDir: t.TempDir()}
	conv := conversation.New("privacy-test")
	conv.AddUserMessage("hello")
	sess := &SessionState{ID: "privacy-test", Conversation: conv}
	req, _ := ctrl.buildToolLoopRequest(sess, "stealth/ox-alpha", false, nil)

	resp, err := ctrl.callToolLoopModel(
		context.Background(),
		req,
		"stealth/ox-alpha",
		1,
		sess,
		nil,
	)
	if resp != nil || !errors.Is(err, model.ErrOpenRouterRetentionUnspecified) {
		t.Fatalf("callToolLoopModel() = %#v, %v; want fail-closed retention error", resp, err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want zero", got)
	}
}
