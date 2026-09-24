package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_StopReasonSurvivesFailedFinalization(t *testing.T) {
	var stops []Termination
	cfg := DefaultConfig()
	cfg.MaxToolCalls = 1
	ctrl, err := NewController(ControllerConfig{
		Governor: New(cfg), FinalizeOnStop: true,
		OnStop: func(stop Termination) { stops = append(stops, stop) },
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test"}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			if req.ToolChoice == "none" {
				if len(stops) != 1 || stops[0].Kind != "tool_call_limit" {
					t.Fatalf("stop was not reported before finalization: %+v", stops)
				}
				return nil, errors.New("PRIVATE_PROVIDER_DETAIL")
			}
			return toolCallResponse("call", "test_tool", "{}", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Success: true, Content: "read evidence"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) || len(stops) != 1 {
		t.Fatalf("result=%+v err=%v stops=%+v", result, err, stops)
	}
	if !strings.Contains(incomplete.StopReason, "1-call harness limit") || strings.Contains(incomplete.StopReason, "PRIVATE") {
		t.Fatalf("stop reason=%q", incomplete.StopReason)
	}
}
