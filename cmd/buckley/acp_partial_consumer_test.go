package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

func newACPPartialConsumerManager(t *testing.T, handler http.HandlerFunc) (*config.Config, *model.Manager) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "acp-test/no-tools-model"
	cfg.Models.Curated = []string{"acp-test/no-tools-model"}

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return cfg, mgr
}
