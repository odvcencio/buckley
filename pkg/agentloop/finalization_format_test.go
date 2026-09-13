package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestFinalizationPreservesRequiredOutputFormat(t *testing.T) {
	for _, cause := range []error{nil, errors.New("tool calls disabled")} {
		original := []model.Message{{Role: "system", Content: "Return a JSON source handoff when tools are disabled."}, {Role: "user", Content: "gather source"}}
		parallel := true
		c := &Controller{cfg: ControllerConfig{FinalizationInstruction: "CALLER_OUTPUT_CONTRACT", BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test", Messages: original, Tools: []map[string]any{{"type": "function"}}, ToolChoice: "auto", ParallelToolCalls: &parallel}, nil
		}}}
		req, err := c.buildFinalizationRequest(context.Background(), 1, "tool budget exhausted", cause)
		if err != nil {
			t.Fatal(err)
		}
		if len(req.Tools) != 0 || req.ToolChoice != "none" || req.ParallelToolCalls != nil {
			t.Fatal("format fallback re-enabled tools")
		}
		last := model.ExtractTextContentOrEmpty(req.Messages[len(req.Messages)-1].Content)
		if !strings.Contains(last, "required output format") || !strings.Contains(last, "tools-disabled fallback") || strings.Contains(last, "plain text only") {
			t.Fatalf("finalization overrides output contract: %s", last)
		}
		if !strings.HasSuffix(last, "CALLER_OUTPUT_CONTRACT") {
			t.Fatal("caller output contract missing from finalization request")
		}
		if len(original) != 2 || model.ExtractTextContentOrEmpty(original[1].Content) != "gather source" {
			t.Fatal("request construction changed history")
		}
	}
}
