package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestRunReviewCommand_CriticCatalogPreflight(t *testing.T) {
	const primary = "fixture/primary"
	const critic = "fixture/missing-critic"
	for _, tc := range []struct {
		name       string
		critic     string
		configured float64
		flags      []string
		catalog    bool
		preflight  bool
		pr         bool
	}{
		{name: "explicit_cap", critic: critic, flags: []string{"--budget", "0.03"}, preflight: true},
		{name: "configured_cap", critic: critic, configured: .03, preflight: true},
		{name: "pull_request", critic: critic, flags: []string{"--budget", "0.03"}, preflight: true, pr: true},
		{name: "uncapped", critic: critic},
		{name: "explicit_no_budget", critic: critic, configured: .03, flags: []string{"--no-budget"}},
		{name: "critic_disabled", flags: []string{"--budget", "0.03"}},
		{name: "same_model", critic: primary, flags: []string{"--budget", "0.03"}},
		{name: "known_critic", critic: critic, catalog: true, flags: []string{"--budget", "0.03"}},
		{name: "native_critic", critic: "codex/gpt-6-luna", flags: []string{"--budget", "0.03"}},
		{name: "project_has_no_critic", critic: critic, flags: []string{"--budget", "0.03", "--project"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newVerifyRepo(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			bin := t.TempDir()
			ghCalls := filepath.Join(t.TempDir(), "gh-called")
			// The baseline must not contact GitHub before it can report the missing critic.
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho called >> \"$FIXTURE_GH_CALLS\"\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FIXTURE_GH_CALLS", ghCalls)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					models := []map[string]any{{"id": primary, "pricing": map[string]string{"prompt": "0.000001", "completion": "0.000001"}}}
					if tc.catalog {
						models = append(models, map[string]any{"id": critic, "pricing": map[string]string{"prompt": "0.000001", "completion": "0.000001"}})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
					return
				}
				calls.Add(1)
				fmt.Fprint(w, `{"id":"fixture-response","model":"fixture/primary","choices":[{"index":0,"message":{"role":"assistant","content":"Incomplete generated fixture review."},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`)
			}))
			defer server.Close()
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "openrouter"
			cfg.Models.Review = primary
			cfg.Buckbot.Model = primary
			cfg.Buckbot.CriticModel = tc.critic
			cfg.Buckbot.PerReviewBudgetUSD = tc.configured
			cfg.Providers.OpenRouter.Enabled = true
			cfg.Providers.OpenRouter.APIKey = "generated-fixture"
			cfg.Providers.OpenRouter.BaseURL = server.URL
			cfg.Providers.OpenAI.Enabled = false
			cfg.Providers.Anthropic.Enabled = false
			cfg.Providers.Google.Enabled = false
			cfg.Providers.Ollama.Enabled = false
			cfg.Providers.Codex.Enabled = false
			if strings.HasPrefix(tc.critic, "codex/") {
				applyReviewCriticModelOverride(cfg, tc.critic)
			}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			previousInit, previousOverride, previousQuiet := initReviewDependenciesFn, modelOverrideFlag, quietMode
			initReviewDependenciesFn = func(string) (*config.Config, *model.Manager, *storage.Store, error) {
				return cfg, mgr, nil, nil
			}
			modelOverrideFlag, quietMode = "", true
			t.Cleanup(func() {
				initReviewDependenciesFn, modelOverrideFlag, quietMode = previousInit, previousOverride, previousQuiet
			})
			args := []string{"--max-turns", "1", "--max-validation-attempts", "1", "--require-verification", "none", "--timeout", "10s", "--cost=false"}
			args = append(args, tc.flags...)
			if tc.pr {
				err = runReviewPRCommand(append(args, "https://github.com/fixture/repository/pull/1"))
			} else {
				err = runReviewCommand(append(args, "--no-interactive"))
			}
			if tc.preflight {
				if err == nil || !strings.Contains(err.Error(), "approval critic") || !strings.Contains(err.Error(), critic) || !strings.Contains(err.Error(), "--critic-model") || !strings.Contains(err.Error(), "no model call was made") {
					t.Errorf("error = %v, want actionable critic error before model dispatch", err)
				}
				if calls.Load() != 0 {
					t.Errorf("model requests = %d, want zero", calls.Load())
				}
				if _, err := os.Stat(ghCalls); !os.IsNotExist(err) {
					t.Errorf("GitHub intake ran before critic preflight: %v", err)
				}
				return
			}
			if calls.Load() == 0 {
				t.Errorf("model requests = 0, want existing primary dispatch preserved; error: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "no model call was made") {
				t.Errorf("unexpected preflight rejection: %v", err)
			}
		})
	}
}
