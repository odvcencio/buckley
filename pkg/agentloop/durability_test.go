package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func newDurableControllerStores(t *testing.T) (*runledger.SQLiteStore, *evidence.SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	runID := "run-durable"
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: runID, SessionID: "durable-test"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return ledger, ev, runID
}

func TestController_ReplaysCompletedModelStepWithoutCallingProvider(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	providerCalls := 0
	build := func(context.Context, int) (model.ChatRequest, error) {
		return model.ChatRequest{Model: "test-model"}, nil
	}
	call := func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return textResponse("durable result", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
	}
	config := ControllerConfig{
		BuildRequest: build,
		CallModel:    ModelCallerFunc(call),
		RunLedger:    ledger,
		Evidence:     ev,
		StepJournal:  ledger,
		RunID:        runID,
		SessionID:    "durable-test",
		TaskID:       "task-1",
		TurnID:       "task-1/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	secondProviderCalls := 0
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		secondProviderCalls++
		return nil, testingError("provider should not be called during replay")
	})
	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController second: %v", err)
	}
	result, err := second.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if secondProviderCalls != 0 || providerCalls != 1 {
		t.Fatalf("provider calls = first %d, replay %d; want 1 and 0", providerCalls, secondProviderCalls)
	}
	if got, _ := result.Message.Content.(string); got != "durable result" {
		t.Fatalf("replayed message = %q, want durable result", got)
	}
}

func TestController_FailedToolEventCarriesBoundedDiagnostics(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	modelCalls := 0
	var toolError LifecycleEvent
	secret := "sk-abcdefghijklmnopqrstuvwxyz123456"
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-failed", "run_shell", `{"command":"false"}`, model.Usage{}), nil
			}
			return textResponse("reported failure", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{
				Content: "Error: command failed",
				Error:   "command failed with token " + secret,
				Stderr:  "fatal stderr " + secret,
			}}, nil
		}),
		LifecycleObserver: func(event LifecycleEvent) {
			if event.Type == LifecycleToolError {
				toolError = event
			}
		},
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-failed-tool",
		TurnID:      "task-failed-tool/cp-001/turn-000",
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, err := ctrl.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if toolError.ToolName != "run_shell" || toolError.ToolCallID != "call-failed" {
		t.Fatalf("tool error identity = %+v", toolError)
	}
	if toolError.Error == "" || toolError.Stderr == "" || len(toolError.EvidenceIDs) != 1 {
		t.Fatalf("tool error diagnostics = %+v, want error, stderr, and evidence", toolError)
	}
	if strings.Contains(toolError.Error, secret) || strings.Contains(toolError.Stderr, secret) {
		t.Fatalf("tool error leaked secret: %+v", toolError)
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID, Types: []string{runledger.EventToolFailed}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].Payload["tool"] != "run_shell" || events[0].Payload["error"] == "" || events[0].Payload["stderr"] == "" {
		t.Fatalf("failed tool ledger event = %+v", events)
	}
}

func TestController_ReplaysOriginalChargedCostWithoutRepricing(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	providerCalls := 0
	firstPriceCalls := 0
	config := ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			firstPriceCalls++
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 100}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return textResponse("durable priced result", model.Usage{TotalTokens: 250}), nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-priced",
		TurnID:      "task-priced/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	firstResult, err := first.Run(ctx)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if firstResult.CostUSD != 0.25 || providerCalls != 1 || firstPriceCalls == 0 {
		t.Fatalf("first result=%+v provider_calls=%d price_calls=%d", firstResult, providerCalls, firstPriceCalls)
	}

	replayPriceCalls := 0
	config.CostForUsage = func(model.Usage) (float64, error) {
		replayPriceCalls++
		return 99, nil
	}
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, testingError("provider should not run during priced replay")
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	replayedResult, err := replay.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if replayedResult.CostUSD != 0.25 || providerCalls != 1 || replayPriceCalls != 0 {
		t.Fatalf("replay result=%+v provider_calls=%d price_calls=%d", replayedResult, providerCalls, replayPriceCalls)
	}
}

func TestController_ReplayAccountingPersistsOnceAcrossRunContinuations(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	providerCalls := 0
	config := ControllerConfig{
		MaxCostUSD:       1,
		MaxModelRequests: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 10}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return textResponse("durable result", model.Usage{TotalTokens: 100}), nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-replay-totals",
		TurnID:      "task-replay-totals/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	replayPriceCalls := 0
	config.CostForUsage = func(model.Usage) (float64, error) {
		replayPriceCalls++
		return 99, nil
	}
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, testingError("provider should not run during replay")
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := replay.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if replayed.ModelRequests != 1 || replayed.Usage.TotalTokens != 100 || replayed.CostUSD != 0.1 {
		t.Fatalf("replayed result=%+v", replayed)
	}

	continued, err := replay.Run(ctx)
	if err != nil {
		t.Fatalf("continued Run: %v", err)
	}
	if continued.ModelRequests != 1 || continued.Usage.TotalTokens != 100 || continued.CostUSD != 0.1 {
		t.Fatalf("continued result double-counted replay=%+v", continued)
	}
	if providerCalls != 1 || replayPriceCalls != 0 {
		t.Fatalf("provider_calls=%d replay_price_calls=%d", providerCalls, replayPriceCalls)
	}
}

func TestController_ReplaysPersistedOverCeilingResponseWithoutDispatch(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	providerCalls := 0
	dispatchCalls := 0
	config := ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 100}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return toolCallResponse("call-1", "write_file", `{}`, model.Usage{TotalTokens: 2000}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "must not execute", Success: true}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-overrun",
		TurnID:      "task-overrun/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	firstResult, firstErr := first.Run(ctx)
	var incomplete *IncompleteTurnError
	if !errors.As(firstErr, &incomplete) || firstResult.CostUSD != 2 {
		t.Fatalf("first result=%+v error=%v", firstResult, firstErr)
	}

	priceCalls := 0
	config.CostForUsage = func(model.Usage) (float64, error) {
		priceCalls++
		return 0, nil
	}
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, testingError("provider should not run during overrun replay")
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	replayedResult, replayErr := replay.Run(ctx)
	if !errors.As(replayErr, &incomplete) {
		t.Fatalf("replay error=%v, want incomplete", replayErr)
	}
	if replayedResult.CostUSD != 2 || providerCalls != 1 || dispatchCalls != 0 || priceCalls != 0 {
		t.Fatalf("replay result=%+v provider_calls=%d dispatch_calls=%d price_calls=%d", replayedResult, providerCalls, dispatchCalls, priceCalls)
	}
}

func TestController_LegacyResponseReplayFailsClosedUnderCostCeiling(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	providerCalls := 0
	dispatchCalls := 0
	request := model.ChatRequest{Model: "test-model", MaxTokens: 100}
	projected := ProjectForContinuation(request, 0, nil, "", false)
	inputDigest, err := jsonDigest(projected)
	if err != nil {
		t.Fatal(err)
	}
	stepID := StableStepID(runID, "task-legacy-cost", "task-legacy-cost/cp-001/turn-000", 1, "model", 0)
	if _, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{
		RunID:          runID,
		TaskID:         "task-legacy-cost",
		StepID:         stepID,
		Kind:           "model",
		IdempotencyKey: stepID,
		InputDigest:    inputDigest,
	}); err != nil {
		t.Fatalf("BeginStep: %v", err)
	}
	legacyBody, err := json.Marshal(toolCallResponse("call-1", "write_file", `{}`, model.Usage{TotalTokens: 100}))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ev.Put(ctx, evidence.Object{
		Kind:       evidence.KindModelResponse,
		MediaType:  "application/json",
		InlineBody: legacyBody,
	})
	if err != nil {
		t.Fatalf("put legacy response evidence: %v", err)
	}
	if err := ledger.CompleteStep(ctx, runID, stepID, legacy.ID, legacy.ContentSHA256, time.Now().UTC()); err != nil {
		t.Fatalf("CompleteStep: %v", err)
	}
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return request, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return toolCallResponse("call-1", "write_file", `{}`, model.Usage{TotalTokens: 100}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "legacy execution", Success: true}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-legacy-cost",
		TurnID:      "task-legacy-cost/cp-001/turn-000",
	}
	priceCalls := 0
	config.MaxCostUSD = 1
	config.CostForUsage = func(model.Usage) (float64, error) {
		priceCalls++
		return 0.01, nil
	}
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, testingError("provider should not run during legacy replay")
	})
	config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
		dispatchCalls++
		return []ToolOutcome{{Content: "must not execute", Success: true}}, nil
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	result, replayErr := replay.Run(ctx)
	var incomplete *IncompleteTurnError
	if !errors.As(replayErr, &incomplete) {
		t.Fatalf("replay error=%v, want incomplete", replayErr)
	}
	if providerCalls != 0 || dispatchCalls != 0 || priceCalls != 0 {
		t.Fatalf("provider_calls=%d dispatch_calls=%d price_calls=%d", providerCalls, dispatchCalls, priceCalls)
	}
	if result.Termination.Kind != "cost_limit" || !strings.Contains(result.Termination.Reason, "no original charged cost") {
		t.Fatalf("result=%+v", result)
	}
}

func TestController_IdenticalResponsesAtDifferentPricesKeepDistinctReplayCharges(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	response := textResponse("same bytes", model.Usage{TotalTokens: 100})
	type fixture struct {
		taskID   string
		turnID   string
		cost     float64
		evidence string
	}
	fixtures := []fixture{
		{taskID: "task-price-a", turnID: "task-price-a/cp-001/turn-000", cost: 0.10},
		{taskID: "task-price-b", turnID: "task-price-b/cp-001/turn-000", cost: 0.60},
	}
	for i := range fixtures {
		item := &fixtures[i]
		config := ControllerConfig{
			MaxCostUSD: 1,
			CostForUsage: func(model.Usage) (float64, error) {
				return item.cost, nil
			},
			BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
				return model.ChatRequest{Model: "test-model", MaxTokens: 100}, nil
			},
			CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
				return response, nil
			}),
			RunLedger:   ledger,
			Evidence:    ev,
			StepJournal: ledger,
			RunID:       runID,
			SessionID:   "durable-test",
			TaskID:      item.taskID,
			TurnID:      item.turnID,
		}
		controller, err := NewController(config)
		if err != nil {
			t.Fatal(err)
		}
		result, err := controller.Run(ctx)
		if err != nil {
			t.Fatalf("initial %s Run: %v", item.taskID, err)
		}
		if result.CostUSD != item.cost {
			t.Fatalf("initial %s cost=%v, want %v", item.taskID, result.CostUSD, item.cost)
		}
		step, err := ledger.GetStep(ctx, runID, StableStepID(runID, item.taskID, item.turnID, 1, "model", 0))
		if err != nil {
			t.Fatalf("GetStep %s: %v", item.taskID, err)
		}
		item.evidence = step.OutputEvidenceID
	}
	if fixtures[0].evidence == "" || fixtures[0].evidence == fixtures[1].evidence {
		t.Fatalf("evidence IDs = %q and %q, want distinct cost-bearing bodies", fixtures[0].evidence, fixtures[1].evidence)
	}

	for i := range fixtures {
		item := fixtures[i]
		priceCalls := 0
		providerCalls := 0
		config := ControllerConfig{
			MaxCostUSD: 1,
			CostForUsage: func(model.Usage) (float64, error) {
				priceCalls++
				return 0.99, nil
			},
			BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
				return model.ChatRequest{Model: "test-model", MaxTokens: 100}, nil
			},
			CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
				providerCalls++
				return nil, testingError("provider should not run during replay")
			}),
			RunLedger:   ledger,
			Evidence:    ev,
			StepJournal: ledger,
			RunID:       runID,
			SessionID:   "durable-test",
			TaskID:      item.taskID,
			TurnID:      item.turnID,
		}
		controller, err := NewController(config)
		if err != nil {
			t.Fatal(err)
		}
		result, err := controller.Run(ctx)
		if err != nil {
			t.Fatalf("replay %s Run: %v", item.taskID, err)
		}
		if result.CostUSD != item.cost || priceCalls != 0 || providerCalls != 0 {
			t.Fatalf("replay %s result=%+v price_calls=%d provider_calls=%d", item.taskID, result, priceCalls, providerCalls)
		}
	}
}

func TestController_PricingFailurePersistsResponseAndReplaysWithoutProvider(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	providerCalls := 0
	priceCalls := 0
	dispatchCalls := 0
	secret := "sk-" + strings.Repeat("p", 30)
	rawPricingError := "catalog price unavailable " + secret + " " + strings.Repeat("x", modelstep.MaxPersistedErrorRunes+100)
	persistedPricingError := modelstep.NormalizeErrorText("agentloop: price model usage: " + rawPricingError)
	config := ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			priceCalls++
			if usage.TotalTokens == 321 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
				return 0, testingError(rawPricingError)
			}
			return float64(usage.TotalTokens) / 1_000_000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 100}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return toolCallResponse("call-1", "write_file", `{}`, model.Usage{TotalTokens: 321}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "must not execute", Success: true}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-pricing-error",
		TurnID:      "task-pricing-error/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	firstResult, firstErr := first.Run(ctx)
	var incomplete *IncompleteTurnError
	if !errors.As(firstErr, &incomplete) {
		t.Fatalf("first error=%v, want incomplete", firstErr)
	}
	if providerCalls != 1 || dispatchCalls != 0 || priceCalls == 0 || firstResult.Usage.TotalTokens != 321 {
		t.Fatalf("first result=%+v provider_calls=%d dispatch_calls=%d price_calls=%d", firstResult, providerCalls, dispatchCalls, priceCalls)
	}
	if firstResult.Partial || firstResult.Termination.ProviderError != "" || firstResult.Termination.Kind != "cost_limit" {
		t.Fatalf("pricing failure was mislabeled as provider partial: %+v", firstResult)
	}
	stepID := StableStepID(runID, config.TaskID, config.TurnID, 1, "model", 0)
	step, err := ledger.GetStep(ctx, runID, stepID)
	if err != nil {
		t.Fatalf("GetStep: %v", err)
	}
	if step.Status != runledger.StepCompleted || step.OutputEvidenceID == "" {
		t.Fatalf("pricing-error step=%+v, want completed response evidence", step)
	}
	object, err := ev.Get(ctx, step.OutputEvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := modelstep.ValidateResponseEvidence(step.OutputEvidenceID, step.OutputDigest, object)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PricingError != persistedPricingError || strings.Contains(decoded.PricingError, secret) || len([]rune(decoded.PricingError)) > modelstep.MaxPersistedErrorRunes {
		t.Fatalf("persisted pricing error = %q", decoded.PricingError)
	}

	replayPriceCalls := 0
	config.CostForUsage = func(model.Usage) (float64, error) {
		replayPriceCalls++
		return 0, nil
	}
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return nil, testingError("provider should not run during pricing-error replay")
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	replayResult, replayErr := replay.Run(ctx)
	if !errors.As(replayErr, &incomplete) {
		t.Fatalf("replay error=%v, want incomplete", replayErr)
	}
	if providerCalls != 1 || dispatchCalls != 0 || replayPriceCalls != 0 || replayResult.Usage.TotalTokens != 321 {
		t.Fatalf("replay result=%+v provider_calls=%d dispatch_calls=%d price_calls=%d", replayResult, providerCalls, dispatchCalls, replayPriceCalls)
	}
	if replayResult.Termination.Kind != "cost_limit" || !strings.Contains(replayResult.Termination.Reason, persistedPricingError) {
		t.Fatalf("replay result=%+v", replayResult)
	}
	if replayResult.Partial || replayResult.Termination.ProviderError != "" {
		t.Fatalf("replayed pricing failure was mislabeled as provider partial: %+v", replayResult)
	}
}

func TestController_ProviderPartialDurabilityFailureBlocksBillableRetry(t *testing.T) {
	for _, tt := range []struct {
		name              string
		failEvidence      bool
		wantReplayContent bool
	}{
		{name: "response evidence write", failEvidence: true},
		{name: "step completion", wantReplayContent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev, runID := newDurableControllerStores(t)
			ctx := t.Context()
			secret := "sk-" + strings.Repeat("a", 30)
			providerErr := errors.New("provider stream failed " + secret + " " + strings.Repeat("x", blockedModelStepErrorRunes+100))
			persistedProviderError := modelstep.NormalizeError(providerErr)
			durabilityErr := errors.New("durability unavailable after response")
			providerCalls := 0

			var evidenceStore evidence.Store = ev
			var stepJournal DurableStepJournal = ledger
			if tt.failEvidence {
				evidenceStore = &modelResponseFailingEvidenceStore{Store: ev, failure: durabilityErr}
			} else {
				stepJournal = &failingCompleteStepJournal{BlockingStepJournal: ledger, failure: durabilityErr}
			}
			config := ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return model.ChatRequest{Model: "test-model"}, nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					providerCalls++
					return textResponse("partial response before durability failure", model.Usage{TotalTokens: 123}), providerErr
				}),
				CostForUsage: func(model.Usage) (float64, error) { return 0.42, nil },
				RunLedger:    ledger,
				Evidence:     evidenceStore,
				StepJournal:  stepJournal,
				RunID:        runID,
				SessionID:    "durable-test",
				TaskID:       "task-partial-durability-" + strings.ReplaceAll(tt.name, " ", "-"),
				TurnID:       "partial-durability-turn-" + strings.ReplaceAll(tt.name, " ", "-"),
			}
			first, err := NewController(config)
			if err != nil {
				t.Fatalf("NewController first: %v", err)
			}
			firstResult, firstErr := first.Run(ctx)
			var incomplete *IncompleteTurnError
			if !errors.As(firstErr, &incomplete) || !errors.Is(firstErr, providerErr) || !errors.Is(firstErr, durabilityErr) {
				t.Fatalf("first error = %v, want incomplete, provider, and durability causes", firstErr)
			}
			if firstResult == nil || !firstResult.Partial || firstResult.Content != "partial response before durability failure" || firstResult.Termination.ProviderError != persistedProviderError {
				t.Fatalf("first result = %+v", firstResult)
			}
			stepID := StableStepID(runID, config.TaskID, config.TurnID, 1, "model", 0)
			step, err := ledger.GetStep(ctx, runID, stepID)
			if err != nil {
				t.Fatalf("GetStep first: %v", err)
			}
			if step.Status != runledger.StepBlocked || step.Attempt != 1 || !strings.HasPrefix(step.Error, blockedModelStepErrorPrefix) {
				t.Fatalf("retry block step = %+v", step)
			}
			if strings.Contains(step.Error, secret) || !strings.Contains(step.Error, persistedProviderError) {
				t.Fatalf("retry block persisted unsafe provider error: %q", step.Error)
			}

			config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
				providerCalls++
				return nil, errors.New("blocked retry must not call provider")
			})
			replay, err := NewController(config)
			if err != nil {
				t.Fatalf("NewController replay: %v", err)
			}
			replayResult, replayErr := replay.Run(ctx)
			if !errors.As(replayErr, &incomplete) {
				t.Fatalf("replay error = %v, want IncompleteTurnError", replayErr)
			}
			if providerCalls != 1 || replayResult == nil || !replayResult.Partial || replayResult.Termination.ProviderError != persistedProviderError {
				t.Fatalf("replay provider_calls=%d result=%+v", providerCalls, replayResult)
			}
			if tt.wantReplayContent && (replayResult.Content != "partial response before durability failure" || replayResult.Usage.TotalTokens != 123 || replayResult.CostUSD != 0.42) {
				t.Fatalf("replayed projection = %+v", replayResult)
			}
			events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				providerProjection, _ := event.Payload["provider_error"].(string)
				if providerProjection != "" && providerProjection != persistedProviderError {
					t.Fatalf("event provider error = %q, want normalized projection", providerProjection)
				}
				if strings.Contains(providerProjection, secret) {
					t.Fatalf("event leaked provider secret: %q", providerProjection)
				}
			}
			step, err = ledger.GetStep(ctx, runID, stepID)
			if err != nil {
				t.Fatalf("GetStep replay: %v", err)
			}
			if step.Attempt != 1 || step.Status != runledger.StepBlocked {
				t.Fatalf("blocked retry advanced step: %+v", step)
			}
		})
	}
}

func TestController_ProviderPartialUsesPrimaryTerminalBlockedStep(t *testing.T) {
	for _, tt := range []struct {
		name              string
		failEvidence      bool
		wantReplayContent bool
	}{
		{name: "without response evidence", failEvidence: true},
		{name: "with response evidence", wantReplayContent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev, runID := newDurableControllerStores(t)
			ctx := t.Context()
			providerErr := errors.New("provider stream failed after response")
			durabilityErr := errors.New("response completion persistence failed")
			providerCalls := 0

			journal := &trackingBlockingJournal{
				BlockingStepJournal: ledger,
			}
			var evidenceStore evidence.Store = ev
			if tt.failEvidence {
				evidenceStore = &modelResponseFailingEvidenceStore{Store: ev, failure: durabilityErr}
			} else {
				journal.completeFailure = durabilityErr
			}
			config := ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return model.ChatRequest{Model: "test-model"}, nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					providerCalls++
					return textResponse("partial response before terminal block", model.Usage{TotalTokens: 123}), providerErr
				}),
				RunLedger:   ledger,
				Evidence:    evidenceStore,
				StepJournal: journal,
				RunID:       runID,
				SessionID:   "durable-test",
				TaskID:      "task-terminal-block-" + strings.ReplaceAll(tt.name, " ", "-"),
				TurnID:      "terminal-block-turn-" + strings.ReplaceAll(tt.name, " ", "-"),
			}
			first, err := NewController(config)
			if err != nil {
				t.Fatalf("NewController first: %v", err)
			}
			firstResult, firstErr := first.Run(ctx)
			var incomplete *IncompleteTurnError
			if !errors.As(firstErr, &incomplete) || !errors.Is(firstErr, providerErr) || !errors.Is(firstErr, durabilityErr) {
				t.Fatalf("first error = %v, want incomplete, provider, and durability causes", firstErr)
			}
			if firstResult == nil || !firstResult.Partial || firstResult.Content != "partial response before terminal block" || firstResult.Termination.ProviderError != providerErr.Error() {
				t.Fatalf("first result = %+v", firstResult)
			}
			wantCompleteCalls := 1
			if tt.failEvidence {
				wantCompleteCalls = 0
			}
			if journal.completeCalls != wantCompleteCalls || journal.blockCalls != 1 {
				t.Fatalf("journal calls complete=%d block=%d", journal.completeCalls, journal.blockCalls)
			}

			stepID := StableStepID(runID, config.TaskID, config.TurnID, 1, "model", 0)
			step, err := ledger.GetStep(ctx, runID, stepID)
			if err != nil {
				t.Fatalf("GetStep first: %v", err)
			}
			if step.Status != runledger.StepBlocked || step.Attempt != 1 || !strings.HasPrefix(step.Error, blockedModelStepErrorPrefix) {
				t.Fatalf("terminal retry block step = %+v", step)
			}
			if (step.OutputEvidenceID != "") != tt.wantReplayContent {
				t.Fatalf("terminal retry block evidence = %q, want_present=%v", step.OutputEvidenceID, tt.wantReplayContent)
			}
			var record blockedModelStepRecord
			if err := json.Unmarshal([]byte(strings.TrimPrefix(step.Error, blockedModelStepErrorPrefix)), &record); err != nil {
				t.Fatalf("decode terminal retry block: %v", err)
			}
			if record.ProviderError != providerErr.Error() || !strings.Contains(record.DurabilityError, durabilityErr.Error()) {
				t.Fatalf("terminal retry block record = %+v", record)
			}

			config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
				providerCalls++
				return nil, errors.New("terminally blocked retry must not call provider")
			})
			replay, err := NewController(config)
			if err != nil {
				t.Fatalf("NewController replay: %v", err)
			}
			replayResult, replayErr := replay.Run(ctx)
			if !errors.As(replayErr, &incomplete) || !strings.Contains(replayErr.Error(), providerErr.Error()) || !strings.Contains(replayErr.Error(), durabilityErr.Error()) {
				t.Fatalf("replay error = %v, want incomplete and all persisted causes", replayErr)
			}
			if providerCalls != 1 || replayResult == nil || !replayResult.Partial || replayResult.Termination.ProviderError != providerErr.Error() {
				t.Fatalf("replay provider_calls=%d result=%+v", providerCalls, replayResult)
			}
			if tt.wantReplayContent && replayResult.Content != "partial response before terminal block" {
				t.Fatalf("replayed content = %q", replayResult.Content)
			}
			if journal.completeCalls != wantCompleteCalls || journal.blockCalls != 1 {
				t.Fatalf("replay mutated journal calls complete=%d block=%d", journal.completeCalls, journal.blockCalls)
			}
			step, err = ledger.GetStep(ctx, runID, stepID)
			if err != nil {
				t.Fatalf("GetStep replay: %v", err)
			}
			if step.Status != runledger.StepBlocked || step.Attempt != 1 {
				t.Fatalf("terminal retry block advanced: %+v", step)
			}
		})
	}
}

func TestBoundedModelStepError_RedactsAndBounds(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 24)
	got := boundedModelStepError(errors.New(secret + strings.Repeat("x", blockedModelStepErrorRunes+100)))
	if strings.Contains(got, secret) || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("bounded error did not redact secret: %q", got)
	}
	if len([]rune(got)) > blockedModelStepErrorRunes {
		t.Fatalf("bounded error has %d runes", len([]rune(got)))
	}
}

func TestController_ConcurrentRunsDoNotDuplicateProviderCall(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	entered := make(chan struct{})
	release := make(chan struct{})
	var providerCalls atomic.Int32
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			if providerCalls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return textResponse("one provider response", model.Usage{TotalTokens: 1}), nil
		}),
		RunLedger: ledger, Evidence: ev, StepJournal: ledger,
		RunID: runID, SessionID: "durable-test", TaskID: "task-concurrent", TurnID: "turn-concurrent",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewController(config)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result *Result
		err    error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		result, err := first.Run(ctx)
		firstDone <- outcome{result: result, err: err}
	}()
	<-entered
	secondResult, secondErr := second.Run(ctx)
	if !errors.Is(secondErr, runledger.ErrStepInProgress) {
		t.Fatalf("concurrent Run result=%+v error=%v, want step in progress", secondResult, secondErr)
	}
	close(release)
	firstOutcome := <-firstDone
	if firstOutcome.err != nil || firstOutcome.result == nil || firstOutcome.result.Content != "one provider response" {
		t.Fatalf("first Run result=%+v error=%v", firstOutcome.result, firstOutcome.err)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestController_ReclaimsPredispatchClaimWithNewFence(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	req := model.ChatRequest{Model: "test-model"}
	inputDigest, err := jsonDigest(ProjectForContinuation(req, 0, nil, "", false))
	if err != nil {
		t.Fatal(err)
	}
	stepID := StableStepID(runID, "task-reclaim", "turn-reclaim", 1, "model", 0)
	deadOwner, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: runID, TaskID: "task-reclaim", StepID: stepID, Kind: "model", InputDigest: inputDigest})
	if err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	controller, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) { return req, nil },
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls.Add(1)
			return textResponse("resumed safely", model.Usage{TotalTokens: 1}), nil
		}),
		RunLedger: ledger, Evidence: ev, StepJournal: ledger,
		RunID: runID, SessionID: "durable-test", TaskID: "task-reclaim", TurnID: "turn-reclaim",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(ctx)
	if err != nil || result == nil || result.Content != "resumed safely" || providerCalls.Load() != 1 {
		t.Fatalf("Run result=%+v provider_calls=%d err=%v", result, providerCalls.Load(), err)
	}
	got, err := ledger.GetStep(ctx, runID, stepID)
	if err != nil || got.Status != runledger.StepCompleted || got.ClaimGeneration != deadOwner.ClaimGeneration+1 {
		t.Fatalf("reclaimed step=%+v err=%v", got, err)
	}
	if err := ledger.MarkStepDispatched(ctx, deadOwner, time.Now().UTC()); !errors.Is(err, runledger.ErrStepTransitionConflict) {
		t.Fatalf("dead owner dispatch error = %v", err)
	}
}

func TestController_SuccessfulProviderPostDispatchFailuresBlockRestart(t *testing.T) {
	for _, failurePoint := range []string{"response encoding", "evidence write", "step completion"} {
		t.Run(failurePoint, func(t *testing.T) {
			ledger, ev, runID := newDurableControllerStores(t)
			ctx := t.Context()
			failure := errors.New(failurePoint + " failed")
			response := textResponse("already purchased", model.Usage{TotalTokens: 9})
			var evidenceStore evidence.Store = ev
			var journal DurableStepJournal = ledger
			switch failurePoint {
			case "response encoding":
				response.Usage.CompletionTokenDetails = &model.CompletionTokenDetails{ReasoningTokens: -1}
			case "evidence write":
				evidenceStore = &modelResponseFailingEvidenceStore{Store: ev, failure: failure}
			case "step completion":
				journal = &failingCompleteStepJournal{BlockingStepJournal: ledger, failure: failure}
			}
			var providerCalls atomic.Int32
			config := ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return model.ChatRequest{Model: "test-model"}, nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					providerCalls.Add(1)
					return response, nil
				}),
				RunLedger: ledger, Evidence: evidenceStore, StepJournal: journal,
				RunID: runID, SessionID: "durable-test", TaskID: "task-post-dispatch-" + strings.ReplaceAll(failurePoint, " ", "-"), TurnID: "turn-post-dispatch",
			}
			first, err := NewController(config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.Run(ctx); err == nil {
				t.Fatal("first Run succeeded despite post-dispatch persistence failure")
			}
			stepID := StableStepID(runID, config.TaskID, config.TurnID, 1, "model", 0)
			step, err := ledger.GetStep(ctx, runID, stepID)
			if err != nil || step.Status != runledger.StepBlocked || step.DispatchState != runledger.StepDispatchDispatched {
				t.Fatalf("blocked step=%+v err=%v", step, err)
			}

			config.Evidence = ev
			config.StepJournal = ledger
			config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
				providerCalls.Add(1)
				return textResponse("duplicate", model.Usage{}), nil
			})
			restart, err := NewController(config)
			if err != nil {
				t.Fatal(err)
			}
			_, restartErr := restart.Run(ctx)
			var recovery *runledger.StepRecoveryError
			if !errors.As(restartErr, &recovery) || recovery.Action != runledger.StepRecoveryRerun {
				t.Fatalf("restart error = %v, want reconciliation/rerun", restartErr)
			}
			if providerCalls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", providerCalls.Load())
			}
		})
	}
}

func TestController_InspectsBlockedStatusReturnedByBeginStep(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	req := model.ChatRequest{Model: "test-model"}
	inputDigest, err := jsonDigest(ProjectForContinuation(req, 0, nil, "", false))
	if err != nil {
		t.Fatal(err)
	}
	stepID := StableStepID(runID, "task-begin-blocked", "turn-begin-blocked", 1, "model", 0)
	step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: runID, TaskID: "task-begin-blocked", StepID: stepID, Kind: "model", InputDigest: inputDigest})
	if err != nil {
		t.Fatal(err)
	}
	record := blockedModelStepRecord{Version: modelstep.BlockedMarkerVersion, Incomplete: true, ProviderError: "provider failed", DurabilityError: "response persistence failed"}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BlockStep(ctx, step, blockedModelStepErrorPrefix+string(encoded), "", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	journal := &hideFirstGetStepJournal{BlockingStepJournal: ledger}
	var providerCalls atomic.Int32
	controller, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) { return req, nil },
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls.Add(1)
			return nil, errors.New("provider must not run")
		}),
		RunLedger: ledger, Evidence: ev, StepJournal: journal,
		RunID: runID, SessionID: "durable-test", TaskID: "task-begin-blocked", TurnID: "turn-begin-blocked",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := controller.Run(ctx)
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) || result == nil || !result.Partial || result.Termination.ProviderError != record.ProviderError {
		t.Fatalf("Run result=%+v error=%v", result, runErr)
	}
	if got := providerCalls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestController_BlockedReplayValidatesMarkerAndEvidenceIntegrity(t *testing.T) {
	for _, tt := range []struct {
		name      string
		corrupt   string
		wantError string
	}{
		{name: "marker evidence mismatch", corrupt: "marker", wantError: "does not match the execution step"},
		{name: "content digest mismatch", corrupt: "digest", wantError: "digest mismatch"},
		{name: "response shape mismatch", corrupt: "shape", wantError: "conclusive model response evidence"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ledger, ev, runID := newDurableControllerStores(t)
			ctx := t.Context()
			req := model.ChatRequest{Model: "test-model"}
			inputDigest, err := jsonDigest(ProjectForContinuation(req, 0, nil, "", false))
			if err != nil {
				t.Fatal(err)
			}
			providerError := "provider failed after partial response"
			envelope := modelResponseEvidenceEnvelope{
				Version:  modelResponseEvidenceVersion,
				Response: textResponse("untrusted partial", model.Usage{TotalTokens: 99}),
				Partial:  true, ProviderError: providerError,
			}
			if tt.corrupt == "shape" {
				envelope.Partial = false
			}
			body, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			obj, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: body})
			if err != nil {
				t.Fatal(err)
			}
			stepID := StableStepID(runID, "task-integrity", "turn-"+tt.corrupt, 1, "model", 0)
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: runID, TaskID: "task-integrity", StepID: stepID, Kind: "model", InputDigest: inputDigest})
			if err != nil {
				t.Fatal(err)
			}
			evidenceID, outputDigest := obj.ID, obj.ContentSHA256
			record := blockedModelStepRecord{Version: modelstep.BlockedMarkerVersion, Incomplete: true, ProviderError: providerError, DurabilityError: "response persistence failed", ResponseEvidenceID: evidenceID, OutputDigest: outputDigest}
			if tt.corrupt == "marker" {
				record.ResponseEvidenceID = "ev_marker_mismatch"
			}
			if tt.corrupt == "digest" {
				outputDigest = strings.Repeat("0", 64)
				record.OutputDigest = outputDigest
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.BlockStep(ctx, step, blockedModelStepErrorPrefix+string(encoded), evidenceID, outputDigest, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			var providerCalls atomic.Int32
			var pricingCalls atomic.Int32
			controller, err := NewController(ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) { return req, nil },
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					providerCalls.Add(1)
					return nil, errors.New("provider must not run")
				}),
				CostForUsage: func(model.Usage) (float64, error) { pricingCalls.Add(1); return 1, nil },
				RunLedger:    ledger, Evidence: ev, StepJournal: ledger,
				RunID: runID, SessionID: "durable-test", TaskID: "task-integrity", TurnID: "turn-" + tt.corrupt,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := controller.Run(ctx)
			if runErr == nil || !strings.Contains(runErr.Error(), tt.wantError) || result == nil {
				t.Fatalf("Run result=%+v error=%v", result, runErr)
			}
			if tt.corrupt != "marker" && (!result.Partial || result.Termination.ProviderError != providerError) {
				t.Fatalf("Run result=%+v error=%v, want preserved validated provider projection", result, runErr)
			}
			if providerCalls.Load() != 0 || pricingCalls.Load() != 0 || result.Usage.TotalTokens != 0 {
				t.Fatalf("provider=%d pricing=%d usage=%+v", providerCalls.Load(), pricingCalls.Load(), result.Usage)
			}
		})
	}
}

func TestController_ReplaysToolResultWithoutExecutingDispatcher(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	providerCalls := 0
	dispatchCalls := 0
	build := func(context.Context, int) (model.ChatRequest, error) {
		return model.ChatRequest{Model: "test-model"}, nil
	}
	firstProvider := func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		if providerCalls == 1 {
			return toolCallResponse("call-1", "read_file", `{}`, model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
		}
		return textResponse("after tool", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
	}
	config := ControllerConfig{
		BuildRequest: build,
		CallModel:    ModelCallerFunc(firstProvider),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "file contents", Success: true, EffectClass: "modifying"}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-1",
		TurnID:      "task-1/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	replayProviderCalls := 0
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		replayProviderCalls++
		return nil, testingError("provider should not be called during tool replay")
	})
	config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
		return nil, testingError("dispatcher should not be called during tool replay")
	})
	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController second: %v", err)
	}
	result, err := second.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if replayProviderCalls != 0 || dispatchCalls != 1 {
		t.Fatalf("replay calls = provider %d, original dispatch %d; want 0 and 1", replayProviderCalls, dispatchCalls)
	}
	if got, _ := result.Message.Content.(string); got != "after tool" {
		t.Fatalf("replayed final message = %q, want after tool", got)
	}
}

func TestController_DispatchErrorBlocksAmbiguousSuffixUntilRerun(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	firstCtx, cancelFirst := context.WithCancel(ctx)
	providerCalls := 0
	dispatchAttempts := 0
	modifyingExecutions := 0
	suffixExecutions := 0

	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			if providerCalls == 1 {
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
					Role: "assistant",
					ToolCalls: []model.ToolCall{
						{ID: "modify-1", Type: "function", Function: model.FunctionCall{Name: "write_file", Arguments: `{"path":"state.txt"}`}},
						{ID: "inspect-2", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"state.txt"}`}},
					},
				}}}}, nil
			}
			return textResponse("completed after retry", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			dispatchAttempts++
			switch dispatchAttempts {
			case 1:
				if len(calls) != 2 || calls[0].Function.Name != "write_file" || calls[1].Function.Name != "read_file" {
					t.Fatalf("first dispatch calls = %+v, want modifying call followed by suffix", calls)
				}
				modifyingExecutions++
				cancelFirst()
				return []ToolOutcome{{Content: "write committed", Success: true, EffectClass: "modifying"}}, context.Canceled
			default:
				t.Fatalf("unexpected dispatch attempt %d with calls %+v", dispatchAttempts, calls)
				return nil, nil
			}
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-1",
		TurnID:      "task-1/cp-001/turn-000",
	}

	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(firstCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run error = %v, want context canceled", err)
	}

	prefixStepID := StableStepID(runID, "task-1", config.TurnID, 1, "tool", 0)
	prefix, err := ledger.GetStep(ctx, runID, prefixStepID)
	if err != nil {
		t.Fatalf("GetStep prefix: %v", err)
	}
	if prefix.Status != runledger.StepCompleted || prefix.OutputEvidenceID == "" {
		t.Fatalf("prefix step = %+v, want completed durable result", prefix)
	}
	suffixStepID := StableStepID(runID, "task-1", config.TurnID, 1, "tool", 1)
	suffix, err := ledger.GetStep(ctx, runID, suffixStepID)
	if err != nil {
		t.Fatalf("GetStep suffix: %v", err)
	}
	if suffix.Status != runledger.StepBlocked || suffix.DispatchState != runledger.StepDispatchDispatched {
		t.Fatalf("suffix step = %+v, want blocked dispatched call", suffix)
	}

	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController retry: %v", err)
	}
	_, err = second.Run(ctx)
	var recovery *runledger.StepRecoveryError
	if !errors.As(err, &recovery) || recovery.Action != runledger.StepRecoveryRerun {
		t.Fatalf("retry error = %v, want explicit rerun recovery", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls = %d, want original model request only", providerCalls)
	}
	if dispatchAttempts != 1 || modifyingExecutions != 1 || suffixExecutions != 0 {
		t.Fatalf("dispatch attempts = %d, modifying executions = %d, suffix executions = %d; want 1, 1, 0", dispatchAttempts, modifyingExecutions, suffixExecutions)
	}
	prefix, err = ledger.GetStep(ctx, runID, prefixStepID)
	if err != nil {
		t.Fatalf("GetStep replayed prefix: %v", err)
	}
	if prefix.Status != runledger.StepCompleted || prefix.Attempt != 1 {
		t.Fatalf("replayed prefix step = %+v, want immutable completed attempt 1", prefix)
	}
	suffix, err = ledger.GetStep(ctx, runID, suffixStepID)
	if err != nil || suffix.Status != runledger.StepBlocked || suffix.Attempt != 1 {
		t.Fatalf("blocked suffix step = %+v, err=%v", suffix, err)
	}
}

func TestController_LargeToolResultStaysInEvidence(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	largeResult := strings.Repeat("payload-probe-", 1<<16) // ~900 KiB
	providerCalls := 0
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			if providerCalls == 1 {
				return toolCallResponse("call-1", "read_file", `{}`, model.Usage{}), nil
			}
			return textResponse("done", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: largeResult, Success: true, EffectClass: "readonly"}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-1",
		TurnID:      "task-1/cp-001/turn-000",
	}
	controller, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, err := controller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var completed *runledger.Event
	for i := range events {
		if events[i].Type == runledger.EventToolCompleted {
			completed = &events[i]
		}
	}
	if completed == nil {
		t.Fatalf("events = %d, want a tool.completed event", len(events))
	}
	encoded, err := json.Marshal(completed.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("tool.completed payload is %d bytes, want compact evidence-referenced payload", len(encoded))
	}
	if strings.Contains(string(encoded), "payload-probe-payload-probe-") {
		t.Fatalf("tool.completed payload embeds the tool result body")
	}
	evidenceID, _ := completed.Payload["output_evidence_id"].(string)
	if evidenceID == "" {
		t.Fatalf("payload = %s, want output_evidence_id", encoded)
	}
	obj, err := ev.Get(ctx, evidenceID)
	if err != nil {
		t.Fatalf("evidence Get %s: %v", evidenceID, err)
	}
	if !strings.Contains(string(obj.InlineBody), "payload-probe-") || len(obj.InlineBody) < len(largeResult) {
		t.Fatalf("evidence body = %d bytes, want the full tool result", len(obj.InlineBody))
	}
}

func TestController_StepEvidenceSurvivesRetentionUntilRunReleased(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("pinned result", model.Usage{}), nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-1",
		TurnID:      "task-1/cp-001/turn-000",
	}
	controller, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, err := controller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A sweep with the cutoff far in the future reaps every unpinned
	// ephemeral object; the run's step evidence must survive it.
	future := time.Now().UTC().Add(365 * 24 * time.Hour)
	removed, err := ev.Sweep(ctx, evidence.DefaultRetentionPolicy(), future)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("sweep removed %v, want pinned step evidence retained", removed)
	}
	step, err := ledger.GetStep(ctx, runID, StableStepID(runID, "task-1", "task-1/cp-001/turn-000", 1, "model", 0))
	if err != nil {
		t.Fatalf("GetStep: %v", err)
	}
	if _, err := ev.Get(ctx, step.OutputEvidenceID); err != nil {
		t.Fatalf("output evidence gone after sweep: %v", err)
	}

	// Pruning the run releases the pins; the next sweep reaps.
	released, err := ev.ReleaseByReason(ctx, RunPinReason(runID))
	if err != nil {
		t.Fatalf("ReleaseByReason: %v", err)
	}
	if released == 0 {
		t.Fatal("ReleaseByReason released nothing, want the run's step pins")
	}
	removed, err = ev.Sweep(ctx, evidence.DefaultRetentionPolicy(), future)
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if len(removed) == 0 {
		t.Fatal("sweep after release removed nothing, want step evidence reaped")
	}
}

type testError string

func (e testError) Error() string { return string(e) }

func testingError(message string) error { return testError(message) }

type hideFirstGetStepJournal struct {
	runledger.BlockingStepJournal
	getCalls atomic.Int32
}

func (j *hideFirstGetStepJournal) GetStep(ctx context.Context, runID, stepID string) (runledger.ExecutionStep, error) {
	if j.getCalls.Add(1) == 1 {
		return runledger.ExecutionStep{}, runledger.ErrStepNotFound
	}
	return j.BlockingStepJournal.GetStep(ctx, runID, stepID)
}

func (j *hideFirstGetStepJournal) MarkStepDispatched(ctx context.Context, step runledger.ExecutionStep, at time.Time) error {
	return j.BlockingStepJournal.(runledger.DispatchStepJournal).MarkStepDispatched(ctx, step, at)
}

func (j *hideFirstGetStepJournal) ReclaimStep(ctx context.Context, step runledger.ExecutionStep, at time.Time) (runledger.ExecutionStep, error) {
	return j.BlockingStepJournal.(runledger.FencedStepJournal).ReclaimStep(ctx, step, at)
}

type modelResponseFailingEvidenceStore struct {
	evidence.Store
	failure error
}

func (s *modelResponseFailingEvidenceStore) Put(ctx context.Context, object evidence.Object) (evidence.Object, error) {
	if object.Kind == evidence.KindModelResponse {
		return evidence.Object{}, s.failure
	}
	return s.Store.Put(ctx, object)
}

type failingCompleteStepJournal struct {
	runledger.BlockingStepJournal
	failure error
}

func (j *failingCompleteStepJournal) CompleteStepAttempt(context.Context, runledger.ExecutionStep, string, string, time.Time) error {
	return j.failure
}

func (j *failingCompleteStepJournal) MarkStepDispatched(ctx context.Context, step runledger.ExecutionStep, at time.Time) error {
	return j.BlockingStepJournal.(runledger.DispatchStepJournal).MarkStepDispatched(ctx, step, at)
}

func (j *failingCompleteStepJournal) ReclaimStep(ctx context.Context, step runledger.ExecutionStep, at time.Time) (runledger.ExecutionStep, error) {
	return j.BlockingStepJournal.(runledger.FencedStepJournal).ReclaimStep(ctx, step, at)
}

type trackingBlockingJournal struct {
	runledger.BlockingStepJournal
	completeFailure error
	completeCalls   int
	blockCalls      int
}

func (j *trackingBlockingJournal) CompleteStepAttempt(ctx context.Context, step runledger.ExecutionStep, evidenceID, outputDigest string, completedAt time.Time) error {
	j.completeCalls++
	if j.completeFailure != nil {
		return j.completeFailure
	}
	return j.BlockingStepJournal.CompleteStepAttempt(ctx, step, evidenceID, outputDigest, completedAt)
}

func (j *trackingBlockingJournal) BlockStep(ctx context.Context, step runledger.ExecutionStep, failure, evidenceID, outputDigest string, completedAt time.Time) error {
	j.blockCalls++
	return j.BlockingStepJournal.BlockStep(ctx, step, failure, evidenceID, outputDigest, completedAt)
}

func (j *trackingBlockingJournal) MarkStepDispatched(ctx context.Context, step runledger.ExecutionStep, at time.Time) error {
	return j.BlockingStepJournal.(runledger.DispatchStepJournal).MarkStepDispatched(ctx, step, at)
}

func (j *trackingBlockingJournal) ReclaimStep(ctx context.Context, step runledger.ExecutionStep, at time.Time) (runledger.ExecutionStep, error) {
	return j.BlockingStepJournal.(runledger.FencedStepJournal).ReclaimStep(ctx, step, at)
}
