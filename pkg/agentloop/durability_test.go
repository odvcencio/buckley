package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	config := ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			priceCalls++
			if usage.TotalTokens == 321 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
				return 0, testingError("catalog price unavailable after response")
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
	stepID := StableStepID(runID, config.TaskID, config.TurnID, 1, "model", 0)
	step, err := ledger.GetStep(ctx, runID, stepID)
	if err != nil {
		t.Fatalf("GetStep: %v", err)
	}
	if step.Status != runledger.StepCompleted || step.OutputEvidenceID == "" {
		t.Fatalf("pricing-error step=%+v, want completed response evidence", step)
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
	if replayResult.Termination.Kind != "cost_limit" || !strings.Contains(replayResult.Termination.Reason, "catalog price unavailable") {
		t.Fatalf("replay result=%+v", replayResult)
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

func TestController_DispatchErrorPersistsSuccessfulPrefixForRetry(t *testing.T) {
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
			case 2:
				if len(calls) != 1 || calls[0].ID != "inspect-2" {
					t.Fatalf("retry dispatch calls = %+v, want only unresolved suffix", calls)
				}
				suffixExecutions++
				return []ToolOutcome{{Content: "new state", Success: true, EffectClass: "readonly"}}, nil
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
	if suffix.Status != runledger.StepFailed {
		t.Fatalf("suffix step = %+v, want failed unresolved call", suffix)
	}

	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController retry: %v", err)
	}
	result, err := second.Run(ctx)
	if err != nil {
		t.Fatalf("retry Run: %v", err)
	}
	if got, _ := result.Message.Content.(string); got != "completed after retry" {
		t.Fatalf("retry result = %q, want completed after retry", got)
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls = %d, want first tool round plus retry terminal round", providerCalls)
	}
	if dispatchAttempts != 2 || modifyingExecutions != 1 || suffixExecutions != 1 {
		t.Fatalf("dispatch attempts = %d, modifying executions = %d, suffix executions = %d; want 2, 1, 1", dispatchAttempts, modifyingExecutions, suffixExecutions)
	}
	prefix, err = ledger.GetStep(ctx, runID, prefixStepID)
	if err != nil {
		t.Fatalf("GetStep replayed prefix: %v", err)
	}
	if prefix.Status != runledger.StepCompleted || prefix.Attempt != 1 {
		t.Fatalf("replayed prefix step = %+v, want immutable completed attempt 1", prefix)
	}
	suffix, err = ledger.GetStep(ctx, runID, suffixStepID)
	if err != nil {
		t.Fatalf("GetStep completed suffix: %v", err)
	}
	if suffix.Status != runledger.StepCompleted || suffix.Attempt != 2 {
		t.Fatalf("retried suffix step = %+v, want completed attempt 2", suffix)
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
