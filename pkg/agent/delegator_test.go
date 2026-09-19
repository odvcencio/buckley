package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	pkgcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

func TestNewDelegator(t *testing.T) {
	mgr := &model.Manager{}
	registry := tool.NewRegistry()
	specs := map[string]*pkgcontext.SubAgentSpec{
		"test-agent": {
			Name:        "test-agent",
			Description: "Test agent",
			Tools:       []string{"read_file"},
		},
	}

	delegator := NewDelegator(mgr, registry, specs)

	if delegator == nil {
		t.Fatal("NewDelegator returned nil")
	}

	if delegator.modelMgr != mgr {
		t.Error("Model manager not set correctly")
	}

	if delegator.registry != registry {
		t.Error("Registry not set correctly")
	}

	if len(delegator.specs) != 1 {
		t.Errorf("Expected 1 spec, got %d", len(delegator.specs))
	}
}

func TestListAgents(t *testing.T) {
	specs := map[string]*pkgcontext.SubAgentSpec{
		"agent1": {Name: "agent1"},
		"agent2": {Name: "agent2"},
		"agent3": {Name: "agent3"},
	}

	delegator := NewDelegator(nil, nil, specs)
	agents := delegator.ListAgents()

	if len(agents) != 3 {
		t.Errorf("Expected 3 agents, got %d", len(agents))
	}

	// Check all agents are present
	agentMap := make(map[string]bool)
	for _, name := range agents {
		agentMap[name] = true
	}

	for _, expected := range []string{"agent1", "agent2", "agent3"} {
		if !agentMap[expected] {
			t.Errorf("Agent %s not found in list", expected)
		}
	}
}

func TestGetSpec(t *testing.T) {
	spec := &pkgcontext.SubAgentSpec{
		Name:        "test-agent",
		Description: "A test agent",
		Model:       "gpt-4",
		Tools:       []string{"read_file", "write_file"},
		MaxCost:     1.50,
	}

	specs := map[string]*pkgcontext.SubAgentSpec{
		"test-agent": spec,
	}

	delegator := NewDelegator(nil, nil, specs)

	// Test existing agent
	gotSpec, ok := delegator.GetSpec("test-agent")
	if !ok {
		t.Fatal("GetSpec returned false for existing agent")
	}

	if gotSpec.Name != spec.Name {
		t.Errorf("Name mismatch: got %s, want %s", gotSpec.Name, spec.Name)
	}

	if gotSpec.Description != spec.Description {
		t.Errorf("Description mismatch: got %s, want %s", gotSpec.Description, spec.Description)
	}

	if gotSpec.Model != spec.Model {
		t.Errorf("Model mismatch: got %s, want %s", gotSpec.Model, spec.Model)
	}

	if gotSpec.MaxCost != spec.MaxCost {
		t.Errorf("MaxCost mismatch: got %f, want %f", gotSpec.MaxCost, spec.MaxCost)
	}

	// Test non-existent agent
	_, ok = delegator.GetSpec("non-existent")
	if ok {
		t.Error("GetSpec returned true for non-existent agent")
	}
}

func TestFilterTools(t *testing.T) {
	registry := tool.NewRegistry()

	specs := map[string]*pkgcontext.SubAgentSpec{
		"limited-agent": {
			Name:  "limited-agent",
			Tools: []string{"read_file", "list_directory"},
		},
		"full-agent": {
			Name:  "full-agent",
			Tools: []string{}, // Empty means all tools
		},
	}

	delegator := NewDelegator(nil, registry, specs)

	// Test filtering to specific tools
	limitedSpec := specs["limited-agent"]
	filtered := delegator.filterTools(limitedSpec.Tools)

	// Filtered registry should be a new instance with only allowed tools
	if filtered == registry {
		t.Error("Expected filtered registry to be a new instance, not the original")
	}

	// Check that only allowed tools are present
	allowedCount := len(limitedSpec.Tools)
	actualCount := filtered.Count()
	if actualCount != allowedCount {
		t.Errorf("Expected %d tools in filtered registry, got %d", allowedCount, actualCount)
	}

	// Verify each allowed tool is present
	for _, toolName := range limitedSpec.Tools {
		if _, ok := filtered.Get(toolName); !ok {
			t.Errorf("Expected tool %s to be in filtered registry", toolName)
		}
	}

	// Test no filtering (empty tools list)
	fullSpec := specs["full-agent"]
	fullRegistry := delegator.filterTools(fullSpec.Tools)

	if fullRegistry != registry {
		t.Error("Empty tools list should return full registry")
	}
}

func TestBuildToolDefinitions(t *testing.T) {
	registry := tool.NewRegistry()
	delegator := NewDelegator(nil, registry, nil)

	tools := delegator.buildToolDefinitions(registry)

	// Should have all built-in tools
	if len(tools) != registry.Count() {
		t.Errorf("Expected %d tools, got %d", registry.Count(), len(tools))
	}

	// Each tool should follow OpenAI function schema
	for _, toolDef := range tools {
		if toolDef["type"] != "function" {
			t.Errorf("Expected tool type 'function', got %v", toolDef["type"])
		}

		functionDef, ok := toolDef["function"].(map[string]any)
		if !ok {
			t.Fatalf("Tool definition missing function metadata: %#v", toolDef)
		}

		if _, ok := functionDef["name"]; !ok {
			t.Error("Tool definition missing 'name' field")
		}

		if _, ok := functionDef["description"]; !ok {
			t.Error("Tool definition missing 'description' field")
		}

		if _, ok := functionDef["parameters"]; !ok {
			t.Error("Tool definition missing 'parameters' field")
		}
	}
}

func TestDelegate_NonExistentAgent(t *testing.T) {
	delegator := NewDelegator(nil, nil, map[string]*pkgcontext.SubAgentSpec{})

	_, err := delegator.Delegate(context.Background(), "non-existent", "test task")
	if err == nil {
		t.Error("Expected error for non-existent agent")
	}

	if err.Error() != "sub-agent not found: non-existent" {
		t.Errorf("Unexpected error message: %s", err.Error())
	}
}

func TestDelegate_RetainsPublicResponseOnProviderError(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	delegator := NewDelegator(mgr, tool.NewEmptyRegistry(), map[string]*pkgcontext.SubAgentSpec{
		"helper": {
			Name:  "helper",
			Model: "gpt-4o",
		},
	})
	delegator.chatCompletion = func(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
		if req.Model != "gpt-4o" {
			t.Fatalf("request model = %q, want gpt-4o", req.Model)
		}
		return &model.ChatResponse{
			ID:    "resp-delegate-partial",
			Model: "gpt-4o",
			Choices: []model.Choice{{
				Message: model.Message{
					Role:             "assistant",
					Content:          "<think>private-reasoning-sentinel</think>public retained draft",
					Reasoning:        "message-reasoning-sentinel",
					ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: "reasoning-detail-sentinel"}},
				},
				FinishReason: "stop",
			}},
			Usage:        model.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7},
			UsagePresent: true,
			ExecutionIdentity: &model.ExecutionIdentity{
				RequestedModel: "gpt-4o",
				SelectedModel:  "gpt-4o",
				ProviderID:     "fake",
				ResponseModel:  "gpt-4o",
				ResponseID:     "resp-delegate-partial",
			},
		}, errors.New("provider secret: do not expose")
	}

	result, err := delegator.Delegate(context.Background(), "helper", "inspect this")
	if !errors.Is(err, errDelegationIncomplete) {
		t.Fatalf("error = %v, want delegation incomplete sentinel", err)
	}
	if result == nil {
		t.Fatal("result is nil, want retained partial response")
	}
	if result.Success || !result.Incomplete {
		t.Fatalf("success/incomplete = %v/%v, want failed incomplete result", result.Success, result.Incomplete)
	}
	if result.Output != "public retained draft" {
		t.Fatalf("output = %q, want public draft with private reasoning stripped", result.Output)
	}
	if strings.Contains(result.Output, "private-reasoning-sentinel") || strings.Contains(result.ErrorMessage, "provider secret") {
		t.Fatalf("result leaked private/raw provider material: %+v", result)
	}
	serialized, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, forbidden := range []string{"private-reasoning-sentinel", "message-reasoning-sentinel", "reasoning-detail-sentinel", "provider secret"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("serialized result contains forbidden sentinel %q: %s", forbidden, string(serialized))
		}
	}
	if result.Usage == nil || result.Usage.Input != 3 || result.Usage.Output != 4 || result.Usage.ReportedTotal != 7 || !result.Usage.UsageEvidencePresent {
		t.Fatalf("usage = %+v, want retained response usage evidence", result.Usage)
	}
	if len(result.ModelExecutions) != 1 || result.ModelExecutions[0].ResponseID != "resp-delegate-partial" {
		t.Fatalf("model executions = %+v, want retained response identity", result.ModelExecutions)
	}
}
