package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/coordination/coordinator"
	"m31labs.dev/buckley/pkg/coordination/events"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/storage"
)

func TestBuildRLMRuntimeAppliesServerRulesEngineCoordinatorPolicy(t *testing.T) {
	for _, tc := range []struct {
		name             string
		withEngine       bool
		policyToolRounds int
		wantModelCalls   int32
	}{
		{name: "nil engine preserves configured round limit", policyToolRounds: 1, wantModelCalls: 2},
		{name: "server engine applies coordinator budget policy", withEngine: true, policyToolRounds: 5, wantModelCalls: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				call := calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if call <= int32(tc.policyToolRounds) {
					_ = json.NewEncoder(w).Encode(coordinatorPolicyToolResponse(call))
					return
				}
				_ = json.NewEncoder(w).Encode(coordinatorPolicyFinalResponse(call))
			}))
			t.Cleanup(httpServer.Close)

			cfg := config.DefaultConfig()
			cfg.Worktrees.RootPath = t.TempDir()
			cfg.Providers.OpenAI.Enabled = true
			cfg.Providers.OpenAI.APIKey = "test-key"
			cfg.Providers.OpenAI.BaseURL = httpServer.URL
			cfg.Models.DefaultProvider = "openai"
			cfg.Models.Execution = "openai/gpt-4o"
			cfg.RLM.Coordinator.Model = "openai/gpt-4o"
			cfg.RLM.Coordinator.MaxIterations = 1
			cfg.RLM.Coordinator.MaxTokensBudget = 1_000

			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			if err := mgr.Initialize(); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			store, err := storage.New(filepath.Join(t.TempDir(), "server-policy.db"))
			if err != nil {
				t.Fatalf("storage.New: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), events.NewInMemoryStore())
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}

			var options []Option
			if tc.withEngine {
				engine, err := rules.NewEngine()
				if err != nil {
					t.Fatalf("NewEngine: %v", err)
				}
				options = append(options, WithRulesEngine(engine))
			}
			srv, err := NewServer(coord, mgr, cfg, store, options...)
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			runtime, cleanup, err := srv.buildRLMRuntime("policy-session", "policy-agent")
			if err != nil {
				t.Fatalf("buildRLMRuntime: %v", err)
			}
			defer cleanup()

			answer, err := runtime.Execute(context.Background(), "coordinate a policy test")
			if err != nil {
				t.Fatalf("Runtime.Execute: %v", err)
			}
			if answer == nil || !answer.Ready || answer.Content != "policy synthesis" {
				t.Fatalf("answer = %#v, want ready policy synthesis", answer)
			}
			if got := calls.Load(); got != tc.wantModelCalls {
				t.Fatalf("model calls = %d, want %d", got, tc.wantModelCalls)
			}
		})
	}
}

func coordinatorPolicyToolResponse(call int32) *model.ChatResponse {
	args, _ := json.Marshal(map[string]any{
		"content":    fmt.Sprintf("draft %d", call),
		"ready":      false,
		"confidence": 0.1,
	})
	return &model.ChatResponse{
		ID:    fmt.Sprintf("policy-%d", call),
		Model: "gpt-4o",
		Choices: []model.Choice{{
			Message: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
				ID:   "set-answer",
				Type: "function",
				Function: model.FunctionCall{
					Name:      "set_answer",
					Arguments: string(args),
				},
			}}},
			FinishReason: "tool_calls",
		}},
		Usage: model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
}

func coordinatorPolicyFinalResponse(call int32) *model.ChatResponse {
	return &model.ChatResponse{
		ID:    fmt.Sprintf("policy-%d", call),
		Model: "gpt-4o",
		Choices: []model.Choice{{
			Message:      model.Message{Role: "assistant", Content: "policy synthesis"},
			FinishReason: "stop",
		}},
		Usage: model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
}
