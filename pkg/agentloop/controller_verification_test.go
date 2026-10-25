package agentloop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_VerificationRepeatPreservesCompletedResultsAndReplay(t *testing.T) {
	ledger, evidence, runID := newDurableControllerStores(t)
	history := &recordingHistory{}
	providerCalls, dispatches := 0, 0
	patterns := []string{"TestWAV", "TestLoudness", "TestVerify", "TestWAV", "TestVerifyDuration", "TestWAV"}
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			providerCalls++
			if req.ToolChoice == "none" {
				if len(req.Tools) != 0 {
					return nil, fmt.Errorf("final synthesis offered tools")
				}
				return textResponse("The completed checks remain available. Artifact encoding still needs a shell command.", model.Usage{}), nil
			}
			if providerCalls > len(patterns) {
				return nil, fmt.Errorf("model continued after the repeating verification")
			}
			return toolCallResponse(fmt.Sprintf("call-%d", providerCalls), "run_tests", fmt.Sprintf(`{"path":"./render","pattern":%q}`, patterns[providerCalls-1]), model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatches++
			return []ToolOutcome{{Content: fmt.Sprintf("PASS execution=%d duration=0.%ds cached=%t", dispatches, dispatches, dispatches > 3), Success: true,
				EffectClass: "readonly", StateObserved: true, VerificationObserved: true, VerificationPassed: true}}, nil
		}),
		History: history, RunLedger: ledger, Evidence: evidence, StepJournal: ledger,
		RunID: runID, SessionID: "verification-loop", TaskID: "verify", TurnID: "verify/turn-1", FinalizeOnStop: true,
	}
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay_%t", replay), func(t *testing.T) {
			if replay {
				history.messages = nil
				config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					return nil, fmt.Errorf("replay called provider")
				})
				config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					return nil, fmt.Errorf("replay repeated tool effects")
				})
			}
			ctrl, err := NewController(config)
			if err != nil {
				t.Fatal(err)
			}
			result, err := ctrl.Run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if result.Termination.Kind != "verification_repeat" || !result.Termination.FinalizationAttempted || dispatches != len(patterns) {
				t.Fatalf("result=%+v dispatches=%d", result, dispatches)
			}
			completed := 0
			for _, message := range history.messages {
				if message.Role == "tool" {
					completed++
					if !strings.Contains(model.ExtractTextContentOrEmpty(message.Content), fmt.Sprintf("PASS execution=%d", completed)) {
						t.Fatalf("completed result %d lost: %+v", completed, message)
					}
				}
			}
			if completed != len(patterns) {
				t.Fatalf("retained %d results, want %d", completed, len(patterns))
			}
		})
	}
}
