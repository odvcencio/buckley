package rlm

import (
	"context"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// fakeToolLoopProvider keeps requesting a tool for as long as tools are offered,
// and only produces text once tools are disabled (ToolChoice="none") — i.e. the
// forced final turn. This reproduces an agent that never voluntarily concludes.
type fakeToolLoopProvider struct{ modelID string }

func (p *fakeToolLoopProvider) ID() string { return "fake" }

func (p *fakeToolLoopProvider) FetchCatalog() (*model.ModelCatalog, error) {
	return &model.ModelCatalog{Data: []model.ModelInfo{{ID: p.modelID, SupportedParameters: []string{"tools"}}}}, nil
}

func (p *fakeToolLoopProvider) GetModelInfo(id string) (*model.ModelInfo, error) {
	return &model.ModelInfo{ID: p.modelID, SupportedParameters: []string{"tools"}}, nil
}

func (p *fakeToolLoopProvider) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if req.ToolChoice == "none" {
		return &model.ChatResponse{Model: p.modelID, Choices: []model.Choice{{
			Message: model.Message{Content: "FINAL REVIEW: found a real issue in foo.go:42."},
		}}}, nil
	}
	return &model.ChatResponse{Model: p.modelID, Choices: []model.Choice{{
		Message: model.Message{ToolCalls: []model.ToolCall{{
			ID: "c1", Type: "function", Function: model.FunctionCall{Name: "noop", Arguments: "{}"},
		}}},
	}}}, nil
}

func (p *fakeToolLoopProvider) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	ch := make(chan model.StreamChunk)
	ec := make(chan error, 1)
	close(ch)
	close(ec)
	return ch, ec
}

type noopTool struct{}

func (noopTool) Name() string        { return "noop" }
func (noopTool) Description() string  { return "does nothing" }
func (noopTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (noopTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}
func (noopTool) Metadata() tool.ToolMetadata {
	return tool.ToolMetadata{Impact: tool.ImpactReadOnly, Category: tool.CategoryFilesystem}
}

// TestSubAgentForcesFinalAnswerAtIterationCap verifies that when an agent
// exhausts its iteration budget mid-tool-calls, the subagent forces a final
// tools-off turn to produce a real answer instead of degrading the summary into
// a "Executed N tool calls: ..." trace (the bug that made parallel review
// bundles worthless).
func TestSubAgentForcesFinalAnswerAtIterationCap(t *testing.T) {
	cfg := &config.Config{Models: config.ModelConfig{DefaultProvider: "fake"}}
	mgr, err := model.NewManagerWithProviders(cfg, map[string]model.Provider{
		"fake": &fakeToolLoopProvider{modelID: "fake/m"},
	})
	if err != nil {
		t.Fatalf("NewManagerWithProviders: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(noopTool{})

	agent, err := NewSubAgent(SubAgentConfig{
		ID:            "test",
		Model:         "fake/m",
		MaxIterations: 2,
		AllowedTools:  []string{"noop"},
	}, SubAgentDeps{Models: mgr, Registry: registry})
	if err != nil {
		t.Fatalf("NewSubAgent: %v", err)
	}

	res, err := agent.Execute(context.Background(), "review the code and report findings")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Summary != "FINAL REVIEW: found a real issue in foo.go:42." {
		t.Fatalf("expected forced final answer, got trace/other: %q", res.Summary)
	}
}
