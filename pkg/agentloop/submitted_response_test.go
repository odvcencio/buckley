package agentloop

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_SubmittedResponseCompletion(t *testing.T) {
	for _, name := range []string{"ready", "verified mutation", "last allowance", "disabled", "not ready", "empty", "invalid", "missing change", "missing verification", "failed verification", "changed after verification", "state observation failed", "failed tool", "clipped", "dispatch interrupted", "observer failed", "cancel observer", "cancel reader", "cancel validator", "safety fuse", "shadow fuse", "partial provider", "truncated provider", "unknown price"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, reads, validations := 0, 0, 0
			history := &recordingHistory{}
			contract := &CompletionContract{
				TaskIntent: ReadOnlyIntent,
				SubmittedResponse: func() (string, bool) {
					reads++
					switch name {
					case "not ready":
						return "", false
					case "empty":
						return " \n", true
					case "invalid":
						return "invalid", true
					case "cancel reader":
						cancel()
					}
					return "published", true
				},
				ValidateFinalResponse: func(text string) error {
					validations++
					if name == "cancel validator" {
						cancel()
					}
					if text == "invalid" {
						return errors.New("invalid submitted output")
					}
					return nil
				},
			}
			if name == "disabled" {
				contract.SubmittedResponse = nil
			}
			outcomes := []ToolOutcome{{Success: true, Content: "accepted"}}
			switch name {
			case "verified mutation", "missing change", "missing verification", "failed verification", "changed after verification", "state observation failed":
				contract.TaskIntent = MutationIntent
				contract.RequireObservableChange = true
				contract.RequirePostChangeVerification = true
				if name != "missing change" {
					outcomes[0].StateObserved, outcomes[0].StateChanged = true, true
				}
				if name == "failed verification" {
					outcomes = append(outcomes, ToolOutcome{Success: true, VerificationObserved: true})
				}
				if name == "verified mutation" {
					outcomes = append(outcomes, ToolOutcome{Success: true, VerificationObserved: true, VerificationPassed: true})
				}
				if name == "changed after verification" {
					outcomes = append(outcomes, ToolOutcome{Success: true, VerificationObserved: true, VerificationPassed: true}, ToolOutcome{Success: true, StateObserved: true, StateChanged: true})
				}
				if name == "state observation failed" {
					outcomes[0].StateObservationFailed = true
					outcomes[0].StateObservationError = "snapshot failed"
				}
			case "failed tool":
				outcomes[0].Success = false
			}
			govCfg := DefaultConfig()
			if name == "last allowance" || name == "clipped" {
				govCfg.MaxToolCalls = 1
			}
			cfg := ControllerConfig{
				Governor: New(govCfg), History: history, CompletionContract: contract, MaxModelRequests: 2,
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 10}), nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					calls++
					if calls > 1 {
						return textResponse("ordinary", model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}), nil
					}
					response := toolCallResponse("first", "test_tool", "{}", model.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3})
					for i := 1; i < len(outcomes); i++ {
						response.Choices[0].Message.ToolCalls = append(response.Choices[0].Message.ToolCalls, model.ToolCall{ID: "extra-" + string(rune('a'+i)), Function: model.FunctionCall{Name: "test_tool", Arguments: "{}"}})
					}
					if name == "clipped" || name == "dispatch interrupted" {
						response.Choices[0].Message.ToolCalls = append(response.Choices[0].Message.ToolCalls, model.ToolCall{ID: "omitted", Function: model.FunctionCall{Name: "test_tool", Arguments: "{}"}})
					}
					if name == "partial provider" {
						return response, errors.New("provider failed after material")
					}
					if name == "truncated provider" {
						response.Choices[0].FinishReason = "length"
					}
					return response, nil
				}),
				DispatchTools: ToolDispatcherFunc(func(_ context.Context, requested []model.ToolCall) ([]ToolOutcome, error) {
					if name == "dispatch interrupted" {
						return outcomes, errors.New("unresolved suffix")
					}
					return outcomes[:len(requested)], nil
				}),
				ObserveToolOutcome: func(context.Context, model.ToolCall, ToolOutcome, bool) error {
					if name == "cancel observer" {
						cancel()
					}
					if name == "observer failed" {
						return errors.New("evidence observer failed")
					}
					return nil
				},
			}
			if name == "last allowance" {
				cfg.MaxModelRequests, cfg.FinalizeOnStop = 1, true
			}
			if name == "clipped" {
				cfg.FinalizeOnStop = true
			}
			if name == "safety fuse" || name == "shadow fuse" {
				mode := ModeDynamic
				if name == "shadow fuse" {
					mode = ModeShadow
				}
				cfg.Progress = NewProgressController(mode, "test", Fuses{ToolExecutions: 1})
			}
			if name == "unknown price" {
				cfg.MaxCostUSD = 1
				cfg.CostForUsage = func(model.Usage) (float64, error) { return 0, errors.New("unknown price") }
			}
			controller, err := NewController(cfg)
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.Run(ctx)
			switch name {
			case "ready", "verified mutation", "last allowance", "shadow fuse":
				if err != nil || result.CompletionStatus != CompletionConclusive || result.Content != "published" || calls != 1 || reads != 1 || validations != 1 || result.ModelRequests != 1 || result.Usage.TotalTokens != 3 || result.Termination.FinalizationAttempted || result.FinishReason != "" {
					t.Fatalf("submitted result: result=%+v calls=%d reads=%d validations=%d err=%v", result, calls, reads, validations, err)
				}
				last := len(history.messages) - 1
				if len(history.messages) != 2+len(outcomes) || history.messages[1].Role != "tool" || history.messages[last].Role != "assistant" || history.messages[last].Content != "published" {
					t.Fatalf("incomplete result transcript: %+v", history.messages)
				}
			case "disabled", "not ready", "empty", "invalid", "failed tool", "clipped":
				if err != nil || calls != 2 || result.Content != "ordinary" {
					t.Fatalf("normal fallback: result=%+v calls=%d reads=%d err=%v", result, calls, reads, err)
				}
				if (name == "disabled" || name == "failed tool" || name == "clipped") && reads != 0 {
					t.Fatalf("ineligible tool round queried submitted result %d times", reads)
				}
				if name == "clipped" && !result.Termination.FinalizationAttempted {
					t.Fatal("clipped batch bypassed normal budget finalization")
				}
			case "missing change", "missing verification", "failed verification", "changed after verification", "state observation failed":
				if err == nil || result.CompletionStatus == CompletionConclusive || reads != 0 || validations != 0 || calls != 2 {
					t.Fatalf("progress gate bypass: result=%+v calls=%d reads=%d validations=%d err=%v", result, calls, reads, validations, err)
				}
			case "unknown price":
				if err == nil || calls != 0 || reads != 0 || result.CompletionStatus == CompletionConclusive {
					t.Fatalf("unknown pricing gate bypass: result=%+v calls=%d reads=%d err=%v", result, calls, reads, err)
				}
			default:
				if result.CompletionStatus == CompletionConclusive || calls != 1 {
					t.Fatalf("safety/error bypass: result=%+v calls=%d reads=%d err=%v", result, calls, reads, err)
				}
				if name != "safety fuse" && name != "truncated provider" && err == nil {
					t.Fatal("lost interruption/provider error")
				}
				if name != "safety fuse" && name != "cancel reader" && name != "cancel validator" && reads != 0 {
					t.Fatalf("interrupted round queried submitted result %d times", reads)
				}
				if name == "safety fuse" && result.Termination.Kind != "emergency_fuse" {
					t.Fatalf("applied safety fuse bypassed: %+v", result)
				}
			}
		})
	}
}
