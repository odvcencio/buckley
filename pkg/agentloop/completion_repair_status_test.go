package agentloop

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_CompletionRepairClearsResolvedTermination(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		name := "model response"
		if submitted {
			name = "submitted response"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			var events []LifecycleEvent
			contract := &CompletionContract{
				TaskIntent: ReadOnlyIntent, MaxRepairAttempts: 1,
				ValidateFinalResponse: func(text string) error {
					if text != "accepted" {
						return errors.New("required evidence missing")
					}
					return nil
				},
			}
			if submitted {
				contract.SubmittedResponse = func() (string, bool) { return "accepted", true }
			}
			controller, err := NewController(ControllerConfig{
				CompletionContract: contract,
				History:            &recordingHistory{},
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					calls++
					if calls == 1 {
						return textResponse("premature", model.Usage{}), nil
					}
					if submitted {
						return toolCallResponse("submit", "test_tool", "{}", model.Usage{}), nil
					}
					return textResponse("accepted", model.Usage{}), nil
				}),
				DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					return []ToolOutcome{{Success: true, Content: "accepted"}}, nil
				}),
				LifecycleObserver: func(event LifecycleEvent) { events = append(events, event) },
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.Run(context.Background())
			if err != nil || result.CompletionStatus != CompletionConclusive || result.Content != "accepted" || calls != 2 {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}
			if result.Termination != (Termination{}) || result.FinishReason != "" {
				t.Errorf("repaired answer retained failure status: %+v", result)
			}
			rejections, endings := 0, 0
			for _, event := range events {
				if event.StopReason == "completion_contract_rejected" {
					rejections++
				}
				if event.Type == LifecycleTurnEnd {
					endings++
					if event.Status != string(CompletionConclusive) || event.StopReason != "" || event.FinishReason != "" || event.Error != "" {
						t.Errorf("repaired lifecycle status: %+v", event)
					}
				}
			}
			if rejections != 1 || endings != 1 {
				t.Fatalf("rejection events=%d end events=%d", rejections, endings)
			}
		})
	}
}
