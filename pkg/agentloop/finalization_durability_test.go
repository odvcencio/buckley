package agentloop

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestController_DurableFinalizationRetriesUseDistinctOrdinalsAndReplayWithoutRebuy(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	const (
		taskID = "task-finalization-ordinal"
		turnID = "turn-finalization-ordinal"
	)
	var providerCalls atomic.Int32
	var dispatchCalls atomic.Int32
	var stepIDs []string

	first := newFinalizationOrdinalController(t, ledger, ev, runID, taskID, turnID, &recordingHistory{},
		func(_ context.Context, call ModelDispatchCall) (*model.ChatResponse, error) {
			stepIDs = append(stepIDs, call.StepID)
			switch providerCalls.Add(1) {
			case 1:
				return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{TotalTokens: 3}), nil
			case 2:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 0) {
					t.Fatalf("first finalization step id = %q", call.StepID)
				}
				return toolCallResponse("disabled-tool", "search_text", `{"query":"must not dispatch"}`, model.Usage{CompletionTokens: 2, TotalTokens: 2}), nil
			case 3:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 1) {
					t.Fatalf("second finalization step id = %q", call.StepID)
				}
				if !strings.Contains(finalizationPrompt(call.Request), "model requested 1 tool call(s) while tools were disabled") {
					t.Fatalf("second finalization missing corrective cause: %+v", call.Request.Messages)
				}
				return textResponse("grounded final answer", model.Usage{CompletionTokens: 4, TotalTokens: 4}), nil
			default:
				t.Fatalf("unexpected provider call %d", providerCalls.Load())
				return nil, nil
			}
		},
		func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls.Add(1)
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		})

	result, err := first.Run(ctx)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if result.Content != "grounded final answer" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("first result=%+v", result)
	}
	if providerCalls.Load() != 3 || dispatchCalls.Load() != 1 {
		t.Fatalf("provider_calls=%d dispatch_calls=%d, want 3 and 1", providerCalls.Load(), dispatchCalls.Load())
	}
	for _, ordinal := range []int{0, 1} {
		stepID := StableStepID(runID, taskID, turnID, 1, "finalize", ordinal)
		step, err := ledger.GetStep(ctx, runID, stepID)
		if err != nil {
			t.Fatalf("GetStep %s: %v", stepID, err)
		}
		if step.Status != runledger.StepCompleted {
			t.Fatalf("step %s status=%q, want completed", stepID, step.Status)
		}
	}
	if len(stepIDs) != 3 || stepIDs[0] != StableStepID(runID, taskID, turnID, 1, "model", 0) {
		t.Fatalf("provider step ids = %v", stepIDs)
	}

	replay := newFinalizationOrdinalController(t, ledger, ev, runID, taskID, turnID, &recordingHistory{},
		func(context.Context, ModelDispatchCall) (*model.ChatResponse, error) {
			t.Fatal("replay re-called provider for completed durable steps")
			return nil, nil
		},
		func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			t.Fatal("replay re-dispatched completed tool step")
			return nil, nil
		})
	replayed, replayErr := replay.Run(ctx)
	if replayErr != nil {
		t.Fatalf("replay Run: %v", replayErr)
	}
	if replayed.Content != "grounded final answer" || replayed.ModelRequests != result.ModelRequests || replayed.ToolCalls != result.ToolCalls {
		t.Fatalf("replayed result=%+v want content/model/tool counts from first=%+v", replayed, result)
	}
}

func TestController_DurableFinalizationRestartAfterMalformedCandidateReplaysWithoutRebuy(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shared.db")
	blobRoot := filepath.Join(dir, "blobs")
	const (
		runID  = "run-finalization-restart"
		taskID = "task-finalization-restart"
		turnID = "turn-finalization-restart"
	)

	ledger, ev := openFinalizationOrdinalStores(t, dbPath, blobRoot)
	ctx := t.Context()
	if _, err := ledger.StartRun(ctx, runledger.AgentRun{RunID: runID, SessionID: "durable-finalization-restart"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	var firstProviderCalls atomic.Int32
	var firstDispatchCalls atomic.Int32
	var firstPriceCalls atomic.Int32
	first := newFinalizationOrdinalController(t, ledger, ev, runID, taskID, turnID, &recordingHistory{},
		func(_ context.Context, call ModelDispatchCall) (*model.ChatResponse, error) {
			switch firstProviderCalls.Add(1) {
			case 1:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "model", 0) {
					t.Fatalf("initial model step id = %q", call.StepID)
				}
				return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}), nil
			case 2:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 0) {
					t.Fatalf("malformed finalization step id = %q", call.StepID)
				}
				return toolCallResponse("disabled-tool", "search_text", `{"query":"must not dispatch"}`, model.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}), nil
			default:
				t.Fatalf("unexpected first provider call %d", firstProviderCalls.Load())
				return nil, nil
			}
		},
		func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			firstDispatchCalls.Add(1)
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		},
		func(cfg *ControllerConfig) {
			cfg.SessionID = "durable-finalization-restart"
			cfg.MaxModelRequests = 2
			cfg.CostForUsage = func(usage model.Usage) (float64, error) {
				firstPriceCalls.Add(1)
				return float64(usage.TotalTokens) / 1000, nil
			}
		})
	firstResult, firstErr := first.Run(ctx)
	if firstErr == nil {
		t.Fatal("first Run succeeded, want request cap after durable malformed finalization")
	}
	if firstResult == nil || firstResult.CompletionStatus != CompletionIncomplete || firstResult.ModelRequests != 2 || firstResult.ToolCalls != 1 {
		t.Fatalf("first result=%+v", firstResult)
	}
	if !strings.Contains(firstResult.Termination.FinalizationError, "request limit reached before a retry") {
		t.Fatalf("first finalization error = %q", firstResult.Termination.FinalizationError)
	}
	if firstProviderCalls.Load() != 2 || firstDispatchCalls.Load() != 1 || firstPriceCalls.Load() != 2 {
		t.Fatalf("first calls provider=%d dispatch=%d price=%d, want 2/1/2", firstProviderCalls.Load(), firstDispatchCalls.Load(), firstPriceCalls.Load())
	}
	if err := ev.Close(); err != nil {
		t.Fatalf("close first evidence store: %v", err)
	}

	ledger, ev = openFinalizationOrdinalStores(t, dbPath, blobRoot)
	var replayProviderCalls atomic.Int32
	var replayDispatchCalls atomic.Int32
	var replayPriceCalls atomic.Int32
	replay := newFinalizationOrdinalController(t, ledger, ev, runID, taskID, turnID, &recordingHistory{},
		func(_ context.Context, call ModelDispatchCall) (*model.ChatResponse, error) {
			replayProviderCalls.Add(1)
			if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 1) {
				t.Fatalf("restart dispatched step id = %q, want finalize ordinal 1", call.StepID)
			}
			prompt := finalizationPrompt(call.Request)
			if !strings.Contains(prompt, "model requested 1 tool call(s) while tools were disabled") {
				t.Fatalf("restart finalization missing corrective cause: %q", prompt)
			}
			return textResponse("final after restart", model.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}), nil
		},
		func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			replayDispatchCalls.Add(1)
			return []ToolOutcome{{Content: "tool should replay", Success: true}}, nil
		},
		func(cfg *ControllerConfig) {
			cfg.SessionID = "durable-finalization-restart"
			cfg.MaxModelRequests = 3
			cfg.CostForUsage = func(usage model.Usage) (float64, error) {
				replayPriceCalls.Add(1)
				return float64(usage.TotalTokens) / 1000, nil
			}
		})
	replayResult, replayErr := replay.Run(ctx)
	if replayErr != nil {
		t.Fatalf("restart Run: %v", replayErr)
	}
	if replayResult.Content != "final after restart" || replayResult.CompletionStatus != CompletionConclusive {
		t.Fatalf("restart result=%+v", replayResult)
	}
	if replayResult.ModelRequests != 3 || replayResult.ToolCalls != 1 {
		t.Fatalf("restart counts model=%d tools=%d, want 3 model requests and one replayed tool", replayResult.ModelRequests, replayResult.ToolCalls)
	}
	if replayProviderCalls.Load() != 1 || replayDispatchCalls.Load() != 0 || replayPriceCalls.Load() != 1 {
		t.Fatalf("restart calls provider=%d dispatch=%d price=%d, want only missing finalize001 priced once", replayProviderCalls.Load(), replayDispatchCalls.Load(), replayPriceCalls.Load())
	}
	if replayResult.Usage.TotalTokens != 15 || replayResult.CostUSD != 0.015 {
		t.Fatalf("restart accounting usage=%+v cost=%f, want replayed 3+5 plus new 7", replayResult.Usage, replayResult.CostUSD)
	}
}

func TestController_DurableFinalizationTransportShellRespectsRequestLimit(t *testing.T) {
	withoutTransportBackoff(t)
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	const (
		taskID = "task-finalization-transport-shell-limit"
		turnID = "turn-finalization-transport-shell-limit"
	)
	var providerCalls atomic.Int32

	ctrl := newFinalizationOrdinalController(t, ledger, ev, runID, taskID, turnID, &recordingHistory{},
		func(_ context.Context, call ModelDispatchCall) (*model.ChatResponse, error) {
			switch providerCalls.Add(1) {
			case 1:
				return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{TotalTokens: 3}), nil
			case 2:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 0) {
					t.Fatalf("transport-shell finalization step id = %q", call.StepID)
				}
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: ""}, NativeFinishReason: transportFailureFinishReason}},
				}, nil
			default:
				t.Fatalf("unexpected provider call %d; request limit should stop before finalize001", providerCalls.Load())
				return nil, nil
			}
		},
		func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		},
		func(cfg *ControllerConfig) {
			cfg.MaxModelRequests = 2
		})

	result, err := ctrl.Run(ctx)
	if err == nil {
		t.Fatal("Run succeeded, want explicit incomplete result at request limit")
	}
	if result == nil || result.CompletionStatus != CompletionIncomplete || providerCalls.Load() != 2 || result.ModelRequests != 2 {
		t.Fatalf("result=%+v provider_calls=%d", result, providerCalls.Load())
	}
	if !strings.Contains(result.Termination.FinalizationError, "request limit reached before a retry") {
		t.Fatalf("finalization error = %q", result.Termination.FinalizationError)
	}
	if _, err := ledger.GetStep(ctx, runID, StableStepID(runID, taskID, turnID, 1, "finalize", 1)); !errors.Is(err, runledger.ErrStepNotFound) {
		t.Fatalf("finalize001 exists or lookup failed: %v", err)
	}
}

func TestController_DurableFinalizationSharedPoolNilResponseRespectsRequestLimit(t *testing.T) {
	withoutTransportBackoff(t)
	for _, tc := range []struct {
		name    string
		durable bool
	}{
		{name: "without_journal"},
		{name: "with_journal", durable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ledger *runledger.SQLiteStore
			var ev *evidence.SQLiteStore
			runID := "run-finalization-shared-pool-limit-" + tc.name
			if tc.durable {
				ledger, ev, runID = newDurableControllerStores(t)
			}
			ctx := t.Context()
			const (
				taskID = "task-finalization-shared-pool-limit"
				turnID = "turn-finalization-shared-pool-limit"
			)
			var providerCalls atomic.Int32

			ctrl := newFinalizationOrdinalController(t, ledger, ev, runID, taskID, turnID, &recordingHistory{},
				func(context.Context, ModelDispatchCall) (*model.ChatResponse, error) {
					switch providerCalls.Add(1) {
					case 1:
						return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{TotalTokens: 3}), nil
					case 2:
						return nil, &model.APIError{StatusCode: 429, LimitSource: model.SharedPoolLimitSource, Message: "upstream provider rate limit"}
					default:
						t.Fatalf("unexpected provider call %d; request limit should stop before retrying nil-response shared-pool finalization", providerCalls.Load())
						return nil, nil
					}
				},
				func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
				},
				func(cfg *ControllerConfig) {
					cfg.MaxModelRequests = 2
				})

			result, err := ctrl.Run(ctx)
			if err == nil {
				t.Fatal("Run succeeded, want explicit incomplete result at request limit")
			}
			if result == nil || result.CompletionStatus != CompletionIncomplete || providerCalls.Load() != 2 || result.ModelRequests != 2 {
				t.Fatalf("result=%+v provider_calls=%d", result, providerCalls.Load())
			}
			if !strings.Contains(result.Termination.FinalizationError, "request limit reached before a retry") {
				t.Fatalf("finalization error = %q", result.Termination.FinalizationError)
			}
			if tc.durable {
				if _, err := ledger.GetStep(ctx, runID, StableStepID(runID, taskID, turnID, 1, "finalize", 1)); !errors.Is(err, runledger.ErrStepNotFound) {
					t.Fatalf("finalize001 exists or lookup failed: %v", err)
				}
			}
		})
	}
}

func TestController_DurableFinalizationTransportShellUsesNewOrdinalWithoutNewCorrection(t *testing.T) {
	withoutTransportBackoff(t)
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	const (
		taskID = "task-finalization-transport-shell"
		turnID = "turn-finalization-transport-shell"
	)
	var providerCalls atomic.Int32

	ctrl := newFinalizationOrdinalController(t, ledger, ev, runID, taskID, turnID, &recordingHistory{},
		func(_ context.Context, call ModelDispatchCall) (*model.ChatResponse, error) {
			switch providerCalls.Add(1) {
			case 1:
				return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{TotalTokens: 3}), nil
			case 2:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 0) {
					t.Fatalf("first finalization step id = %q", call.StepID)
				}
				return toolCallResponse("disabled-tool", "search_text", `{}`, model.Usage{CompletionTokens: 2, TotalTokens: 2}), nil
			case 3:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 1) {
					t.Fatalf("transport-shell finalization step id = %q", call.StepID)
				}
				prompt := finalizationPrompt(call.Request)
				if !strings.Contains(prompt, "model requested 1 tool call(s) while tools were disabled") {
					t.Fatalf("transport-shell retry lost prior genuine correction: %q", prompt)
				}
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: ""}, NativeFinishReason: transportFailureFinishReason}},
				}, nil
			case 4:
				if call.StepID != StableStepID(runID, taskID, turnID, 1, "finalize", 2) {
					t.Fatalf("post-transport finalization step id = %q", call.StepID)
				}
				prompt := finalizationPrompt(call.Request)
				if !strings.Contains(prompt, "model requested 1 tool call(s) while tools were disabled") {
					t.Fatalf("post-transport retry lost prior genuine correction: %q", prompt)
				}
				if strings.Contains(prompt, "model returned a final response without text") || strings.Contains(prompt, "network_error") {
					t.Fatalf("transport shell became a corrective nudge: %q", prompt)
				}
				return textResponse("final after transport shell", model.Usage{CompletionTokens: 4, TotalTokens: 4}), nil
			default:
				t.Fatalf("unexpected provider call %d", providerCalls.Load())
				return nil, nil
			}
		},
		func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		})

	result, err := ctrl.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Content != "final after transport shell" || result.CompletionStatus != CompletionConclusive {
		t.Fatalf("result=%+v", result)
	}
	if providerCalls.Load() != 4 {
		t.Fatalf("provider_calls=%d, want 4", providerCalls.Load())
	}
	for _, ordinal := range []int{0, 1, 2} {
		stepID := StableStepID(runID, taskID, turnID, 1, "finalize", ordinal)
		step, err := ledger.GetStep(ctx, runID, stepID)
		if err != nil {
			t.Fatalf("GetStep %s: %v", stepID, err)
		}
		if step.Status != runledger.StepCompleted {
			t.Fatalf("step %s status=%q, want completed", stepID, step.Status)
		}
	}
}

func newFinalizationOrdinalController(
	t *testing.T,
	ledger *runledger.SQLiteStore,
	ev *evidence.SQLiteStore,
	runID, taskID, turnID string,
	history *recordingHistory,
	call func(context.Context, ModelDispatchCall) (*model.ChatResponse, error),
	dispatch func(context.Context, []model.ToolCall) ([]ToolOutcome, error),
	opts ...func(*ControllerConfig),
) *Controller {
	t.Helper()
	cfg := ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			messages := []model.Message{{Role: "user", Content: "answer from the tool evidence"}}
			messages = append(messages, history.messages...)
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: messages}), nil
		},
		CallModel: ContextualModelCallerFunc(call),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			return dispatch(ctx, calls)
		}),
		History:   history,
		RunID:     runID,
		SessionID: "durable-finalization-test",
		TaskID:    taskID,
		TurnID:    turnID,
	}
	if ledger != nil {
		cfg.RunLedger = ledger
		cfg.StepJournal = ledger
	}
	if ev != nil {
		cfg.Evidence = ev
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	ctrl, err := NewController(cfg)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return ctrl
}

func openFinalizationOrdinalStores(t *testing.T, dbPath, blobRoot string) (*runledger.SQLiteStore, *evidence.SQLiteStore) {
	t.Helper()
	ev, err := evidence.New(dbPath, evidence.WithBlobRoot(blobRoot))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	return ledger, ev
}

func finalizationPrompt(req model.ChatRequest) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return model.ExtractTextContentOrEmpty(req.Messages[len(req.Messages)-1].Content)
}
