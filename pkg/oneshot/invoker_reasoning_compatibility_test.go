package oneshot

import (
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestRequestProfile_ForcedToolReasoningCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name          string
		choice        string
		tools         bool
		requireTool   bool
		compatibility bool
		wantDisabled  bool
	}{
		{"required", "required", true, false, true, true},
		{"named tool", "submit_commit", true, false, true, true},
		{"profile forces tool", "auto", true, true, true, true},
		{"automatic tools", "auto", true, false, true, false},
		{"no tool selection", "none", true, false, true, false},
		{"default tool selection", "", true, false, true, false},
		{"free text", "", false, true, true, false},
		{"unaffected provider", "required", true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasoning := &model.ReasoningConfig{Effort: "high", MaxTokens: 2048, Enabled: boolPtr(true)}
			inv := NewInvoker(InvokerConfig{RequestProfile: RequestProfile{
				Reasoning:                      reasoning,
				RequireTool:                    tc.requireTool,
				DisableReasoningForForcedTools: tc.compatibility,
			}})
			req := model.ChatRequest{ToolChoice: tc.choice, Reasoning: inv.requestReasoning()}
			if tc.tools {
				req.Tools = []map[string]any{{"type": "function"}}
			}
			got := inv.applyRequestProfile(req)
			wantChoice := tc.choice
			if tc.requireTool && tc.tools {
				wantChoice = "required"
			}
			if got.ToolChoice != wantChoice || !reflect.DeepEqual(got.Tools, req.Tools) {
				t.Fatalf("tool contract changed: %+v", got)
			}
			wantReasoning := reasoning
			if tc.wantDisabled {
				wantReasoning = &model.ReasoningConfig{Enabled: boolPtr(false)}
			}
			if !reflect.DeepEqual(got.Reasoning, wantReasoning) {
				t.Fatalf("reasoning = %+v, want %+v", got.Reasoning, wantReasoning)
			}
			if !reflect.DeepEqual(inv.requestReasoning(), reasoning) || !*reasoning.Enabled {
				t.Fatal("compatibility changed the shared reasoning profile")
			}
		})
	}
}
