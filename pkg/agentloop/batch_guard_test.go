package agentloop

import (
	"context"
	"fmt"
	"m31labs.dev/buckley/pkg/model"
	"testing"
)

func TestController_BatchedFailuresStopBeforeSuffixExecutes(t *testing.T) {
	executed := 0
	history := &recordingHistory{}
	controller, err := NewController(batchGuardConfig(history, nil, &executed))
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Termination.Kind != "exact_repeat" || executed != 3 {
		t.Fatalf("executed=%d stop=%s; want stop after third result before suffix effects", executed, result.Termination.Kind)
	}
}

func batchGuardConfig(history *recordingHistory, parallel *bool, executed *int) ControllerConfig {
	return ControllerConfig{
		FinalizeOnStop: true, History: history,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "fixture-model", ParallelToolCalls: parallel, Messages: append([]model.Message(nil), history.messages...), ToolChoice: "auto", Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "run_tests"}}}}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			if req.ToolChoice == "none" {
				return textResponse("The requested check is unavailable; no verification was established.", model.Usage{}), nil
			}
			response := &model.ChatResponse{Choices: []model.Choice{{FinishReason: "tool_calls", Message: model.Message{Role: "assistant"}}}}
			for i := range 5 {
				response.Choices[0].Message.ToolCalls = append(response.Choices[0].Message.ToolCalls, model.ToolCall{ID: fmt.Sprintf("unavailable-%d", i), Type: "function", Function: model.FunctionCall{Name: "run_tests", Arguments: `{"path":"./examples/turbofan","timeout_seconds":300}`}})
			}
			return response, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			results := make([]ToolOutcome, len(calls))
			for i := range calls {
				(*executed)++
				results[i] = ToolOutcome{Content: "No test framework was detected; verification unavailable.", Success: false, EffectClass: "readonly", StateObserved: true}
			}
			return results, nil
		}),
	}
}

func testParallelToolRequest(req model.ChatRequest, names ...string) model.ChatRequest {
	parallel := true
	req = testToolRequest(req, names...)
	req.ParallelToolCalls = &parallel
	return req
}
