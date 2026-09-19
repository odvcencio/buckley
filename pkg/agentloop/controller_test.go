package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

// recordingHistory captures every message Controller appends, in order.
type recordingHistory struct {
	messages []model.Message
}

func (h *recordingHistory) Append(msg model.Message) {
	h.messages = append(h.messages, msg)
}

func textResponse(content string, usage model.Usage) *model.ChatResponse {
	return &model.ChatResponse{
		Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: content}}},
		Usage:   usage,
	}
}

// withoutTransportBackoff swaps the package-level transport-retry sleep for
// an instant stand-in that returns immediately without waiting in real
// time, restoring the original on test cleanup. Tests that deliberately
// exhaust or exercise transport-retry classification use this so they stay
// fast instead of sleeping through the real (near-30s-and-up) backoff.
func withoutTransportBackoff(t *testing.T) {
	t.Helper()
	orig := transportRetrySleep
	transportRetrySleep = func(ctx context.Context, attempt int) error {
		return ctx.Err()
	}
	t.Cleanup(func() { transportRetrySleep = orig })
}

func toolCallResponse(callID, toolName, args string, usage model.Usage) *model.ChatResponse {
	return &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{
				Role: "assistant",
				ToolCalls: []model.ToolCall{{
					ID:       callID,
					Type:     "function",
					Function: model.FunctionCall{Name: toolName, Arguments: args},
				}},
			},
		}},
		Usage: usage,
	}
}

func testToolRequest(req model.ChatRequest) model.ChatRequest {
	if len(req.Tools) == 0 {
		req.Tools = []map[string]any{{"type": "function", "function": map[string]any{"name": "test_tool"}}}
		req.ToolChoice = "auto"
	}
	return req
}

func TestController_NormalCompletionNoToolCalls(t *testing.T) {
	history := &recordingHistory{}
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return textResponse("hello there", model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}), nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != "" {
		t.Fatalf("FinishReason = %q, want normal completion", result.FinishReason)
	}
	if got, ok := result.Message.Content.(string); !ok || got != "hello there" {
		t.Fatalf("Message.Content = %#v, want %q", result.Message.Content, "hello there")
	}
	if result.Usage.TotalTokens != 5 {
		t.Fatalf("Usage.TotalTokens = %d, want 5", result.Usage.TotalTokens)
	}
	if result.Rounds != 1 {
		t.Fatalf("Rounds = %d, want 1", result.Rounds)
	}
	if len(history.messages) != 1 || history.messages[0].Role != "assistant" {
		t.Fatalf("expected exactly the final assistant message appended, got %+v", history.messages)
	}
}

func TestController_CompletionContractAllowsReadOnlyAnswerWithoutChange(t *testing.T) {
	t.Parallel()

	ctrl, err := NewController(ControllerConfig{
		CompletionContract: &CompletionContract{RequirePostChangeVerification: true},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("answer from existing evidence", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive {
		t.Fatalf("CompletionStatus = %s", result.CompletionStatus)
	}
}

func TestController_CompletionContractRepairsMissingPostChangeVerification(t *testing.T) {
	t.Parallel()

	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		CompletionContract: &CompletionContract{RequirePostChangeVerification: true, MaxRepairAttempts: 1},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-edit", "edit_file", `{}`, model.Usage{}), nil
			case 2:
				return textResponse("done without verification", model.Usage{}), nil
			case 3:
				return toolCallResponse("call-test", "run_tests", `{}`, model.Usage{}), nil
			default:
				return textResponse("done after tests", model.Usage{}), nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			switch calls[0].Function.Name {
			case "edit_file":
				return []ToolOutcome{{Content: "changed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
			case "run_tests":
				return []ToolOutcome{{Content: "pass", Success: true, EffectClass: "readonly", VerificationObserved: true, VerificationPassed: true}}, nil
			default:
				return nil, fmt.Errorf("unexpected tool %s", calls[0].Function.Name)
			}
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || result.Content != "done after tests" || modelCalls != 4 {
		t.Fatalf("result=%+v modelCalls=%d", result, modelCalls)
	}
	foundRepair := false
	for _, msg := range history.messages {
		if msg.Role == "user" && strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), "workspace changed after the last successful verification") {
			foundRepair = true
		}
	}
	if !foundRepair {
		t.Fatalf("repair instruction not appended: %+v", history.messages)
	}
}

func TestController_CompletionContractExhaustedRepairReturnsIncompleteWithCandidate(t *testing.T) {
	t.Parallel()

	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		CompletionContract: &CompletionContract{RequirePostChangeVerification: true, MaxRepairAttempts: 1},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-edit", "edit_file", `{}`, model.Usage{}), nil
			default:
				return textResponse("still done without verification", model.Usage{}), nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "changed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
		}),
		History: &recordingHistory{},
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", err)
	}
	if result == nil || result.CompletionStatus != CompletionIncomplete || result.Content != "still done without verification" || modelCalls != 3 {
		t.Fatalf("result=%+v modelCalls=%d", result, modelCalls)
	}
	if !strings.Contains(incomplete.Reason, "missing successful verification") {
		t.Fatalf("incomplete reason = %q", incomplete.Reason)
	}
}

func TestController_CompletionContractExplicitMutationRejectsNoChangeWithSpecificRepair(t *testing.T) {
	t.Parallel()

	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		CompletionContract: &CompletionContract{
			RequirePostChangeVerification: true,
			RequireObservableChange:       true,
			MaxRepairAttempts:             1,
			TaskIntent:                    MutationIntent,
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse(fmt.Sprintf("terminal answer %d", modelCalls), model.Usage{}), nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", err)
	}
	if result == nil || result.CompletionStatus != CompletionIncomplete || result.Content != "terminal answer 2" || modelCalls != 2 {
		t.Fatalf("result=%+v modelCalls=%d", result, modelCalls)
	}
	if !strings.Contains(incomplete.Reason, "observable workspace change") {
		t.Fatalf("incomplete reason = %q", incomplete.Reason)
	}
	if len(history.messages) != 1 || history.messages[0].Role != "user" {
		t.Fatalf("repair history = %+v", history.messages)
	}
	repair := model.ExtractTextContentOrEmpty(history.messages[0].Content)
	if !strings.Contains(repair, "requires an observable workspace change") || strings.Contains(repair, "workspace changed after") {
		t.Fatalf("repair guidance = %q", repair)
	}
}

func TestController_CompletionContractReadOnlyIntentStillVerifiesUnexpectedMutation(t *testing.T) {
	t.Parallel()

	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		CompletionContract: &CompletionContract{
			RequirePostChangeVerification: true,
			MaxRepairAttempts:             1,
			TaskIntent:                    ReadOnlyIntent,
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-edit", "edit_file", `{}`, model.Usage{}), nil
			case 2:
				return textResponse("read-only answer after unexpected edit", model.Usage{}), nil
			case 3:
				return toolCallResponse("call-test", "run_tests", `{}`, model.Usage{}), nil
			default:
				return textResponse("verified read-only answer", model.Usage{}), nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			switch calls[0].Function.Name {
			case "edit_file":
				return []ToolOutcome{{Content: "changed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
			case "run_tests":
				return []ToolOutcome{{Content: "pass", Success: true, EffectClass: "readonly", VerificationObserved: true, VerificationPassed: true}}, nil
			default:
				return nil, fmt.Errorf("unexpected tool %s", calls[0].Function.Name)
			}
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || result.Content != "verified read-only answer" || modelCalls != 4 {
		t.Fatalf("result=%+v modelCalls=%d", result, modelCalls)
	}
	foundVerificationRepair := false
	for _, msg := range history.messages {
		if msg.Role == "user" && strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), "workspace changed after the last successful verification") {
			foundVerificationRepair = true
		}
	}
	if !foundVerificationRepair {
		t.Fatalf("verification repair instruction not appended: %+v", history.messages)
	}
}

func TestController_CompletionContractStoppedFinalizationCannotBypassVerification(t *testing.T) {
	t.Parallel()

	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:           New(Config{MaxRounds: 10, MaxToolCalls: 1}),
		FinalizeOnStop:     true,
		CompletionContract: &CompletionContract{RequirePostChangeVerification: true},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-edit", "edit_file", `{}`, model.Usage{}), nil
			}
			return textResponse("final synthesis", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "changed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
		}),
		History: &recordingHistory{},
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", err)
	}
	if result == nil || result.CompletionStatus != CompletionIncomplete || !result.Termination.FinalizationAttempted {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(incomplete.FinalizationError, "missing successful verification") {
		t.Fatalf("finalization error = %q", incomplete.FinalizationError)
	}
}

func TestController_PreservesBillablePartialResponseWhenProviderFails(t *testing.T) {
	providerErr := errors.New("stream interrupted after provider emitted content")
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("partial answer", model.Usage{PromptTokens: 400, CompletionTokens: 200, TotalTokens: 600}), providerErr
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", err)
	}
	if result == nil || !result.Partial || result.CompletionStatus != CompletionIncomplete {
		t.Fatalf("result = %+v, want an explicit incomplete partial result", result)
	}
	if result.Content != "partial answer" || result.Usage.TotalTokens != 600 || result.CostUSD != 0.6 {
		t.Fatalf("partial accounting = content %q usage=%+v cost=%v", result.Content, result.Usage, result.CostUSD)
	}
	if result.Termination.ProviderError != providerErr.Error() || incomplete.ProviderError != providerErr.Error() {
		t.Fatalf("provider error was not retained: termination=%+v incomplete=%+v", result.Termination, incomplete)
	}
}

func TestController_ProviderErrorProjectionIsBoundedWhileRawCauseRemainsWrapped(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 30)
	providerErr := errors.New("provider failed " + secret + " " + strings.Repeat("x", modelstep.MaxPersistedErrorRunes+100))
	wantProjection := modelstep.NormalizeError(providerErr)
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("partial answer", model.Usage{TotalTokens: 1}), providerErr
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.Is(runErr, providerErr) || !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want raw cause and incomplete projection", runErr)
	}
	if result.Termination.ProviderError != wantProjection || incomplete.ProviderError != wantProjection {
		t.Fatalf("provider projection result=%q incomplete=%q want=%q", result.Termination.ProviderError, incomplete.ProviderError, wantProjection)
	}
	if strings.Contains(result.Termination.ProviderError, secret) || len([]rune(result.Termination.ProviderError)) > modelstep.MaxPersistedErrorRunes {
		t.Fatalf("unsafe provider projection: %q", result.Termination.ProviderError)
	}
}

func TestController_AccountingFailureIsNotProviderPartial(t *testing.T) {
	history := &recordingHistory{}
	pricingErr := errors.New("catalog price unavailable after response")
	ctrl, err := NewController(ControllerConfig{
		CostForUsage: func(model.Usage) (float64, error) {
			return 0, pricingErr
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("must not become a partial answer", model.Usage{TotalTokens: 77}), nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, runErr := ctrl.Run(context.Background())
	if runErr == nil || !strings.Contains(runErr.Error(), pricingErr.Error()) {
		t.Fatalf("Run error = %v, want pricing failure", runErr)
	}
	if result == nil || result.Partial || result.FinishReason == FinishReasonModelError || result.Termination.Kind != "cost_accounting" {
		t.Fatalf("result = %+v, want non-provider accounting failure", result)
	}
	if result.Termination.ProviderError != "" || result.Message.Content != nil || result.Content != "" || len(history.messages) != 0 {
		t.Fatalf("accounting failure leaked provider projection: result=%+v history=%+v", result, history.messages)
	}
	if result.Usage.TotalTokens != 77 || result.CostUSD != 0 {
		t.Fatalf("accounting = usage=%+v cost=%v", result.Usage, result.CostUSD)
	}
}

func TestController_ProviderPartialKeepsAccountingFailureDistinct(t *testing.T) {
	providerErr := errors.New("provider stream interrupted")
	pricingErr := errors.New("catalog price unavailable")
	ctrl, err := NewController(ControllerConfig{
		CostForUsage: func(model.Usage) (float64, error) {
			return 0, pricingErr
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("usable provider fragment", model.Usage{TotalTokens: 88}), providerErr
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, runErr := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) || !errors.Is(runErr, providerErr) {
		t.Fatalf("Run error = %v, want provider and incomplete causes", runErr)
	}
	if !strings.Contains(runErr.Error(), pricingErr.Error()) {
		t.Fatalf("Run error = %v, want accounting cause", runErr)
	}
	if result == nil || !result.Partial || result.Content != "usable provider fragment" || result.Termination.Kind != "cost_accounting" {
		t.Fatalf("result = %+v, want preserved provider fragment with accounting termination", result)
	}
	if result.Termination.ProviderError != providerErr.Error() || incomplete.ProviderError != providerErr.Error() {
		t.Fatalf("provider error was conflated: termination=%+v incomplete=%+v", result.Termination, incomplete)
	}
}

func TestController_PersistsAttemptEvidenceAndReplaysAggregateCostOnce(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()

	firstUsage := model.Usage{
		PromptTokens:     11,
		CompletionTokens: 3,
		TotalTokens:      14,
		PromptTokensDetails: &model.PromptTokensDetails{
			CachedTokens: 5,
		},
		CacheWriteTokens: 7,
	}
	secondUsage := model.Usage{
		PromptTokens:     13,
		CompletionTokens: 4,
		TotalTokens:      17,
		CompletionTokenDetails: &model.CompletionTokenDetails{
			ReasoningTokens: 2,
		},
	}
	aggregate := model.AddUsage(firstUsage, secondUsage)
	wantCost := float64(aggregate.TotalTokens) / 1000
	firstIdentity := model.ExecutionIdentity{RequestedModel: "test-model", SelectedModel: "test-model", ProviderID: "provider-a", ResponseModel: "test-model", ResponseID: "attempt-1"}
	secondIdentity := model.ExecutionIdentity{RequestedModel: "test-model", SelectedModel: "test-model", ProviderID: "provider-a", ResponseModel: "test-model", ResponseID: "attempt-2"}

	providerCalls := 0
	firstPriceCalls := 0
	config := ControllerConfig{
		CostForUsage: func(usage model.Usage) (float64, error) {
			firstPriceCalls++
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return &model.ChatResponse{
				Model: "test-model",
				Choices: []model.Choice{{
					Message:      model.Message{Role: "assistant", Content: "durable aggregate result"},
					FinishReason: "stop",
				}},
				Usage:        aggregate,
				UsagePresent: true,
				AttemptEvidence: []model.ModelAttemptEvidence{
					{Usage: firstUsage, UsagePresent: true, Incomplete: true, ExecutionIdentity: &firstIdentity},
					{Usage: secondUsage, UsagePresent: true, FinishReason: "stop", ExecutionIdentity: &secondIdentity},
				},
				ExecutionIdentity: &secondIdentity,
			}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-attempt-evidence",
		TurnID:      "task-attempt-evidence/cp-001/turn-000",
	}

	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	firstResult, err := first.Run(ctx)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if providerCalls != 1 || firstPriceCalls != 1 {
		t.Fatalf("first provider_calls=%d price_calls=%d, want 1 and 1", providerCalls, firstPriceCalls)
	}
	if !reflect.DeepEqual(firstResult.Usage, aggregate) || firstResult.CostUSD != wantCost {
		t.Fatalf("first result usage=%+v cost=%v, want usage=%+v cost=%v", firstResult.Usage, firstResult.CostUSD, aggregate, wantCost)
	}
	if !reflect.DeepEqual(firstResult.ModelExecutions, []model.ExecutionIdentity{firstIdentity, secondIdentity}) {
		t.Fatalf("first model executions = %+v, want flattened attempts without duplicate final response", firstResult.ModelExecutions)
	}

	steps, err := ledger.ListSteps(ctx, runID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want exactly 1", len(steps))
	}
	step := steps[0]
	if step.Status != runledger.StepCompleted || step.OutputEvidenceID == "" {
		t.Fatalf("step = %+v, want completed with output evidence", step)
	}
	object, err := ev.Get(ctx, step.OutputEvidenceID)
	if err != nil {
		t.Fatalf("load response evidence: %v", err)
	}
	decoded, err := modelstep.ValidateResponseEvidence(step.OutputEvidenceID, step.OutputDigest, object)
	if err != nil {
		t.Fatalf("validate response evidence: %v", err)
	}
	if decoded.Response == nil || !decoded.Response.UsagePresent || !reflect.DeepEqual(decoded.Response.Usage, aggregate) {
		t.Fatalf("decoded response = %+v, want present aggregate usage %+v", decoded.Response, aggregate)
	}
	if len(decoded.Response.AttemptEvidence) != 2 {
		t.Fatalf("persisted attempt evidence = %d, want 2", len(decoded.Response.AttemptEvidence))
	}
	firstAttempt := decoded.Response.AttemptEvidence[0]
	secondAttempt := decoded.Response.AttemptEvidence[1]
	if !firstAttempt.UsagePresent || !firstAttempt.Incomplete || !reflect.DeepEqual(firstAttempt.Usage, firstUsage) {
		t.Fatalf("first persisted attempt = %+v, want present incomplete usage %+v", firstAttempt, firstUsage)
	}
	if !secondAttempt.UsagePresent || secondAttempt.Incomplete || secondAttempt.FinishReason != "stop" || !reflect.DeepEqual(secondAttempt.Usage, secondUsage) {
		t.Fatalf("second persisted attempt = %+v, want present complete stop usage %+v", secondAttempt, secondUsage)
	}
	if secondAttempt.ExecutionIdentity == nil || *secondAttempt.ExecutionIdentity != secondIdentity {
		t.Fatalf("second persisted attempt identity = %+v, want %+v", secondAttempt.ExecutionIdentity, secondIdentity)
	}

	replayPriceCalls := 0
	config.CostForUsage = func(model.Usage) (float64, error) {
		replayPriceCalls++
		return 99, nil
	}
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, testingError("provider should not run during attempt-evidence replay")
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController replay: %v", err)
	}
	replayedResult, err := replay.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if providerCalls != 1 || replayPriceCalls != 0 {
		t.Fatalf("after replay provider_calls=%d price_calls=%d, want 1 and 0", providerCalls, replayPriceCalls)
	}
	if !reflect.DeepEqual(replayedResult.Usage, aggregate) || replayedResult.CostUSD != wantCost {
		t.Fatalf("replay usage=%+v cost=%v, want usage=%+v cost=%v", replayedResult.Usage, replayedResult.CostUSD, aggregate, wantCost)
	}
	if !reflect.DeepEqual(replayedResult.ModelExecutions, []model.ExecutionIdentity{firstIdentity, secondIdentity}) {
		t.Fatalf("replayed model executions = %+v, want flattened durable attempt identities", replayedResult.ModelExecutions)
	}
}

func TestController_ReplaysBillablePartialResponseWithoutProviderRetry(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	providerCalls := 0
	build := func(context.Context, int) (model.ChatRequest, error) {
		return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
	}
	config := ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: build,
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return textResponse("durable partial", model.Usage{TotalTokens: 500}), errors.New("provider stream ended")
		}),
		RunLedger: ledger, Evidence: ev, StepJournal: ledger,
		RunID: runID, SessionID: "durable-test", TaskID: "partial", TurnID: "partial-turn",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(context.Background()); err == nil {
		t.Fatal("first Run unexpectedly succeeded")
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls after first run = %d, want 1", providerCalls)
	}

	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, errors.New("replay must not call provider")
	})
	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController second: %v", err)
	}
	result, err := second.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("replay error = %v, want IncompleteTurnError", err)
	}
	if providerCalls != 1 || result == nil || !result.Partial || result.Content != "durable partial" || result.CostUSD != 0.5 {
		t.Fatalf("replay provider_calls=%d result=%+v", providerCalls, result)
	}
}

func TestController_LifecycleObserverIsOrderedRedactedAndIsolated(t *testing.T) {
	var (
		mu     sync.Mutex
		events []LifecycleEvent
		calls  int
	)
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "deepseek/deepseek-v4-pro-0813"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call-1", "search_text", `{"query":"needle"}`, model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}), nil
			}
			return textResponse("grounded answer", model.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		LifecycleObserver: func(event LifecycleEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			// A broken renderer must not abort or alter the agent turn.
			if event.Type == LifecycleModelRequest {
				panic("renderer failure")
			}
		},
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := result.RequireConclusive(); err != nil {
		t.Fatalf("RequireConclusive: %v", err)
	}

	mu.Lock()
	got := append([]LifecycleEvent(nil), events...)
	mu.Unlock()
	if len(got) < 10 {
		t.Fatalf("lifecycle events = %d, want turn, model, tool, and end transitions: %+v", len(got), got)
	}
	if got[0].Type != LifecycleTurnStart || got[len(got)-1].Type != LifecycleTurnEnd {
		t.Fatalf("lifecycle boundary = %q ... %q, want %q ... %q", got[0].Type, got[len(got)-1].Type, LifecycleTurnStart, LifecycleTurnEnd)
	}
	seen := map[LifecycleEventType]bool{}
	for i, event := range got {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, i+1)
		}
		seen[event.Type] = true
		if event.Type == LifecycleToolCall && event.ToolName != "search_text" {
			t.Fatalf("tool projection = %+v", event)
		}
		// LifecycleEvent has no content, arguments, or prompt fields by design;
		// this assertion also proves the model's grounded text never enters the
		// live projection.
		if len(event.EvidenceIDs) != 0 {
			t.Fatalf("unexpected evidence IDs without an evidence store: %+v", event)
		}
	}
	for _, want := range []LifecycleEventType{
		LifecycleStepStart,
		LifecycleModelRequest,
		LifecycleModelResponse,
		LifecycleToolCall,
		LifecycleToolStart,
		LifecycleToolResult,
	} {
		if !seen[want] {
			t.Fatalf("missing lifecycle event %q in %+v", want, got)
		}
	}
}

func TestController_DeferredLifecycleUsesRunAttemptsWithinOneLogicalTurn(t *testing.T) {
	var events []LifecycleEvent
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse(fmt.Sprintf("candidate %d", modelCalls), model.Usage{TotalTokens: 1}), nil
		}),
		TurnID:                "stable-logical-turn",
		DeferLifecycleTurnEnd: true,
		LifecycleObserver: func(event LifecycleEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	first, err := ctrl.Run(t.Context())
	if err != nil || first.CompletionStatus != CompletionConclusive {
		t.Fatalf("first Run = %+v, %v", first, err)
	}
	for _, event := range events {
		if event.Type == LifecycleTurnEnd {
			t.Fatalf("intermediate Run emitted logical completion: %+v", events)
		}
	}
	second, err := ctrl.Run(t.Context())
	if err != nil || second.CompletionStatus != CompletionConclusive {
		t.Fatalf("second Run = %+v, %v", second, err)
	}
	ctrl.CompleteLifecycleTurn(second, nil)
	ctrl.CompleteLifecycleTurn(second, nil)

	counts := map[LifecycleEventType]int{}
	for index, event := range events {
		counts[event.Type]++
		if event.Sequence != uint64(index+1) || event.TurnID != "stable-logical-turn" {
			t.Fatalf("event[%d] correlation/order = %+v", index, event)
		}
		if event.Type == LifecycleAttemptStart || event.Type == LifecycleAttemptEnd {
			if event.RunAttempt < 1 || event.RunAttempt > 2 || event.Continuation != (event.RunAttempt == 2) {
				t.Fatalf("attempt projection = %+v", event)
			}
		}
	}
	if counts[LifecycleTurnStart] != 1 || counts[LifecycleAttemptStart] != 2 || counts[LifecycleAttemptEnd] != 2 || counts[LifecycleTurnEnd] != 1 {
		t.Fatalf("lifecycle counts = %+v events=%+v", counts, events)
	}
	last := events[len(events)-1]
	if last.Type != LifecycleTurnEnd || last.Status != string(CompletionConclusive) || last.RunAttempt != 2 || !last.Continuation {
		t.Fatalf("logical completion = %+v", last)
	}
}

func TestController_DefaultLifecycleDoesNotEmitRunAttempts(t *testing.T) {
	var events []LifecycleEvent
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse(fmt.Sprintf("answer %d", modelCalls), model.Usage{TotalTokens: 1}), nil
		}),
		LifecycleObserver: func(event LifecycleEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	for run := 1; run <= 2; run++ {
		result, err := ctrl.Run(t.Context())
		if err != nil || result.CompletionStatus != CompletionConclusive {
			t.Fatalf("Run %d = %+v, %v", run, result, err)
		}
	}

	counts := map[LifecycleEventType]int{}
	for _, event := range events {
		counts[event.Type]++
		if event.Type == LifecycleAttemptStart || event.Type == LifecycleAttemptEnd {
			t.Fatalf("default lifecycle emitted opt-in attempt event: %+v", event)
		}
		if event.RunAttempt != 0 || event.Continuation {
			t.Fatalf("default lifecycle gained attempt metadata: %+v", event)
		}
	}
	if counts[LifecycleTurnStart] != 2 || counts[LifecycleTurnEnd] != 2 {
		t.Fatalf("default lifecycle boundaries = %+v events=%+v", counts, events)
	}
}

func TestController_EmptyOrTruncatedTerminalCandidateIsIncomplete(t *testing.T) {
	for _, tt := range []struct {
		name   string
		choice model.Choice
	}{
		{name: "empty", choice: model.Choice{Message: model.Message{Role: "assistant"}}},
		{name: "truncated", choice: model.Choice{Message: model.Message{Role: "assistant", Content: "partial"}, FinishReason: "length"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withoutTransportBackoff(t)
			ctrl, err := NewController(ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					return &model.ChatResponse{Choices: []model.Choice{tt.choice}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := ctrl.Run(t.Context())
			if runErr != nil {
				t.Fatalf("Run: %v", runErr)
			}
			if result.CompletionStatus != CompletionIncomplete || result.FinishReason != FinishReasonInvalidCompletion || result.RequireConclusive() == nil {
				t.Fatalf("result = %+v, want explicit incomplete candidate", result)
			}
		})
	}
}

func TestController_TruncatedToolCallChoiceNeverDispatches(t *testing.T) {
	history := &recordingHistory{}
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			response := toolCallResponse("call-truncated", "write_file", `{}`, model.Usage{TotalTokens: 10})
			response.Choices[0].FinishReason = "max_tokens"
			return response, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "must not execute", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if dispatchCalls != 0 || len(history.messages) != 0 {
		t.Fatalf("dispatch_calls=%d history=%+v", dispatchCalls, history.messages)
	}
	if result.FinishReason != FinishReasonInvalidCompletion || result.CompletionStatus != CompletionIncomplete || !strings.Contains(result.Termination.Reason, "truncated") {
		t.Fatalf("result=%+v", result)
	}
}

func TestController_DispatchesToolsBackfillsIDsAndSumsUsage(t *testing.T) {
	history := &recordingHistory{}
	round := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, r int) (model.ChatRequest, error) {
			round++
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			if round == 1 {
				// No ID: Controller must backfill it.
				return toolCallResponse("", "search_text", `{"query":"foo"}`, model.Usage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14}), nil
			}
			return textResponse("done", model.Usage{PromptTokens: 20, CompletionTokens: 6, TotalTokens: 26}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			if len(calls) != 1 || calls[0].ID == "" {
				t.Fatalf("expected one backfilled tool call, got %+v", calls)
			}
			return []ToolOutcome{{Content: "ok: foo", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != "" {
		t.Fatalf("FinishReason = %q, want normal completion", result.FinishReason)
	}
	if result.Rounds != 2 {
		t.Fatalf("Rounds = %d, want 2", result.Rounds)
	}
	if result.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d, want 1", result.ToolCalls)
	}
	if result.Usage.TotalTokens != 40 {
		t.Fatalf("Usage.TotalTokens = %d, want 40 (14 + 26)", result.Usage.TotalTokens)
	}
	// History: round 1 assistant tool-call message, round 1 tool result, round 2 final assistant message.
	if len(history.messages) != 3 {
		t.Fatalf("expected 3 appended messages, got %d: %+v", len(history.messages), history.messages)
	}
	if len(history.messages[0].ToolCalls) != 1 || history.messages[0].ToolCalls[0].ID == "" {
		t.Fatalf("expected the assistant tool-call message with a backfilled ID, got %+v", history.messages[0])
	}
	if history.messages[1].Role != "tool" || history.messages[1].Content != "ok: foo" {
		t.Fatalf("expected the tool result message, got %+v", history.messages[1])
	}
	if history.messages[1].ToolCallID != history.messages[0].ToolCalls[0].ID {
		t.Fatalf("tool result ToolCallID = %q, want %q", history.messages[1].ToolCallID, history.messages[0].ToolCalls[0].ID)
	}
}

func TestController_UnofferedToolCallsStopBeforeHistoryOrToolSteps(t *testing.T) {
	tests := []struct {
		name         string
		request      model.ChatRequest
		wantDispatch bool
	}{
		{
			name:    "no tools explicit none",
			request: model.ChatRequest{Model: "test-model", ToolChoice: "none"},
		},
		{
			name:    "no schemas omitted choice",
			request: model.ChatRequest{Model: "test-model"},
		},
		{
			name: "schemas explicit none",
			request: model.ChatRequest{
				Model:      "test-model",
				Tools:      []map[string]any{{"type": "function", "function": map[string]any{"name": "modify_probe"}}},
				ToolChoice: "none",
			},
		},
		{
			name: "offered schema dispatches normally",
			request: model.ChatRequest{
				Model:      "test-model",
				Tools:      []map[string]any{{"type": "function", "function": map[string]any{"name": "modify_probe"}}},
				ToolChoice: "auto",
			},
			wantDispatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger, evidenceStore, runID := newDurableControllerStores(t)
			history := &recordingHistory{}
			dispatches := 0
			modelCalls := 0
			controller, err := NewController(ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return tt.request, nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					modelCalls++
					if tt.wantDispatch && modelCalls == 2 {
						return textResponse("normal completion", model.Usage{TotalTokens: 3}), nil
					}
					return toolCallResponse("unexpected-call", "modify_probe", `{}`, model.Usage{TotalTokens: 2}), nil
				}),
				DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					dispatches++
					return []ToolOutcome{{Content: "probe completed", Success: true}}, nil
				}),
				History:     history,
				RunLedger:   ledger,
				Evidence:    evidenceStore,
				StepJournal: ledger,
				RunID:       runID,
				SessionID:   "tool-offer-fuse",
				TaskID:      "task-tool-offer-fuse",
				TurnID:      "turn-tool-offer-fuse",
			})
			if err != nil {
				t.Fatalf("NewController: %v", err)
			}

			result, runErr := controller.Run(t.Context())
			steps, err := ledger.ListSteps(t.Context(), runID)
			if err != nil {
				t.Fatalf("ListSteps: %v", err)
			}
			if tt.wantDispatch {
				if runErr != nil {
					t.Fatalf("Run: %v", runErr)
				}
				if dispatches != 1 || modelCalls != 2 || result.CompletionStatus != CompletionConclusive {
					t.Fatalf("dispatches=%d model_calls=%d result=%+v", dispatches, modelCalls, result)
				}
				if len(history.messages) != 3 || len(steps) != 3 || steps[1].Kind != "tool" {
					t.Fatalf("history=%+v steps=%+v, want assistant/tool/assistant and a durable tool step", history.messages, steps)
				}
				return
			}

			var incomplete *IncompleteTurnError
			if !errors.As(runErr, &incomplete) {
				t.Fatalf("Run error = %v, want IncompleteTurnError", runErr)
			}
			if dispatches != 0 || modelCalls != 1 || len(history.messages) != 0 {
				t.Fatalf("dispatches=%d model_calls=%d history=%+v, want no tool or history side effect", dispatches, modelCalls, history.messages)
			}
			if result.CompletionStatus != CompletionIncomplete || result.FinishReason != FinishReasonInvalidCompletion || result.Termination.Code != "unoffered_tool_call" || !result.Partial || len(result.Message.ToolCalls) != 1 {
				t.Fatalf("result=%+v, want preserved incomplete malformed response", result)
			}
			if len(steps) != 1 || steps[0].Kind != "model" {
				t.Fatalf("steps=%+v, want only the completed model-response step", steps)
			}
			object, err := evidenceStore.Get(t.Context(), steps[0].OutputEvidenceID)
			if err != nil {
				t.Fatalf("load model response evidence: %v", err)
			}
			decoded, err := modelstep.ValidateResponseEvidence(steps[0].OutputEvidenceID, steps[0].OutputDigest, object)
			if err != nil {
				t.Fatalf("validate model response evidence: %v", err)
			}
			if decoded.Response == nil || len(decoded.Response.Choices) != 1 || len(decoded.Response.Choices[0].Message.ToolCalls) != 1 || decoded.ResponseToolsOffered == nil || *decoded.ResponseToolsOffered {
				t.Fatalf("decoded response=%+v offer=%v, want retained call with false offer", decoded.Response, decoded.ResponseToolsOffered)
			}
		})
	}
}

func TestController_ReplayKeepsResponseProducingNoToolsDecision(t *testing.T) {
	ledger, evidenceStore, runID := newDurableControllerStores(t)
	request := testToolRequest(model.ChatRequest{Model: "test-model"})
	dispatches := 0
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return request, nil
		},
		CallModel: ResponseToolOfferModelCallerFunc(func(context.Context, ModelDispatchCall) (*model.ChatResponse, bool, error) {
			return toolCallResponse("retry-call", "modify_probe", `{}`, model.Usage{TotalTokens: 2}), false, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatches++
			return []ToolOutcome{{Content: "must not execute", Success: true}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    evidenceStore,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "tool-offer-replay",
		TaskID:      "task-tool-offer-replay",
		TurnID:      "turn-tool-offer-replay",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	firstResult, firstErr := first.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(firstErr, &incomplete) || firstResult.Termination.Code != "unoffered_tool_call" {
		t.Fatalf("first result=%+v error=%v", firstResult, firstErr)
	}

	providerCalls := 0
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, errors.New("durable replay must not call provider")
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, replayErr := replay.Run(t.Context())
	if !errors.As(replayErr, &incomplete) || replayed.Termination.Code != "unoffered_tool_call" {
		t.Fatalf("replay result=%+v error=%v", replayed, replayErr)
	}
	if providerCalls != 0 || dispatches != 0 || replayed.ModelRequests != 1 {
		t.Fatalf("provider_calls=%d dispatches=%d replayed=%+v", providerCalls, dispatches, replayed)
	}
	steps, err := ledger.ListSteps(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Kind != "model" {
		t.Fatalf("steps=%+v, want only the replayed model-response step", steps)
	}
}

func TestController_ContextualDispatcherCarriesStableApprovalIdentity(t *testing.T) {
	var dispatched []ToolDispatchCall
	round := 0
	const (
		runID  = "run-contextual"
		taskID = "task-contextual"
		turnID = "turn-contextual"
	)
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			round++
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			switch round {
			case 1:
				return toolCallResponse("provider-round-1", "write_file", `{"path":"one"}`, model.Usage{}), nil
			case 2:
				return toolCallResponse("provider-round-2", "write_file", `{"path":"two"}`, model.Usage{}), nil
			default:
				return textResponse("done", model.Usage{}), nil
			}
		}),
		DispatchTools: ContextualToolDispatcherFunc(func(_ context.Context, calls []ToolDispatchCall) ([]ToolOutcome, error) {
			dispatched = append(dispatched, calls...)
			return []ToolOutcome{{Content: "ok", Success: true}}, nil
		}),
		RunID: runID, TaskID: taskID, TurnID: turnID,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(dispatched) != 2 {
		t.Fatalf("dispatched calls = %+v, want two calls", dispatched)
	}
	for index, got := range dispatched {
		wantRound := index + 1
		wantProviderID := fmt.Sprintf("provider-round-%d", wantRound)
		wantStepID := StableStepID(runID, taskID, turnID, wantRound, "tool", 0)
		if got.Call.ID != wantProviderID || got.ProviderToolCallID != wantProviderID {
			t.Fatalf("call %d provider identity = %+v, want %q", index, got, wantProviderID)
		}
		if got.RunID != runID || got.TaskID != taskID || got.TurnID != turnID || got.StepID != wantStepID || got.Round != wantRound || got.ToolIndex != 0 {
			t.Fatalf("call %d durable identity = %+v", index, got)
		}
		wantApprovalID := StableApprovalID(runID, taskID, turnID, wantStepID, wantRound, 0)
		if got.ApprovalID != wantApprovalID || got.ApprovalID == got.ProviderToolCallID {
			t.Fatalf("call %d approval identity = %q, provider=%q want %q", index, got.ApprovalID, got.ProviderToolCallID, wantApprovalID)
		}
		if StableApprovalID(runID, taskID, turnID, got.StepID, got.Round, got.ToolIndex) != got.ApprovalID {
			t.Fatalf("call %d approval identity was not replay-stable", index)
		}
	}
	if dispatched[0].ApprovalID == dispatched[1].ApprovalID {
		t.Fatalf("distinct rounds collided on approval ID %q", dispatched[0].ApprovalID)
	}
}

func TestController_ContextualModelCallerCarriesExactStepIdentity(t *testing.T) {
	const (
		runID  = "run-contextual-model"
		taskID = "task-contextual-model"
		turnID = "turn-contextual-model"
	)
	var got ModelDispatchCall
	controller, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ContextualModelCallerFunc(func(_ context.Context, call ModelDispatchCall) (*model.ChatResponse, error) {
			got = call
			return textResponse("done", model.Usage{}), nil
		}),
		RunID: runID, TaskID: taskID, TurnID: turnID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantStep := StableStepID(runID, taskID, turnID, 1, "model", 0)
	if got.RunID != runID || got.TaskID != taskID || got.TurnID != turnID ||
		got.StepID != wantStep || got.Kind != "model" || got.Round != 1 ||
		got.Request.Model != "test-model" || got.UseContinuation {
		t.Fatalf("contextual model call = %+v", got)
	}
}

func TestController_ContextualDispatcherStableWithoutProviderIDs(t *testing.T) {
	var dispatched []ToolDispatchCall
	round := 0
	const (
		runID  = "run-contextual-missing-provider"
		taskID = "task-contextual-missing-provider"
		turnID = "turn-contextual-missing-provider"
	)
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			round++
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			switch round {
			case 1:
				return toolCallResponse("", "write_file", `{"path":"one"}`, model.Usage{}), nil
			case 2:
				return toolCallResponse("", "write_file", `{"path":"two"}`, model.Usage{}), nil
			default:
				return textResponse("done", model.Usage{}), nil
			}
		}),
		DispatchTools: ContextualToolDispatcherFunc(func(_ context.Context, calls []ToolDispatchCall) ([]ToolOutcome, error) {
			dispatched = append(dispatched, calls...)
			return []ToolOutcome{{Content: "ok", Success: true}}, nil
		}),
		RunID: runID, TaskID: taskID, TurnID: turnID,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, err := ctrl.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(dispatched) != 2 {
		t.Fatalf("dispatched calls = %+v, want two calls", dispatched)
	}
	for index, got := range dispatched {
		wantRound := index + 1
		if got.ProviderToolCallID != "tool-1" || got.Call.ID != got.ProviderToolCallID {
			t.Fatalf("call %d provider fallback = %+v, want tool-1", index, got)
		}
		stepID := StableStepID(runID, taskID, turnID, wantRound, "tool", 0)
		wantApprovalID := StableApprovalID(runID, taskID, turnID, stepID, wantRound, 0)
		if got.ApprovalID != wantApprovalID {
			t.Fatalf("call %d approval ID = %q, want %q", index, got.ApprovalID, wantApprovalID)
		}
		if got.ApprovalID != StableApprovalID(runID, taskID, turnID, got.StepID, got.Round, got.ToolIndex) {
			t.Fatalf("call %d approval identity was not replay-stable", index)
		}
	}
	if dispatched[0].ApprovalID == dispatched[1].ApprovalID {
		t.Fatalf("missing provider IDs collided across rounds: %q", dispatched[0].ApprovalID)
	}
}

func TestStableApprovalID_LegacyFallbackAndReplay(t *testing.T) {
	step := StableStepID("run-one", "task-one", "turn-one", 3, "tool", 2)
	first := StableApprovalID("run-one", "task-one", "turn-one", step, 3, 2)
	if first == "" {
		t.Fatal("stable approval ID is empty")
	}
	if replay := StableApprovalID("run-one", "task-one", "turn-one", step, 3, 2); replay != first {
		t.Fatalf("replay approval ID = %q, want %q", replay, first)
	}
	if otherRound := StableApprovalID("run-one", "task-one", "turn-one", StableStepID("run-one", "task-one", "turn-one", 4, "tool", 2), 4, 2); otherRound == first {
		t.Fatalf("rounds collided on approval ID %q", first)
	}
	if otherSession := StableApprovalID("run-two", "task-one", "turn-one", step, 3, 2); otherSession == first {
		t.Fatalf("distinct sessions collided on approval ID %q", first)
	}
	if legacy := StableApprovalID("", "task-one", "turn-one", step, 3, 2); legacy != "" {
		t.Fatalf("legacy identity = %q, want empty fallback marker", legacy)
	}
}

func TestController_GovernorStopsOnExactRepeat(t *testing.T) {
	calls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 3, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 100}),
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			calls++
			return toolCallResponse("call-1", "search_text", `{"query":"foo"}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "same result", Success: true}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != FinishReasonLoopGuard {
		t.Fatalf("FinishReason = %q, want %q", result.FinishReason, FinishReasonLoopGuard)
	}
	if result.GuardDecision.Kind != "exact_repeat" {
		t.Fatalf("GuardDecision.Kind = %q, want exact_repeat", result.GuardDecision.Kind)
	}
	if calls != 3 {
		t.Fatalf("model calls = %d, want exactly 3 (the exact-repeat limit)", calls)
	}
	if result.Content == "" {
		t.Fatalf("expected a caller-facing stop message")
	}
}

func TestController_StepCapStopsBeforeGovernorDefault(t *testing.T) {
	calls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 100}),
		StepCap:  1,
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			calls++
			return toolCallResponse("call-1", "search_text", `{"query":"foo"}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "distinct-" + calls[0].Function.Arguments, Success: true}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != FinishReasonStepCap {
		t.Fatalf("FinishReason = %q, want %q", result.FinishReason, FinishReasonStepCap)
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want exactly 1 (StepCap stops before round 2's model call)", calls)
	}
}

func TestController_GuardFinalizesFromToolEvidenceWithToolsDisabled(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	parallel := true
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			messages := []model.Message{{Role: "user", Content: "find the answer"}}
			messages = append(messages, history.messages...)
			return model.ChatRequest{
				Model:             "test-model",
				Messages:          messages,
				Tools:             []map[string]any{{"type": "function"}},
				ToolChoice:        "auto",
				ParallelToolCalls: &parallel,
			}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{TotalTokens: 5}), nil
			}
			if len(req.Tools) != 0 || req.ToolChoice != "none" || req.ParallelToolCalls != nil {
				t.Fatalf("finalization still exposed tools: %+v", req)
			}
			var sawToolEvidence, sawFinalizationPrompt bool
			for _, message := range req.Messages {
				if message.Role == "tool" && message.ToolCallID == "call-1" && strings.Contains(model.ExtractTextContentOrEmpty(message.Content), "grounded evidence") {
					sawToolEvidence = true
				}
			}
			if final := req.Messages[len(req.Messages)-1]; final.Role == "user" && strings.Contains(model.ExtractTextContentOrEmpty(final.Content), "Do not call tools") {
				sawFinalizationPrompt = true
			}
			if !sawToolEvidence || !sawFinalizationPrompt {
				t.Fatalf("finalization request missing evidence=%v or prompt=%v: %+v", sawToolEvidence, sawFinalizationPrompt, req.Messages)
			}
			return textResponse("grounded final answer", model.Usage{TotalTokens: 7}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || result.RequireConclusive() != nil {
		t.Fatalf("completion = %q, termination = %+v", result.CompletionStatus, result.Termination)
	}
	if result.FinishReason != FinishReasonLoopGuard || result.Termination.Kind != "tool_call_limit" || !result.Termination.FinalizationAttempted || result.Termination.FinalizationError != "" {
		t.Fatalf("unexpected termination: finish=%q termination=%+v", result.FinishReason, result.Termination)
	}
	if got := model.ExtractTextContentOrEmpty(result.Message.Content); got != "grounded final answer" {
		t.Fatalf("final message = %q", got)
	}
	if result.Usage.TotalTokens != 12 || modelCalls != 2 || dispatchCalls != 1 {
		t.Fatalf("usage=%d model_calls=%d dispatch_calls=%d", result.Usage.TotalTokens, modelCalls, dispatchCalls)
	}
}

func TestController_FinalizationFailureIsExplicitlyIncomplete(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
			}
			return nil, errors.New("provider unavailable during synthesis")
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence survives", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", err)
	}
	if result == nil || result.CompletionStatus != CompletionIncomplete || !result.Termination.FinalizationAttempted {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Termination.FinalizationError, "provider unavailable") || result.Message.Content != nil {
		t.Fatalf("termination = %+v message=%+v", result.Termination, result.Message)
	}
	if len(history.messages) != 2 || history.messages[1].Role != "tool" || !strings.Contains(model.ExtractTextContentOrEmpty(history.messages[1].Content), "evidence survives") {
		t.Fatalf("tool evidence was not preserved in history: %+v", history.messages)
	}
}

func TestController_FinalizationPreservesPartialResponseAccountingAndProjection(t *testing.T) {
	history := &recordingHistory{}
	providerErr := errors.New("final synthesis stream interrupted")
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		MaxCostUSD:     2,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 100}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 100}), nil
			}
			return textResponse("partial final synthesis", model.Usage{TotalTokens: 200}), providerErr
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence survives", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, runErr := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", runErr)
	}
	if result == nil || !result.Partial || result.CompletionStatus != CompletionIncomplete {
		t.Fatalf("result = %+v, want an incomplete partial finalization", result)
	}
	if result.Content != "partial final synthesis" || model.ExtractTextContentOrEmpty(result.Message.Content) != "partial final synthesis" {
		t.Fatalf("partial projection = content %q message=%q", result.Content, model.ExtractTextContentOrEmpty(result.Message.Content))
	}
	if result.Usage.TotalTokens != 300 || math.Abs(result.CostUSD-0.3) > 1e-12 {
		t.Fatalf("partial accounting = usage=%+v cost=%v", result.Usage, result.CostUSD)
	}
	if result.Termination.ProviderError != providerErr.Error() || incomplete.ProviderError != providerErr.Error() {
		t.Fatalf("provider error was not retained: termination=%+v incomplete=%+v", result.Termination, incomplete)
	}
	if !strings.Contains(result.Termination.FinalizationError, providerErr.Error()) {
		t.Fatalf("finalization error = %q, want provider detail", result.Termination.FinalizationError)
	}
	if !result.Termination.FinalizationAttempted || modelCalls != 2 {
		t.Fatalf("termination=%+v model_calls=%d", result.Termination, modelCalls)
	}
	if len(history.messages) != 2 || history.messages[0].Role != "assistant" || len(history.messages[0].ToolCalls) != 1 || history.messages[1].Role != "tool" {
		t.Fatalf("partial finalization appended an inappropriate history message: %+v", history.messages)
	}
}

func TestController_FinalizationAccountingFailureIsNotProviderPartial(t *testing.T) {
	history := &recordingHistory{}
	pricingErr := errors.New("final synthesis price unavailable")
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		CostForUsage: func(usage model.Usage) (float64, error) {
			if usage.TotalTokens == 200 {
				return 0, pricingErr
			}
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 100}), nil
			}
			return textResponse("must not become a partial final answer", model.Usage{TotalTokens: 200}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "preserved evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, runErr := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", runErr)
	}
	if result == nil || result.Partial || result.CompletionStatus != CompletionIncomplete || result.Termination.ProviderError != "" {
		t.Fatalf("result = %+v, want non-provider finalization failure", result)
	}
	if !strings.Contains(result.Termination.FinalizationError, pricingErr.Error()) || incomplete.ProviderError != "" {
		t.Fatalf("termination=%+v incomplete=%+v", result.Termination, incomplete)
	}
	if strings.Contains(result.Content, "must not become") || result.Message.Content != nil || len(history.messages) != 2 {
		t.Fatalf("accounting failure leaked final response: result=%+v history=%+v", result, history.messages)
	}
	if result.Usage.TotalTokens != 300 || math.Abs(result.CostUSD-0.1) > 1e-12 || modelCalls != 2 {
		t.Fatalf("accounting result=%+v model_calls=%d", result, modelCalls)
	}
}

func TestController_ReplaysPartialFinalizationWithoutProviderRetry(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	providerCalls := 0
	secret := "sk-" + strings.Repeat("a", 30)
	rawProviderError := "final synthesis stream ended " + secret + " " + strings.Repeat("x", modelstep.MaxPersistedErrorRunes+100)
	persistedProviderError := modelstep.NormalizeErrorText(rawProviderError)
	build := func(context.Context, int) (model.ChatRequest, error) {
		return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 100}), nil
	}
	config := ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		MaxCostUSD:     2,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: build,
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			if providerCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 100}), nil
			}
			return textResponse("durable partial finalization", model.Usage{TotalTokens: 200}), errors.New(rawProviderError)
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "durable evidence", Success: true}}, nil
		}),
		RunLedger: ledger, Evidence: ev, StepJournal: ledger,
		RunID: runID, SessionID: "durable-test", TaskID: "finalization", TurnID: "finalization-turn",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(context.Background()); err == nil {
		t.Fatal("first Run unexpectedly succeeded")
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls after first run = %d, want 2", providerCalls)
	}
	// A fresh Governor represents a process restart; the durable step journal,
	// rather than the in-memory round counter, supplies replay identity.
	config.Governor = New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1})

	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, errors.New("replay must not call provider")
	})
	config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
		return nil, errors.New("replay must not dispatch tool")
	})
	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController second: %v", err)
	}
	result, runErr := second.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("replay error = %v, want IncompleteTurnError", runErr)
	}
	if providerCalls != 2 || result == nil || !result.Partial || result.Content != "durable partial finalization" || result.Usage.TotalTokens != 300 || math.Abs(result.CostUSD-0.3) > 1e-12 {
		t.Fatalf("replay provider_calls=%d result=%+v", providerCalls, result)
	}
	if result.Termination.ProviderError != persistedProviderError || incomplete.ProviderError != persistedProviderError {
		t.Fatalf("replay provider error = termination=%+v incomplete=%+v", result.Termination, incomplete)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	foundFinalizationFailure := false
	for _, event := range events {
		for _, key := range []string{"error", "provider_error", "reason", "pricing_error"} {
			value, _ := event.Payload[key].(string)
			if strings.Contains(value, secret) || len([]rune(value)) > modelstep.MaxPersistedErrorRunes {
				t.Fatalf("event %s leaked or exceeded bound in %s: %q", event.Type, key, value)
			}
		}
		if event.Type == runledger.EventControllerDecision && event.Payload["kind"] == "finalization_failed" {
			foundFinalizationFailure = true
			if reason, _ := event.Payload["reason"].(string); reason == "" || !strings.Contains(reason, "[REDACTED]") {
				t.Fatalf("finalization failure reason = %q", reason)
			}
		}
	}
	if !foundFinalizationFailure {
		t.Fatal("missing normalized finalization_failed decision")
	}
}

func TestController_EmptyChoicesAfterToolEvidenceUsesFinalization(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 2}), nil
			case 2:
				return &model.ChatResponse{Usage: model.Usage{TotalTokens: 1}}, nil
			default:
				if len(req.Tools) != 0 || req.ToolChoice != "none" || req.ParallelToolCalls != nil {
					t.Fatalf("finalization request retained tools: %+v", req)
				}
				return textResponse("answer from preserved evidence", model.Usage{TotalTokens: 3}), nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || !result.Termination.FinalizationAttempted || modelCalls != 3 {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
	if got := model.ExtractTextContentOrEmpty(result.Message.Content); got != "answer from preserved evidence" {
		t.Fatalf("final answer = %q", got)
	}
}

func TestController_ExplicitCostCeilingDoesNotSpendAgainAfterExhaustion(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 50}),
		FinalizeOnStop: true,
		MaxCostUSD:     1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 100, Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 1000}), nil
			}
			return textResponse("cost-bounded answer", model.Usage{TotalTokens: 2000}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete after cost exhaustion", err)
	}
	if result.CompletionStatus != CompletionIncomplete || result.Termination.Kind != "cost_limit" || result.CostUSD != 1 || modelCalls != 1 {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
}

func TestController_CostCeilingPersistsAcrossRunContinuations(t *testing.T) {
	modelCalls := 0
	request := model.ChatRequest{
		Model:     "test-model",
		MaxTokens: 10,
		Messages:  []model.Message{{Role: "user", Content: strings.Repeat("prompt", 20)}},
	}
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return request, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("first answer needs a nudge", model.Usage{TotalTokens: 600}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.CostUSD != 0.6 || first.ModelRequests != 1 || first.Usage.TotalTokens != 600 || modelCalls != 1 {
		t.Fatalf("first result=%+v model_calls=%d", first, modelCalls)
	}

	second, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("second Run error = %v, want incomplete cost stop", runErr)
	}
	if modelCalls != 1 {
		t.Fatalf("model calls = %d, second continuation dispatched past the all-in ceiling", modelCalls)
	}
	if second.CostUSD != 0.6 || second.ModelRequests != 1 || second.Usage.TotalTokens != 600 {
		t.Fatalf("second cumulative result=%+v", second)
	}
	if second.Termination.Kind != "cost_limit" {
		t.Fatalf("second termination=%+v", second.Termination)
	}
}

func TestController_ModelRequestLimitPersistsAcrossRunContinuations(t *testing.T) {
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxModelRequests: 1,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("first answer needs a nudge", model.Usage{TotalTokens: 7}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.ModelRequests != 1 || first.Usage.TotalTokens != 7 || modelCalls != 1 {
		t.Fatalf("first result=%+v model_calls=%d", first, modelCalls)
	}

	second, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if modelCalls != 1 || second.ModelRequests != 1 || second.Usage.TotalTokens != 7 {
		t.Fatalf("second result=%+v model_calls=%d", second, modelCalls)
	}
	if second.Termination.Kind != "model_request_limit" || second.CompletionStatus != CompletionIncomplete {
		t.Fatalf("second termination=%+v completion=%q", second.Termination, second.CompletionStatus)
	}
}

func TestController_OverCeilingFirstResponseCannotDispatchTools(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 100}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if req.MaxTokens != 100 {
				t.Fatalf("MaxTokens = %d, want existing affordable allowance 100", req.MaxTokens)
			}
			return toolCallResponse("call-1", "write_file", `{}`, model.Usage{TotalTokens: 2000}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "must not execute", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want explicit incomplete", runErr)
	}
	if modelCalls != 1 || dispatchCalls != 0 || len(history.messages) != 0 {
		t.Fatalf("model_calls=%d dispatch_calls=%d history=%+v", modelCalls, dispatchCalls, history.messages)
	}
	if result.CompletionStatus != CompletionIncomplete || result.Termination.Kind != "cost_limit" || result.CostUSD != 2 || result.Partial || result.Termination.ProviderError != "" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Termination.Reason, "content and tool calls were rejected") {
		t.Fatalf("termination reason = %q", result.Termination.Reason)
	}
}

func TestController_OverCeilingFinalizationResponseIsRejected(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		MaxCostUSD:     1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 10_000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{
				Model:     "test-model",
				MaxTokens: 100,
				Messages:  append([]model.Message(nil), history.messages...),
			}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 4000}), nil
			}
			return textResponse("must not be accepted", model.Usage{TotalTokens: 7000}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "preserved evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want explicit incomplete", runErr)
	}
	if modelCalls != 2 || dispatchCalls != 1 {
		t.Fatalf("model_calls=%d dispatch_calls=%d", modelCalls, dispatchCalls)
	}
	if len(history.messages) != 2 {
		t.Fatalf("history has %d messages, want only assistant tool call and tool result: %+v", len(history.messages), history.messages)
	}
	if result.CompletionStatus != CompletionIncomplete || !result.Termination.FinalizationAttempted || result.CostUSD != 1.1 || result.Partial || result.Termination.ProviderError != "" {
		t.Fatalf("result = %+v", result)
	}
	if result.Message.Content != nil || strings.Contains(result.Content, "must not be accepted") {
		t.Fatalf("over-ceiling finalization leaked rejected content: %+v", result)
	}
	if !strings.Contains(result.Termination.FinalizationError, "exceeding the explicit") {
		t.Fatalf("finalization error = %q", result.Termination.FinalizationError)
	}
}

func TestController_CostReservationBoundsUnknownAndExplicitOutputAllowances(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		request               model.ChatRequest
		maxCostUSD            float64
		wantMaxTokens         int
		wantCompletionClamped bool
	}{
		{
			name:          "provider default becomes explicit conservative allowance",
			request:       model.ChatRequest{Model: "test-model"},
			maxCostUSD:    10,
			wantMaxTokens: fallbackCostBoundedOutputTokens,
		},
		{
			name:                  "existing completion field is clamped without adding conflicting max tokens",
			request:               model.ChatRequest{Model: "test-model", MaxCompletionTokens: 2000},
			maxCostUSD:            1,
			wantCompletionClamped: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var captured model.ChatRequest
			ctrl, err := NewController(ControllerConfig{
				MaxCostUSD: tt.maxCostUSD,
				CostForUsage: func(usage model.Usage) (float64, error) {
					return float64(usage.TotalTokens) / 1000, nil
				},
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return tt.request, nil
				},
				CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
					captured = req
					return textResponse("done", model.Usage{TotalTokens: 1}), nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ctrl.Run(t.Context()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tt.wantMaxTokens > 0 && captured.MaxTokens != tt.wantMaxTokens {
				t.Fatalf("MaxTokens = %d, want %d", captured.MaxTokens, tt.wantMaxTokens)
			}
			if tt.wantCompletionClamped {
				if captured.MaxTokens != 0 || captured.MaxCompletionTokens <= 0 || captured.MaxCompletionTokens >= tt.request.MaxCompletionTokens {
					t.Fatalf("captured request = %+v, want only clamped MaxCompletionTokens", captured)
				}
				envelope := model.EstimateRequestTokens(captured).Total + captured.MaxCompletionTokens
				if envelope > 1_000 {
					t.Fatalf("reserved envelope = %d tokens, exceeds 1,000-token ceiling", envelope)
				}
			}
		})
	}
}

func TestController_MissingProviderUsageChargesReservationAndKeepsResponse(t *testing.T) {
	var dispatched model.ChatRequest
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{
				Model:     "test-model",
				MaxTokens: 100,
				Messages:  []model.Message{{Role: "user", Content: "answer this"}},
			}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			dispatched = req
			message := model.Message{Role: "assistant", Content: "completed response"}
			return &model.ChatResponse{
				Model: req.Model,
				Choices: []model.Choice{{
					Message:      message,
					FinishReason: "stop",
				}},
				Usage:        model.EstimateChatUsage(req, message),
				UsagePresent: false,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || result.Content != "completed response" {
		t.Fatalf("result = %+v, want accepted completed response", result)
	}
	if !result.Usage.Estimated || result.Usage.TotalTokens <= 0 {
		t.Fatalf("usage = %+v, want marked local estimate", result.Usage)
	}
	inputTokens, err := conservativeRequestInputTokenBound(dispatched)
	if err != nil {
		t.Fatal(err)
	}
	wantReservedCost := float64(inputTokens+dispatched.MaxTokens) / 1000
	if math.Abs(result.CostUSD-wantReservedCost) > 1e-12 {
		t.Fatalf("cost = %v, want conservative reservation %v", result.CostUSD, wantReservedCost)
	}
}

func TestController_CostCeilingRejectsUnpriceableImageInputBeforeDispatch(t *testing.T) {
	modelCalls := 0
	priceCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(model.Usage) (float64, error) {
			priceCalls++
			return 0, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{
				Model: "vision-model",
				Messages: []model.Message{{
					Role: "user",
					Content: []model.ContentPart{
						{Type: "text", Text: "inspect this"},
						{Type: "image_url", ImageURL: &model.ImageURL{URL: "https://example.invalid/large.png"}},
					},
				}},
			}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("must not run", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error=%v, want incomplete", runErr)
	}
	if modelCalls != 0 || priceCalls != 0 {
		t.Fatalf("model_calls=%d price_calls=%d, want no dispatch or pricing", modelCalls, priceCalls)
	}
	if result.Termination.Kind != "cost_limit" || !strings.Contains(result.Termination.Reason, "image-token estimator") {
		t.Fatalf("result=%+v", result)
	}
}

func TestController_CostNormalizerObservesFinalAffordableAllowance(t *testing.T) {
	var normalized []int
	var captured model.ChatRequest
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			normalized = append(normalized, requestOutputAllowance(req, 0, 0))
			return req, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 2000}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			captured = req
			return textResponse("done", model.Usage{TotalTokens: 1}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(normalized) < 4 || len(normalized)%2 != 0 || normalized[0] != 2000 {
		t.Fatalf("normalizer allowances = %v, want requested then lower affordable candidates", normalized)
	}
	sawDispatchedAllowance := false
	for i := 0; i < len(normalized); i += 2 {
		if normalized[i] != normalized[i+1] {
			t.Fatalf("normalizer idempotence pair %d = %d, %d", i/2, normalized[i], normalized[i+1])
		}
		if normalized[i] == captured.MaxTokens {
			sawDispatchedAllowance = true
		}
	}
	if captured.MaxTokens >= normalized[0] || !sawDispatchedAllowance {
		t.Fatalf("dispatched MaxTokens = %d, normalized candidates = %v", captured.MaxTokens, normalized)
	}
}

func TestNewController_RejectsNonFiniteMaxCostCeilings(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "negative", value: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewController(ControllerConfig{
				MaxCostUSD: tt.value,
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{}), nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					return textResponse("unused", model.Usage{}), nil
				}),
			})
			if err == nil || !strings.Contains(err.Error(), "finite and non-negative") {
				t.Fatalf("NewController error = %v", err)
			}
		})
	}
}

func TestController_CostCeilingRejectsUntypedAndRawImageContent(t *testing.T) {
	raw := json.RawMessage(`{"type":"input_image","image_url":"data:image/png;base64,AA=="}`)
	for _, tt := range []struct {
		name    string
		content any
	}{
		{
			name: "concrete map slice",
			content: []map[string]any{
				{"type": "text", "text": "inspect"},
				{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid/image.png"}},
			},
		},
		{name: "raw JSON pointer", content: &raw},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modelCalls := 0
			priceCalls := 0
			ctrl, err := NewController(ControllerConfig{
				MaxCostUSD: 1,
				CostForUsage: func(model.Usage) (float64, error) {
					priceCalls++
					return 0, nil
				},
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "vision-model", Messages: []model.Message{{Role: "user", Content: tt.content}}}), nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					modelCalls++
					return textResponse("must not run", model.Usage{}), nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := ctrl.Run(t.Context())
			var incomplete *IncompleteTurnError
			if !errors.As(runErr, &incomplete) {
				t.Fatalf("Run error = %v, want incomplete", runErr)
			}
			if modelCalls != 0 || priceCalls != 0 || result.Termination.Kind != "cost_limit" {
				t.Fatalf("result=%+v model_calls=%d price_calls=%d", result, modelCalls, priceCalls)
			}
		})
	}
}

func TestContentContainsUnpricedImage_CyclicNonImageContentIsSafe(t *testing.T) {
	content := map[string]any{"type": "text", "text": "plain"}
	content["self"] = content
	if contentContainsUnpricedImage(content) {
		t.Fatal("cyclic text-only content reported an image")
	}
}

func TestController_CostNormalizerBytesAreIncludedBeforeAdmission(t *testing.T) {
	base := model.ChatRequest{Model: "test-model", MaxTokens: 10, Messages: []model.Message{{Role: "user", Content: "small"}}}
	rawBound, err := conservativeRequestInputTokenBound(base)
	if err != nil {
		t.Fatal(err)
	}
	maxCost := float64(rawBound+base.MaxTokens+1) / 1000
	modelCalls := 0
	maxPricedPrompt := 0
	normalizeCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: maxCost,
		CostForUsage: func(usage model.Usage) (float64, error) {
			maxPricedPrompt = max(maxPricedPrompt, usage.PromptTokens)
			return float64(usage.TotalTokens) / 1000, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			normalizeCalls++
			provider := make(map[string]any, len(req.Provider)+1)
			for key, value := range req.Provider {
				provider[key] = value
			}
			provider["wire_padding"] = strings.Repeat("x", 2048)
			req.Provider = provider
			return req, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return base, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("must not run", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete", runErr)
	}
	if modelCalls != 0 || normalizeCalls < 2 || normalizeCalls%2 != 0 {
		t.Fatalf("model_calls=%d normalize_calls=%d", modelCalls, normalizeCalls)
	}
	if maxPricedPrompt <= rawBound+1500 {
		t.Fatalf("priced prompt bound = %d, raw bound = %d; provider-added wire bytes were not reserved", maxPricedPrompt, rawBound)
	}
	if result.Termination.Kind != "cost_limit" {
		t.Fatalf("result=%+v", result)
	}
}

func TestController_CostNormalizerMustBeIdempotent(t *testing.T) {
	modelCalls := 0
	priceCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(model.Usage) (float64, error) {
			priceCalls++
			return 0, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			req.Transforms = append(req.Transforms, "changes-every-pass")
			return req, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 10}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("must not run", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete", runErr)
	}
	if modelCalls != 0 || priceCalls != 0 || !strings.Contains(result.Termination.Reason, "not idempotent") {
		t.Fatalf("result=%+v model_calls=%d price_calls=%d", result, modelCalls, priceCalls)
	}
}

func TestController_CostAdmissionUsesNormalizedModelContext(t *testing.T) {
	var contextModels []string
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1_000_000, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			req.Model = "wire-model"
			return req, nil
		},
		ContextWindow: func(modelID string) int {
			contextModels = append(contextModels, modelID)
			if modelID == "wire-model" {
				return 4096
			}
			return 64
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("unused", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	bounded, reservation, err := ctrl.reserveModelRequest(model.ChatRequest{Model: "alias-model"}, 0)
	if err != nil {
		t.Fatalf("reserveModelRequest: %v", err)
	}
	if bounded.Model != "wire-model" || reservation.outputTokens <= 0 || reservation.outputTokens >= fallbackCostBoundedOutputTokens {
		t.Fatalf("bounded=%+v reservation=%+v", bounded, reservation)
	}
	if len(contextModels) == 0 {
		t.Fatal("ContextWindow was not consulted")
	}
	for _, modelID := range contextModels {
		if modelID != "wire-model" {
			t.Fatalf("ContextWindow called with pre-normalized model %q: %v", modelID, contextModels)
		}
	}
}

func TestController_ConservativeInputBoundIsNotCappedByContextCatalog(t *testing.T) {
	payload := strings.Repeat("large-prompt-byte-", 512)
	maxPricedPrompt := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			maxPricedPrompt = max(maxPricedPrompt, usage.PromptTokens)
			return float64(usage.TotalTokens) / 1_000_000, nil
		},
		ContextWindow: func(string) int { return 32 },
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("unused", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, reservation, err := ctrl.reserveModelRequest(model.ChatRequest{
		Model:     "test-model",
		MaxTokens: 1,
		Messages:  []model.Message{{Role: "user", Content: payload}},
	}, 0)
	if err != nil {
		t.Fatalf("reserveModelRequest: %v", err)
	}
	if reservation.inputTokens <= 32 || maxPricedPrompt <= 32 {
		t.Fatalf("reservation=%+v max_priced_prompt=%d, want bound above stale 32-token context", reservation, maxPricedPrompt)
	}
}

func TestController_FinalizationEmptyChoicesStillAccountsUsageAndCost(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		MaxCostUSD:     10,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", MaxTokens: 10, Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 100}), nil
			}
			return &model.ChatResponse{Usage: model.Usage{TotalTokens: 200}}, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete", runErr)
	}
	// Empty-choices finals retry up to maxFinalizationAttempts, and every
	// attempt is billed: one tool round (100) plus three finals (200 each).
	if result.Usage.TotalTokens != 700 || math.Abs(result.CostUSD-0.7) > 1e-12 || modelCalls != 1+maxFinalizationAttempts {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
	if !strings.Contains(result.Termination.FinalizationError, "no response choices") {
		t.Fatalf("finalization error = %q", result.Termination.FinalizationError)
	}
}

func TestController_EmptyFirstRoundRetriesWithNudge(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	sawNudge := false
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				// Reasoning-only first turn: tool-free, empty content, but the
				// model genuinely ran (nonzero billed usage) -- this must take
				// the immediate corrective-nudge path, not transport-retry.
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: ""}}}, Usage: model.Usage{CompletionTokens: 5, TotalTokens: 5}, UsagePresent: true}, nil
			}
			for _, msg := range req.Messages {
				if text, _ := model.ExtractTextContent(msg.Content); strings.Contains(text, "not usable") {
					sawNudge = true
				}
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "recovered answer"}}}}, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return nil, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run error = %v, want recovery on retry", runErr)
	}
	if result.Content != "recovered answer" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 2 {
		t.Fatalf("model_calls = %d, want 2", modelCalls)
	}
	if !sawNudge {
		t.Fatal("retry request did not carry the corrective nudge")
	}
}

func TestController_FinalizationRetriesEmptyFinalThenSucceeds(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	sawNudge := false
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
			case 2:
				// First final: well-formed transport, empty text, but the
				// model genuinely ran (nonzero billed usage) -- this must
				// take the immediate corrective-nudge path, not
				// transport-retry.
				return &model.ChatResponse{
					Choices:      []model.Choice{{Message: model.Message{Role: "assistant", Content: "   "}}},
					Usage:        model.Usage{CompletionTokens: 4, TotalTokens: 4},
					UsagePresent: true,
				}, nil
			default:
				for _, msg := range req.Messages {
					if text, _ := model.ExtractTextContent(msg.Content); strings.Contains(text, "not a usable final answer") {
						sawNudge = true
					}
				}
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "final synthesis"}}}}, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run error = %v, want success after retry", runErr)
	}
	if result.Content != "final synthesis" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 3 {
		t.Fatalf("model_calls = %d, want 3 (tool round + empty final + retried final)", modelCalls)
	}
	if !sawNudge {
		t.Fatal("retry request did not carry the corrective nudge")
	}
}

func assertNoPrivateReasoningLeak(t *testing.T, result *Result, history *recordingHistory, raw string) {
	t.Helper()
	if strings.Contains(result.Content, raw) || strings.Contains(model.ExtractTextContentOrEmpty(result.Message.Content), raw) {
		t.Fatalf("private reasoning leaked through result: %+v", result)
	}
	if strings.Contains(result.Termination.Reason, raw) ||
		strings.Contains(result.Termination.FinalizationError, raw) ||
		strings.Contains(result.Termination.ProviderError, raw) ||
		strings.Contains(result.GuardDecision.Reason, raw) {
		t.Fatalf("private reasoning leaked through termination: %+v", result.Termination)
	}
	for _, msg := range history.messages {
		if strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), raw) || strings.Contains(msg.Reasoning, raw) {
			t.Fatalf("private reasoning leaked through history: %+v", history.messages)
		}
		for _, detail := range msg.ReasoningDetails {
			if strings.Contains(detail.Text, raw) || strings.Contains(detail.Summary, raw) || strings.Contains(detail.Data, raw) {
				t.Fatalf("private reasoning details leaked through history: %+v", history.messages)
			}
		}
	}
}

// TestController_ReasoningOnlyTerminalRetriesExhaustIncomplete covers
// Particle-style replies that carry private reasoning but no final-answer
// content. Reasoning proves the model ran, so the controller uses the
// bounded corrective-nudge path instead of transport backoff, but it must
// never promote that reasoning into Result.Content or final history.
func TestController_ReasoningOnlyTerminalRetriesExhaustIncomplete(t *testing.T) {
	store, err := runledger.New(t.TempDir() + "/ledger.db")
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	run, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	for _, tt := range []struct {
		name     string
		response func(int) *model.ChatResponse
		raw      string
	}{
		{
			name: "zero_usage_reasoning",
			raw:  "PRIVATE_ZERO_USAGE_REASONING",
			response: func(call int) *model.ChatResponse {
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{
						Role:      "assistant",
						Content:   "",
						Reasoning: fmt.Sprintf("PRIVATE_ZERO_USAGE_REASONING_%d", call),
					}}},
					Usage:        model.Usage{},
					UsagePresent: true,
				}
			},
		},
		{
			name: "nonzero_usage_reasoning",
			raw:  "PRIVATE_NONZERO_USAGE_REASONING",
			response: func(call int) *model.ChatResponse {
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{
						Role:      "assistant",
						Content:   "",
						Reasoning: fmt.Sprintf("PRIVATE_NONZERO_USAGE_REASONING_%d", call),
					}}},
					Usage:        model.Usage{CompletionTokens: 2, TotalTokens: 2},
					UsagePresent: true,
				}
			},
		},
		{
			name: "missing_usage_reasoning_details",
			raw:  "PRIVATE_REASONING_DETAILS",
			response: func(call int) *model.ChatResponse {
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{
						Role:    "assistant",
						Content: "",
						ReasoningDetails: []model.ReasoningDetail{{
							Type: "reasoning.text",
							Text: fmt.Sprintf("PRIVATE_REASONING_DETAILS_%d", call),
						}},
					}}},
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			history := &recordingHistory{}
			modelCalls := 0
			ctrl, err := NewController(ControllerConfig{
				Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
				FinalizeOnStop: true,
				RunLedger:      store,
				RunID:          run.RunID,
				SessionID:      "sess-1",
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
				},
				CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
					modelCalls++
					return tt.response(modelCalls), nil
				}),
				DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					return nil, nil
				}),
				History: history,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := ctrl.Run(t.Context())
			if runErr != nil {
				t.Fatalf("Run: %v", runErr)
			}
			if result.CompletionStatus != CompletionIncomplete || result.FinishReason != FinishReasonInvalidCompletion || result.RequireConclusive() == nil {
				t.Fatalf("result=%+v, want incomplete invalid completion", result)
			}
			if modelCalls != 1+maxEmptyTerminalRetries {
				t.Fatalf("model_calls = %d, want %d (initial + corrective nudges)", modelCalls, 1+maxEmptyTerminalRetries)
			}
			if len(history.messages) != maxEmptyTerminalRetries {
				t.Fatalf("history=%+v, want only corrective nudge messages", history.messages)
			}
			for _, msg := range history.messages {
				if msg.Role != "user" || !strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), "not usable") {
					t.Fatalf("history=%+v, want corrective nudge user messages", history.messages)
				}
			}
			assertNoPrivateReasoningLeak(t, result, history, tt.raw)
		})
	}

	events, err := store.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawFallbackDecision, sawEmptyRetryDecision bool
	for _, e := range events {
		if e.Type != runledger.EventControllerDecision {
			continue
		}
		if e.Payload["kind"] == "reasoning_fallback_final" {
			sawFallbackDecision = true
		}
		if e.Payload["kind"] == "empty_terminal_retry" {
			sawEmptyRetryDecision = true
		}
	}
	if sawFallbackDecision {
		t.Fatal("reasoning-only response was promoted through reasoning_fallback_final")
	}
	if !sawEmptyRetryDecision {
		t.Fatal("expected reasoning-only responses to use corrective nudge decisions")
	}
}

func TestController_MissingUsageReasoningOnlyCorrectiveNudgeCanRecover(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	sawNudge := false
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
					Role:      "assistant",
					Content:   "",
					Reasoning: "PRIVATE_CORRECTABLE_REASONING",
				}}}}, nil
			}
			for _, msg := range req.Messages {
				if strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), "not usable") {
					sawNudge = true
				}
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "plain final answer"}}}}, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return nil, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if result.Content != "plain final answer" || result.CompletionStatus != CompletionConclusive || modelCalls != 2 || !sawNudge {
		t.Fatalf("result=%+v model_calls=%d saw_nudge=%v", result, modelCalls, sawNudge)
	}
	assertNoPrivateReasoningLeak(t, result, history, "PRIVATE_CORRECTABLE_REASONING")
}

func TestController_ReasoningOnlyCorrectionRespectsModelRequestLimit(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:         New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		MaxModelRequests: 1,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
				Role:      "assistant",
				Content:   "",
				Reasoning: "PRIVATE_CAP_REASONING",
			}}}}, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "must not run", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if result.CompletionStatus != CompletionIncomplete || result.Termination.Kind != "model_request_limit" || result.RequireConclusive() == nil {
		t.Fatalf("result=%+v, want incomplete model_request_limit", result)
	}
	if modelCalls != 1 || dispatchCalls != 0 {
		t.Fatalf("model_calls=%d dispatch_calls=%d, want no extra model/tool effects", modelCalls, dispatchCalls)
	}
	assertNoPrivateReasoningLeak(t, result, history, "PRIVATE_CAP_REASONING")
}

func TestController_FinalizationReasoningOnlyExhaustsIncompleteWithoutLeak(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
			default:
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{
						Role:             "assistant",
						Content:          "",
						ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: "PRIVATE_FINAL_REASONING"}},
					}}},
					Usage:        model.Usage{CompletionTokens: 3, TotalTokens: 3},
					UsagePresent: true,
				}, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", runErr)
	}
	if modelCalls != 4 {
		t.Fatalf("model_calls = %d, want 4 (tool round + finalization attempts)", modelCalls)
	}
	if result.CompletionStatus != CompletionIncomplete || !result.Termination.FinalizationAttempted || result.Termination.FinalizationError == "" {
		t.Fatalf("result=%+v, want incomplete finalization failure", result)
	}
	assertNoPrivateReasoningLeak(t, result, history, "PRIVATE_FINAL_REASONING")
}

// TestController_TransportFailureEmptyResponseRetriesWithBackoffThenSucceeds
// covers the OpenRouter early-200 transport failure shell from the
// stealth/ox-alpha incident: an empty tool-free reply with no usage object
// at all (UsagePresent stays false, the JSON-decode default when the wire
// response never carried a "usage" key) must retry with backoff -- not the
// immediate corrective nudge -- and recover once the transport starts
// answering again, well within the bounded retry budget.
func TestController_TransportFailureEmptyResponseRetriesWithBackoffThenSucceeds(t *testing.T) {
	withoutTransportBackoff(t)
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls <= 2 {
				// No usage object at all: OpenRouter's early-committed-200
				// transport failure shell.
				return &model.ChatResponse{Choices: []model.Choice{{
					Message:            model.Message{Role: "assistant", Content: nil},
					NativeFinishReason: "network_error",
				}}}, nil
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "recovered after transport retries"}}}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run error = %v, want recovery once the transport starts answering", runErr)
	}
	if result.Content != "recovered after transport retries" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 3 {
		t.Fatalf("model_calls = %d, want 3 (two transport failures + the recovered reply)", modelCalls)
	}
}

// TestController_TransportFailureExhaustsWithoutTakingTheNudgePath covers
// the constraint that a transport-classified empty response never falls
// through to the immediate corrective-nudge retry once its own backoff
// budget is exhausted: it fails outright (no reasoning to fall back to
// here), and the model is called exactly 1 (initial) + maxTransportRetries
// times, never the smaller maxEmptyTerminalRetries-bounded count a nudge
// path would produce.
func TestController_TransportFailureExhaustsWithoutTakingTheNudgePath(t *testing.T) {
	withoutTransportBackoff(t)
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			return &model.ChatResponse{Choices: []model.Choice{{
				Message:            model.Message{Role: "assistant", Content: nil},
				NativeFinishReason: "network_error",
			}}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if result.CompletionStatus != CompletionIncomplete || result.FinishReason != FinishReasonInvalidCompletion {
		t.Fatalf("result=%+v, want an explicit incomplete candidate once transport retries are exhausted", result)
	}
	if modelCalls != 1+maxTransportRetries {
		t.Fatalf("model_calls = %d, want %d (1 initial + maxTransportRetries)", modelCalls, 1+maxTransportRetries)
	}
	if len(history.messages) != 0 {
		t.Fatalf("history=%+v, want no corrective-nudge messages appended for a transport-classified failure", history.messages)
	}
}

// TestController_TransportFailureZeroCompletionTokensWithNonzeroPromptTokensRetriesWithBackoff
// covers the classifier gap behind the 2026-08-23 incident autopsy: a
// response can legitimately bill nonzero prompt tokens for a request whose
// generation never started (the prompt was priced before the transport
// failure occurred), so requiring PromptTokens==0 too -- the pre-fix
// all-zero gate -- would let a nonzero prompt count defeat transport
// classification for what is still, by every other signal (no text, no
// completion tokens, no native_finish_reason), a degenerate transport-class
// response. It must take the backoff retry path, not the immediate
// corrective nudge.
func TestController_TransportFailureZeroCompletionTokensWithNonzeroPromptTokensRetriesWithBackoff(t *testing.T) {
	withoutTransportBackoff(t)
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls <= 2 {
				// Nonzero prompt tokens (the prompt was billed before
				// generation failed), zero completion tokens, and no
				// native_finish_reason at all: the pre-fix all-zero gate
				// would have missed this and taken the immediate nudge path.
				return &model.ChatResponse{
					Choices:      []model.Choice{{Message: model.Message{Role: "assistant", Content: nil}}},
					Usage:        model.Usage{PromptTokens: 500, CompletionTokens: 0, TotalTokens: 500},
					UsagePresent: true,
				}, nil
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "recovered after transport retries"}}}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run error = %v, want recovery once the transport starts answering", runErr)
	}
	if result.Content != "recovered after transport retries" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 3 {
		t.Fatalf("model_calls = %d, want 3 (two transport failures + the recovered reply)", modelCalls)
	}
	// Only the final, successful assistant message should be appended --
	// never a corrective-nudge user message, which would mean the response
	// took the immediate-nudge path instead of the backoff-retry path.
	for _, msg := range history.messages {
		if msg.Role == "user" {
			t.Fatalf("history=%+v, want no corrective-nudge user message for a transport-classified failure", history.messages)
		}
	}
}

// TestController_TransportFailureExhaustsWithFinalizeOnStopStillAttemptsFinalization
// covers the actual defect behind the 2026-08-23
// run_01M0RGJE21TW6HVH59DKCNME3Q / run_01M0RGF3Z9HZNZSK65ZZNV5FZQ incident: a
// turn whose very first round hits the OpenRouter transport-failure shell on
// every attempt, with zero tool calls made anywhere in the turn before
// transport retries exhaust. finalizeStoppedTurn used to be gated on
// Governor.ToolCalls() > 0 here -- appropriate for a deliberate governor
// stop with nothing new to summarize, but wrong for a transport outage that
// has nothing to do with tool-call history. That gate turned an ordinary,
// several-minute intermittent OpenRouter degradation into an unrecoverable
// run death (goal_engine's RequireConclusive check surfaced it as
// "agentloop: incomplete turn: model returned a final response without
// text") instead of the graceful finalization retry every other stop path
// already gets.
func TestController_TransportFailureExhaustsWithFinalizeOnStopStillAttemptsFinalization(t *testing.T) {
	withoutTransportBackoff(t)
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls <= 1+maxTransportRetries {
				return &model.ChatResponse{Choices: []model.Choice{{
					Message:            model.Message{Role: "assistant", Content: nil},
					NativeFinishReason: "network_error",
				}}}, nil
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "recovered during finalization"}}}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run error = %v, want finalizeStoppedTurn to recover even with zero tool calls made", runErr)
	}
	if result.CompletionStatus != CompletionConclusive || result.Content != "recovered during finalization" {
		t.Fatalf("result=%+v, want a conclusive answer from the finalization attempt", result)
	}
	if !result.Termination.FinalizationAttempted {
		t.Fatalf("result.Termination=%+v, want FinalizationAttempted", result.Termination)
	}
	wantCalls := 1 + maxTransportRetries + 1
	if modelCalls != wantCalls {
		t.Fatalf("model_calls = %d, want %d (main-loop transport exhaustion + one recovered finalization attempt)", modelCalls, wantCalls)
	}
}

// TestController_GenuinelyEmptyWithUsageTakesNudgeNotTransportRetry is the
// direct contrast case: a tool-free empty reply that carries confirmed
// nonzero usage is a genuine (if unhelpful) model answer, not a transport
// failure, so it must take the pre-existing immediate corrective-nudge path
// (no backoff wait, bounded at maxEmptyTerminalRetries) rather than the
// transport-retry classification. transportRetrySleep is deliberately left
// wired to its real implementation -- if this test ever misclassifies into
// the transport-retry bucket, it will time out instead of passing fast.
func TestController_GenuinelyEmptyWithUsageTakesNudgeNotTransportRetry(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return &model.ChatResponse{
					Choices:      []model.Choice{{Message: model.Message{Role: "assistant", Content: ""}}},
					Usage:        model.Usage{PromptTokens: 20, CompletionTokens: 8, TotalTokens: 28},
					UsagePresent: true,
				}, nil
			}
			for _, msg := range req.Messages {
				if text, _ := model.ExtractTextContent(msg.Content); strings.Contains(text, "not usable") {
					return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "answered on the nudge"}}}}, nil
				}
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "unexpected: no nudge seen"}}}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if result.Content != "answered on the nudge" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 2 {
		t.Fatalf("model_calls = %d, want 2 (immediate nudge retry, no backoff)", modelCalls)
	}
}

// TestController_SharedPoolRateLimitRetriesWithBackoffAndNeverAbortsOnFirst
// covers OpenRouter's non-standard 429
// {"limit_source":"upstream_provider_shared_pool"} response: an upstream
// provider's own shared quota, not an OpenRouter-side limit, which must
// never abort the run on its first occurrence.
func TestController_SharedPoolRateLimitRetriesWithBackoffAndNeverAbortsOnFirst(t *testing.T) {
	withoutTransportBackoff(t)
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 10, MaxToolCalls: 10}),
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return nil, &model.APIError{StatusCode: 429, LimitSource: model.SharedPoolLimitSource, Message: "upstream provider rate limit"}
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "answered after the shared-pool 429"}}}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run error = %v, want the shared-pool 429 to retry rather than abort the run", runErr)
	}
	if result.Content != "answered after the shared-pool 429" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 2 {
		t.Fatalf("model_calls = %d, want 2 (one shared-pool 429 + the recovered reply)", modelCalls)
	}
}

// TestController_FinalizationSharedPoolRateLimitRetriesThenSucceeds mirrors
// the live-round shared-pool 429 case for finalizeStoppedTurn: a 429 during
// final synthesis retries with backoff instead of failing the turn.
func TestController_FinalizationSharedPoolRateLimitRetriesThenSucceeds(t *testing.T) {
	withoutTransportBackoff(t)
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, _ model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
			case 2:
				return nil, &model.APIError{StatusCode: 429, LimitSource: model.SharedPoolLimitSource, Message: "upstream provider rate limit"}
			default:
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "final synthesis after the shared-pool 429"}}}}, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run error = %v, want the shared-pool 429 to retry finalization rather than fail it", runErr)
	}
	if result.Content != "final synthesis after the shared-pool 429" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 3 {
		t.Fatalf("model_calls = %d, want 3 (tool round + one shared-pool 429 + the recovered final synthesis)", modelCalls)
	}
}

func TestController_ExplicitModelRequestLimitIncludesFinalSynthesis(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:         New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 50}),
		FinalizeOnStop:   true,
		MaxModelRequests: 2,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
			}
			return textResponse("final within request ceiling", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || result.ModelRequests != 2 || modelCalls != 2 || result.Termination.Kind != "model_request_limit" {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
}

func TestController_ParallelBatchCannotOvershootToolCeiling(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatched := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 2}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls > 1 {
				return textResponse("answer from two admitted calls", model.Usage{}), nil
			}
			calls := make([]model.ToolCall, 4)
			for i := range calls {
				calls[i] = model.ToolCall{
					ID:   fmt.Sprintf("call-%d", i+1),
					Type: "function",
					Function: model.FunctionCall{
						Name:      "read_file",
						Arguments: fmt.Sprintf(`{"path":"file-%d"}`, i+1),
					},
				}
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", ToolCalls: calls}}}}, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			dispatched += len(calls)
			outcomes := make([]ToolOutcome, len(calls))
			for i := range outcomes {
				outcomes[i] = ToolOutcome{Content: "evidence", Success: true}
			}
			return outcomes, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if dispatched != 2 || result.ToolCalls != 2 {
		t.Fatalf("dispatched=%d result.ToolCalls=%d, want exact ceiling 2", dispatched, result.ToolCalls)
	}
	if len(history.messages) == 0 || len(history.messages[0].ToolCalls) != 2 {
		t.Fatalf("assistant transcript advertised calls that did not run: %+v", history.messages)
	}
	if result.CompletionStatus != CompletionConclusive || result.Termination.Kind != "tool_call_limit" {
		t.Fatalf("result = %+v", result)
	}
}

func TestController_EmptyChoicesReportsFinishReasonWithoutError(t *testing.T) {
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return &model.ChatResponse{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != FinishReasonEmptyChoices {
		t.Fatalf("FinishReason = %q, want %q", result.FinishReason, FinishReasonEmptyChoices)
	}
}

func TestController_CallModelErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return nil, wantErr
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	_, err = ctrl.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
}

func TestController_MissingToolDispatcherErrors(t *testing.T) {
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	if _, err := ctrl.Run(context.Background()); err == nil {
		t.Fatalf("expected an error when the model requests tools with no ToolDispatcher configured")
	}
}

func TestController_ToolDispatcherErrorAbortsTurn(t *testing.T) {
	wantErr := errors.New("approval wait cancelled")
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return toolCallResponse("call-1", "run_shell", `{}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			return nil, wantErr
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	_, err = ctrl.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
}

func TestController_RecordsRunLedgerEvents(t *testing.T) {
	store, err := runledger.New(t.TempDir() + "/ledger.db")
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	run, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return textResponse("done", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
		}),
		RunLedger: store,
		RunID:     run.RunID,
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	if _, err := ctrl.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, err := store.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawStarted, sawCompleted bool
	for _, e := range events {
		switch e.Type {
		case runledger.EventModelRequestStarted:
			sawStarted = true
		case runledger.EventModelRequestCompleted:
			sawCompleted = true
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("expected model.request_started and model.request_completed events, got %+v", events)
	}
}

func TestController_RecordsAndReplaysModelExecutionIdentity(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	identity := model.ExecutionIdentity{
		RequestedModel: "requested/model",
		SelectedModel:  "selected/model",
		ProviderID:     "provider-a",
		ResponseModel:  "reported/model-v1",
		ResponseID:     "resp-identity-1",
	}
	var lifecycleEvents []LifecycleEvent
	providerCalls := 0
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "dispatch/requested-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			response := textResponse("done", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
			response.ExecutionIdentity = &identity
			return response, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "sess-identity",
		TaskID:      "task-identity",
		TurnID:      "turn-identity",
		LifecycleObserver: func(event LifecycleEvent) {
			lifecycleEvents = append(lifecycleEvents, event)
		},
	}
	ctrl, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	result, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls = %d, want live call", providerCalls)
	}
	if len(result.ModelExecutions) != 1 || result.ModelExecutions[0] != identity {
		t.Fatalf("result model executions = %+v, want %+v", result.ModelExecutions, identity)
	}
	var liveResponse LifecycleEvent
	for _, event := range lifecycleEvents {
		if event.Type == LifecycleModelResponse {
			liveResponse = event
		}
	}
	if liveResponse.ModelID != "dispatch/requested-model" || liveResponse.RequestedModel != identity.RequestedModel || liveResponse.SelectedModel != identity.SelectedModel || liveResponse.ProviderID != identity.ProviderID || liveResponse.ResponseModel != identity.ResponseModel || liveResponse.ResponseID != identity.ResponseID {
		t.Fatalf("lifecycle model response = %+v, want separate dispatch/request/response identity", liveResponse)
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var completed runledger.Event
	for _, event := range events {
		if event.Type == runledger.EventModelRequestCompleted {
			completed = event
		}
	}
	if completed.ModelID != "dispatch/requested-model" || completed.ProviderID != identity.ProviderID || completed.Payload["selected_model"] != identity.SelectedModel || completed.Payload["response_model"] != identity.ResponseModel || completed.Payload["response_id"] != identity.ResponseID {
		t.Fatalf("runledger completed event = %+v", completed)
	}

	config.LifecycleObserver = nil
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, errors.New("replay must not call provider")
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController replay: %v", err)
	}
	replayed, err := replay.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls after replay = %d, want durable replay without provider", providerCalls)
	}
	if len(replayed.ModelExecutions) != 1 || replayed.ModelExecutions[0] != identity {
		t.Fatalf("replayed model executions = %+v, want old identity %+v", replayed.ModelExecutions, identity)
	}
}

func TestController_PreservesUnknownIdentityEntryBeforeKnownResponse(t *testing.T) {
	history := &recordingHistory{}
	known := model.ExecutionIdentity{
		RequestedModel: "requested/model",
		SelectedModel:  "requested/model",
		ProviderID:     "provider-a",
		ResponseModel:  "requested/model",
		ResponseID:     "resp-known",
	}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "requested/model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "inspect", `{}`, model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
			case 2:
				response := textResponse("done", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
				response.ExecutionIdentity = &known
				return response, nil
			default:
				t.Fatalf("unexpected model call %d", modelCalls)
				return nil, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "inspected", Success: true, StateObserved: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.ModelExecutions) != 2 {
		t.Fatalf("model executions = %+v, want unknown then known entries", result.ModelExecutions)
	}
	if result.ModelExecutions[0] != (model.ExecutionIdentity{}) || result.ModelExecutions[1] != known {
		t.Fatalf("model executions = %+v, want unknown then %+v", result.ModelExecutions, known)
	}
}

func TestController_RecordsModelExecutionIdentityFromPartialProviderError(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	identity := model.ExecutionIdentity{
		RequestedModel: "requested/model",
		SelectedModel:  "selected/model",
		ProviderID:     "provider-a",
		ResponseModel:  "reported/model",
		ResponseID:     "resp-partial",
	}
	providerErr := errors.New("provider stream ended early")
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "requested/model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			response := textResponse("partial text", model.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
			response.ExecutionIdentity = &identity
			return response, providerErr
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "sess-partial",
		TaskID:      "task-partial",
		TurnID:      "turn-partial",
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	result, err := ctrl.Run(context.Background())
	if !errors.Is(err, providerErr) {
		t.Fatalf("Run error = %v, want provider error", err)
	}
	if result == nil || len(result.ModelExecutions) != 1 || result.ModelExecutions[0] != identity {
		t.Fatalf("result model executions = %+v, want partial identity %+v", result, identity)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: runID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, event := range events {
		if event.Type == runledger.EventModelRequestFailed {
			if event.ProviderID != identity.ProviderID || event.Payload["response_id"] != identity.ResponseID {
				t.Fatalf("failed event = %+v, want partial identity metadata", event)
			}
			return
		}
	}
	t.Fatalf("missing partial model.request_failed event in %+v", events)
}

// TestTransportRetryBackoff_StartsNearThirtySecondsAndGrows covers the
// shape required for the transport-retry backoff: the first attempt centers
// near 30s, later attempts grow, and every attempt stays within
// transportRetryMaxInterval plus its jitter headroom.
func TestTransportRetryBackoff_StartsNearThirtySecondsAndGrows(t *testing.T) {
	first := transportRetryBackoff(1)
	if first < 15*time.Second || first > 40*time.Second {
		t.Fatalf("transportRetryBackoff(1) = %s, want roughly near 30s", first)
	}
	for attempt := 1; attempt <= maxTransportRetries; attempt++ {
		delay := transportRetryBackoff(attempt)
		if delay <= 0 {
			t.Fatalf("transportRetryBackoff(%d) = %s, want positive", attempt, delay)
		}
		// Growth is exponential with jitter, so a later attempt is not
		// guaranteed to exceed an earlier one on every draw, but the
		// backoff must never run away past its cap plus jitter headroom.
		if delay > transportRetryMaxInterval+transportRetryMaxInterval/2 {
			t.Fatalf("transportRetryBackoff(%d) = %s, exceeded the capped ceiling", attempt, delay)
		}
	}
	zero := transportRetryBackoff(0)
	if zero < 15*time.Second || zero > 40*time.Second {
		t.Fatalf("transportRetryBackoff(0) = %s, want it to normalize to attempt 1's range", zero)
	}
}

func TestBackfillToolCallIDs(t *testing.T) {
	calls := []model.ToolCall{{ID: "kept"}, {ID: ""}, {ID: ""}}
	got := BackfillToolCallIDs(calls)
	if got[0].ID != "kept" {
		t.Fatalf("expected an existing ID to survive, got %q", got[0].ID)
	}
	if got[1].ID != "tool-2" || got[2].ID != "tool-3" {
		t.Fatalf("expected positional backfilled IDs, got %q, %q", got[1].ID, got[2].ID)
	}
}

func TestGuardStopMessageDefaultsWhenReasonEmpty(t *testing.T) {
	msg := GuardStopMessage("")
	if msg == "" {
		t.Fatalf("expected a non-empty default message")
	}
}

func TestProjectForContinuation_NilCoordinatorIsPassthroughPin(t *testing.T) {
	req := model.ChatRequest{
		Model:    "test-model",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	}
	got := ProjectForContinuation(req, 0, nil, "", true)
	if len(got.Messages) != 1 || got.Messages[0].Content != "hi" {
		t.Fatalf("expected the message to survive an unpinned projection pass, got %+v", got.Messages)
	}
}
