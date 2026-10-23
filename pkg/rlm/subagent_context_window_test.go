package rlm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestSubAgentExecute_ControllerUsesResolvedContextWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window int
	}{
		{"large model window", 131072},
		{"small selected route", 8192},
		{"unknown window fallback", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			steps := make([]scriptedSubAgentModelStep, 0, 9)
			for round := range 8 {
				calls := make([]model.ToolCall, 4)
				for i := range calls {
					index := round*4 + i
					path := fmt.Sprintf("source-%02d.txt", index)
					body := strings.Repeat("prefix filler prefix filler prefix filler prefix filler\n", 70) + fmt.Sprintf("\nUNIQUE_FACT_%02d=observed\n", index) + strings.Repeat("suffix filler suffix filler suffix filler suffix filler\n", 70)
					if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
					calls[i] = model.ToolCall{ID: fmt.Sprintf("read-%02d", index), Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: fmt.Sprintf(`{"path":%q}`, path)}}
				}
				steps = append(steps, scriptedSubAgentModelStep{resp: &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}}}})
			}
			steps = append(steps, scriptedSubAgentModelStep{resp: &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}}})
			client := &routedContextSubAgentModelClient{
				scriptedSubAgentModelClient: scriptedSubAgentModelClient{steps: steps},
				route:                       model.ModelRoute{RequestedModel: "alias/model", SelectedModel: "selected/model", ProviderID: "selected"},
				contextLength:               tc.window,
				legacyContextLength:         1 << 20,
			}
			registry := tool.NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.SetWorkDir(root)
			t.Cleanup(func() { _ = registry.Close() })
			agent, err := NewSubAgent(SubAgentConfig{ID: "context-test", Model: "alias/model", MaxIterations: 10, MaxToolCalls: 33, MaxOutputTokens: 512, AllowedTools: []string{"read_file"}}, SubAgentDeps{Models: &model.Manager{}, Registry: registry})
			if err != nil {
				t.Fatal(err)
			}
			agent.client = client
			result, err := agent.Execute(t.Context(), "Inspect all source files and report their observed facts.")
			if err != nil || result == nil || result.Summary != "done" || len(result.ToolCalls) != 32 || len(client.requests) != 9 {
				t.Fatalf("unexpected execution: err=%v result=%+v requests=%d", err, result, len(client.requests))
			}
			for i, call := range result.ToolCalls {
				if !call.Success || !strings.Contains(call.Result, fmt.Sprintf("UNIQUE_FACT_%02d=observed", i)) {
					t.Fatalf("fixture read %d did not observe its source fact", i)
				}
			}
			for _, req := range client.requests {
				if tc.window > 0 && model.EstimateRequestTokens(req).Total+req.MaxTokens > tc.window {
					t.Fatalf("request exceeds selected context window: %d > %d", model.EstimateRequestTokens(req).Total+req.MaxTokens, tc.window)
				}
			}
			last := client.requests[len(client.requests)-1]
			contents := fmt.Sprint(last.Messages)
			if tc.window == 131072 {
				for i := range 32 {
					if fact := fmt.Sprintf("UNIQUE_FACT_%02d=observed", i); !strings.Contains(contents, fact) {
						t.Fatalf("model window has room but request lost %q", fact)
					}
				}
			} else if strings.Contains(contents, "UNIQUE_FACT_00=observed") {
				t.Fatal("small/unknown window did not compact old source evidence")
			}
		})
	}
}
