package agentloop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_ToolBudgetNotice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limit      int
		batch      int
		remaining  int
		failed     bool
		repeatStop bool
	}{
		{name: "exhausted", limit: 1, batch: 1},
		{name: "one remaining", limit: 2, batch: 1, remaining: 1},
		{name: "two remaining", limit: 3, batch: 1, remaining: 2},
		{name: "three remaining", limit: 4, batch: 1, remaining: 3},
		{name: "ample budget stays quiet", limit: 5, batch: 1},
		{name: "one notice after whole batch", limit: 4, batch: 2, remaining: 2},
		{name: "clipped batch stays bounded", limit: 2, batch: 4},
		{name: "failed call consumes budget", limit: 2, batch: 1, remaining: 1, failed: true},
		{name: "failed parallel batch", limit: 4, batch: 2, remaining: 2, failed: true},
		{name: "repeat stop has no further work notice", limit: 3, batch: 2, repeatStop: true},
		{name: "earlier stop in batch", limit: 5, batch: 3, repeatStop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := &recordingHistory{}
			modelCalls, dispatched := 0, 0
			exactLimit := 100
			if tc.repeatStop {
				exactLimit = 2
			}
			ctrl, err := NewController(ControllerConfig{
				Governor:       New(Config{MaxToolCalls: tc.limit, MaxRounds: 50, ExactRepeatLimit: exactLimit, OutcomeRepeatLimit: 100}),
				FinalizeOnStop: true,
				History:        history,
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
				},
				CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
					modelCalls++
					if modelCalls == 1 {
						response := toolCallResponse("call-0", "read_file", `{}`, model.Usage{})
						for i := 1; i < tc.batch; i++ {
							response.Choices[0].Message.ToolCalls = append(response.Choices[0].Message.ToolCalls,
								model.ToolCall{ID: fmt.Sprintf("call-%d", i), Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{}`}})
						}
						return response, nil
					}
					notices := 0
					for _, message := range req.Messages {
						content, _ := message.Content.(string)
						if !strings.Contains(content, "Harness budget:") {
							continue
						}
						notices++
						if message.Role != "tool" || message.ToolCallID != fmt.Sprintf("call-%d", min(tc.batch, tc.limit)-1) {
							t.Errorf("notice must follow last admitted tool result: %+v", message)
						}
						if !strings.Contains(content, fmt.Sprintf("%d tool calls remain", tc.remaining)) || !strings.Contains(content, "verification") {
							t.Errorf("wrong budget or missing verification guidance: %q", content)
						}
						if !strings.HasPrefix(content, "observed outcome") {
							t.Errorf("notice replaced actual tool evidence: %q", content)
						}
					}
					want := 0
					if tc.remaining > 0 {
						want = 1
					}
					if notices != want {
						t.Errorf("budget notices = %d, want %d", notices, want)
					}
					return textResponse("bounded handoff", model.Usage{}), nil
				}),
				DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
					outcomes := make([]ToolOutcome, len(calls))
					for i := range calls {
						outcomes[i] = ToolOutcome{Content: "observed outcome", Success: !tc.failed}
					}
					dispatched += len(calls)
					return outcomes, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := ctrl.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if modelCalls != 2 || dispatched != min(tc.batch, tc.limit) || result.ToolCalls != dispatched {
				t.Fatalf("model calls=%d dispatched=%d result=%+v", modelCalls, dispatched, result)
			}
		})
	}
}
