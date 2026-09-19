package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestNewOrchestratorRLMWiresRulesEngineIntoHostBoundRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var request string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"policy-model","context_length":128000,"pricing":{"prompt":"0.000001","completion":"0.000002"},"supported_parameters":["tools"]}]}`)
			return
		}
		body, _ := io.ReadAll(req.Body)
		request = string(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&model.ChatResponse{
			ID:    "cli-policy",
			Model: "policy-model",
			Choices: []model.Choice{{
				Message:      model.Message{Role: "assistant", Content: "review complete"},
				FinishReason: "stop",
			}},
			Usage: model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		})
	}))
	t.Cleanup(server.Close)

	cfg := cliRLMPolicyConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Models.Execution = "openai_compatible/policy-model"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(cliRLMPolicyTool{name: "read_file"})
	registry.Register(cliRLMPolicyTool{name: "write_file"})
	planStore := orchestrator.NewFilePlanStore(t.TempDir())
	plan := &orchestrator.Plan{ID: "cli-policy-plan", Tasks: []orchestrator.Task{{
		ID:          "review-task",
		Title:       "Review",
		Description: "review the implementation without changing files",
		Type:        orchestrator.TaskTypeAnalysis,
		Status:      orchestrator.TaskPending,
	}}}
	if err := planStore.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	runner := newOrchestratorFn(nil, mgr, registry, cfg, nil, planStore)
	if _, err := runner.LoadPlan(plan.ID); err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if err := runner.ExecutePlan(); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	if request == "" {
		t.Fatal("runner did not make a local model request")
	}
	if contains := containsPolicyTool(request, "write_file"); contains {
		t.Fatalf("CLI RLM request advertised write_file despite review policy: %s", request)
	}
	if !containsPolicyTool(request, "read_file") {
		t.Fatalf("CLI RLM request omitted read_file: %s", request)
	}
}

func cliRLMPolicyConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Execution.Mode = config.ExecutionModeRLM
	cfg.RLM.Tiers = map[string]config.RLMTierConfig{
		"light":  {Model: "openai_compatible/policy-model", Models: []string{"openai_compatible/policy-model"}, MaxCostPerMillion: 1_000_000},
		"medium": {Model: "openai_compatible/policy-model", Models: []string{"openai_compatible/policy-model"}, MaxCostPerMillion: 1_000_000},
	}
	return cfg
}

func containsPolicyTool(request, name string) bool {
	var payload struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request), &payload); err != nil {
		return false
	}
	for _, tool := range payload.Tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

type cliRLMPolicyTool struct {
	name string
}

func (t cliRLMPolicyTool) Name() string        { return t.name }
func (t cliRLMPolicyTool) Description() string { return "policy test tool" }
func (t cliRLMPolicyTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (t cliRLMPolicyTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}
