package agentloop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_UnofferedToolNameRetainsPriorWorkAndReplay(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, mixedBatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("durable_%t/mixed_%t", durable, mixedBatch), func(t *testing.T) {
				history := &recordingHistory{}
				modelCalls, dispatched, unoffered := 0, 0, 0
				config := ControllerConfig{
					BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
						return model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...), ToolChoice: "auto",
							Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}}}, nil
					},
					CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
						modelCalls++
						if modelCalls == 1 {
							return toolCallResponse("prior-inspection", "read_file", `{}`, model.Usage{}), nil
						}
						if modelCalls > 2 {
							return textResponse("success-looking summary after an unoffered action", model.Usage{}), nil
						}
						response := toolCallResponse("unoffered-shell", "run_shell", `{"command":"echo never-dispatch"}`, model.Usage{})
						if mixedBatch {
							response.Choices[0].Message.ToolCalls = append([]model.ToolCall{{ID: "allowed-batch-prefix", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{}`}}}, response.Choices[0].Message.ToolCalls...)
						}
						return response, nil
					}),
					DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
						outcomes := make([]ToolOutcome, len(calls))
						for i, call := range calls {
							dispatched++
							if call.Function.Name != "read_file" {
								unoffered++
							}
							outcomes[i] = ToolOutcome{Content: "retained completed inspection", Success: true, EffectClass: "readonly"}
						}
						return outcomes, nil
					}),
					History: history,
				}
				if durable {
					ledger, evidence, runID := newDurableControllerStores(t)
					config.RunLedger, config.StepJournal, config.Evidence = ledger, ledger, evidence
					config.RunID, config.SessionID, config.TaskID, config.TurnID = runID, "tool-name-offer", "inspect", "inspect/turn-1"
				}
				attempts := 1
				if durable {
					attempts = 2
				}
				for attempt := 0; attempt < attempts; attempt++ {
					if attempt > 0 {
						history.messages = nil
						config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
							return nil, fmt.Errorf("replay called provider")
						})
						config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
							return nil, fmt.Errorf("replay repeated tools")
						})
					}
					controller, err := NewController(config)
					if err != nil {
						t.Fatal(err)
					}
					result, err := controller.Run(t.Context())
					if err == nil || result == nil || result.Termination.Code != "unoffered_tool_call" || result.CompletionStatus != CompletionIncomplete || !result.Partial {
						t.Fatalf("result=%+v error=%v, want incomplete unoffered tool evidence", result, err)
					}
					if !strings.Contains(result.Content, "run_shell") || modelCalls != 2 || dispatched != 1 || unoffered != 0 {
						t.Fatalf("model=%d dispatched=%d unoffered=%d result=%+v", modelCalls, dispatched, unoffered, result)
					}
					tools := 0
					for _, message := range history.messages {
						if message.Role == "tool" {
							tools++
							if model.ExtractTextContentOrEmpty(message.Content) != "retained completed inspection" {
								t.Fatalf("prior result changed: %+v", message)
							}
						}
					}
					if tools != 1 {
						t.Fatalf("retained tool results=%d, want 1", tools)
					}
				}
			})
		}
	}
}

func TestController_ToolAuthorizationSnapshotsRequestBeforeCaller(t *testing.T) {
	dispatched := false
	controller, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", ToolChoice: "auto", Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}}}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			req.Tools[0]["function"].(map[string]any)["name"] = "run_shell"
			return toolCallResponse("unexpected-shell", "run_shell", `{}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatched = true
			return []ToolOutcome{{Content: "unexpected", Success: true}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(t.Context())
	if err == nil || result == nil || result.Termination.Code != "unoffered_tool_call" || dispatched {
		t.Fatalf("result=%+v err=%v dispatched=%t", result, err, dispatched)
	}
}
