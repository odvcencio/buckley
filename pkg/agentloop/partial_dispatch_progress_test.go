package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestController_PartialDispatchSurfacesPrefixProgressAndKeepsContinuationIncomplete(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()

	history := &recordingHistory{}
	modelCalls := 0
	dispatchAttempts := 0
	prefixEffects := 0
	suffixEffects := 0

	const (
		taskID = "task-partial-dispatch"
		turnID = "task-partial-dispatch/cp-001/turn-000"
	)
	config := ControllerConfig{
		CompletionContract: &CompletionContract{
			RequirePostChangeVerification: true,
		},
		MaxModelRequests: 2,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
					Role: "assistant",
					ToolCalls: []model.ToolCall{
						{ID: "modify-prefix", Type: "function", Function: model.FunctionCall{Name: "edit_file", Arguments: `{"path":"state.txt"}`}},
						{ID: "unknown-suffix", Type: "function", Function: model.FunctionCall{Name: "run_tests", Arguments: `{}`}},
					},
				}}}}, nil
			default:
				t.Fatalf("unexpected model call %d; interrupted tool round should block continuation before another model request", modelCalls)
				return nil, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			dispatchAttempts++
			if dispatchAttempts != 1 {
				t.Fatalf("unexpected repeated dispatch attempt %d with calls %+v", dispatchAttempts, calls)
			}
			if len(calls) != 2 || calls[0].ID != "modify-prefix" || calls[1].ID != "unknown-suffix" {
				t.Fatalf("dispatch calls = %+v, want prefix mutation followed by unresolved suffix", calls)
			}
			prefixEffects++
			cancelFirst()
			return []ToolOutcome{{
				Content:       "changed state.txt",
				Success:       true,
				EffectClass:   "modifying",
				StateObserved: true,
				StateChanged:  true,
			}}, context.Canceled
		}),
		History:     history,
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      taskID,
		TurnID:      turnID,
	}

	ctrl, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	first, err := ctrl.Run(firstCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run error = %v, want context canceled", err)
	}
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("first Run error = %v, want typed incomplete interruption", err)
	}
	if incomplete.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("first incomplete code = %q, want %q", incomplete.Code, IncompleteToolRoundInterrupted)
	}
	if first == nil {
		t.Fatalf("first result is nil")
	}
	if first.FinishReason != IncompleteToolRoundInterrupted || first.Termination.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("first termination = %+v finish=%q, want typed tool interruption", first.Termination, first.FinishReason)
	}
	if strings.Contains(first.Termination.Reason, "context canceled") {
		t.Fatalf("first public reason leaked raw cause: %q", first.Termination.Reason)
	}
	if prefixEffects != 1 || suffixEffects != 0 || dispatchAttempts != 1 {
		t.Fatalf("effects prefix=%d suffix=%d dispatches=%d, want 1, 0, 1", prefixEffects, suffixEffects, dispatchAttempts)
	}

	prefixStepID := StableStepID(runID, taskID, turnID, 1, "tool", 0)
	prefix, err := ledger.GetStep(ctx, runID, prefixStepID)
	if err != nil {
		t.Fatalf("GetStep prefix: %v", err)
	}
	if prefix.Status != runledger.StepCompleted || prefix.OutputEvidenceID == "" {
		t.Fatalf("prefix step = %+v, want completed durable result", prefix)
	}
	suffixStepID := StableStepID(runID, taskID, turnID, 1, "tool", 1)
	suffix, err := ledger.GetStep(ctx, runID, suffixStepID)
	if err != nil {
		t.Fatalf("GetStep suffix: %v", err)
	}
	if suffix.Status != runledger.StepBlocked || suffix.OutputEvidenceID != "" {
		t.Fatalf("suffix step = %+v, want blocked unresolved result", suffix)
	}

	if first.ToolCalls != 1 {
		t.Errorf("first result ToolCalls = %d, want confirmed prefix count 1", first.ToolCalls)
	}
	if len(first.ModelExecutions) != 1 || first.ModelExecutions[0] != (model.ExecutionIdentity{}) {
		t.Errorf("first model executions = %+v, want one observed response with unavailable identity", first.ModelExecutions)
	}
	if first.Progress.ToolCalls != 1 || first.Progress.SuccessfulToolCalls != 1 || first.Progress.StateChangedCalls != 1 {
		t.Errorf("first result progress = %+v, want prefix mutation surfaced", first.Progress)
	}
	if first.Progress.VerificationObservedCalls != 0 {
		t.Errorf("first result verification calls = %d, want none for unresolved suffix", first.Progress.VerificationObservedCalls)
	}
	if got := countHistoryToolMessages(history, "modify-prefix"); got != 1 {
		t.Errorf("history prefix tool messages = %d, want 1", got)
	}
	if got := countHistoryToolMessages(history, "unknown-suffix"); got != 0 {
		t.Errorf("history suffix confirmed tool messages = %d, want 0 unresolved suffix credit", got)
	}
	if got := countHistoryMessages(history, "unknown-suffix"); got != 1 {
		t.Errorf("history suffix diagnostic messages = %d, want 1 unresolved suffix placeholder", got)
	}

	second, err := ctrl.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("second Run error = %v, want latched context canceled interruption", err)
	}
	if !errors.As(err, &incomplete) || incomplete.Code != IncompleteToolRoundInterrupted {
		t.Errorf("second Run error = %v, want latched typed incomplete interruption", err)
	}
	if second == nil {
		t.Fatalf("second result is nil")
	}
	if second.FinishReason != IncompleteToolRoundInterrupted || second.Termination.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("second termination = %+v finish=%q, want restored typed tool interruption", second.Termination, second.FinishReason)
	}
	if second.CompletionStatus == CompletionConclusive {
		t.Errorf("second completion status = %s, want incomplete until prefix mutation is verified", second.CompletionStatus)
	}
	if len(second.ModelExecutions) != 1 || second.ModelExecutions[0] != (model.ExecutionIdentity{}) {
		t.Errorf("second model executions = %+v, want carried single observed response without duplicate", second.ModelExecutions)
	}
	if modelCalls != 1 {
		t.Errorf("model calls after same-controller continuation = %d, want 1", modelCalls)
	}
	if second.Progress.StateChangedCalls != 1 || second.Progress.VerificationObservedCalls != 0 {
		t.Errorf("second progress = %+v, want carried prefix mutation with verification debt", second.Progress)
	}
	if dispatchAttempts != 1 || prefixEffects != 1 || suffixEffects != 0 {
		t.Errorf("after continuation effects prefix=%d suffix=%d dispatches=%d, want no repeated or suffix side effects", prefixEffects, suffixEffects, dispatchAttempts)
	}

	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		t.Fatalf("durable replay should not call provider while suffix step is blocked")
		return nil, nil
	})
	config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
		t.Fatalf("durable replay should not dispatch while suffix step is blocked")
		return nil, nil
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController replay: %v", err)
	}
	replayed, err := replay.Run(ctx)
	var recovery *runledger.StepRecoveryError
	if !errors.As(err, &recovery) || recovery.Action != runledger.StepRecoveryRerun {
		t.Errorf("replay error = %v, want blocked suffix recovery rerun", err)
	}
	if replayed == nil {
		t.Fatalf("replay result is nil")
	}
	if replayed.Progress.ToolCalls != 1 || replayed.Progress.StateChangedCalls != 1 {
		t.Errorf("replay progress = %+v, want durable prefix evidence surfaced before blocked suffix", replayed.Progress)
	}
	if len(replayed.ModelExecutions) != 1 || replayed.ModelExecutions[0] != (model.ExecutionIdentity{}) {
		t.Errorf("replayed model executions = %+v, want durable observed response identity coverage", replayed.ModelExecutions)
	}
}

func TestController_PartialDispatchShortOutcomeSurfacesOnlyConfirmedPrefix(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls > 1 {
				t.Fatalf("unexpected continuation model call after short tool outcome")
			}
			return twoToolCallResponse("confirmed-prefix", "unknown-suffix"), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{
				Content:       "changed prefix",
				Success:       true,
				EffectClass:   "modifying",
				StateObserved: true,
				StateChanged:  true,
			}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "returned 1 outcomes for 2 calls") {
		t.Fatalf("Run error = %v, want short outcome error", err)
	}
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("Run error = %v, want typed incomplete interruption", err)
	}
	if result.ToolCalls != 1 || result.Progress.ToolCalls != 1 || result.Progress.StateChangedCalls != 1 {
		t.Fatalf("result=%+v, want only confirmed prefix progress", result)
	}
	if countHistoryToolMessages(history, "confirmed-prefix") != 1 {
		t.Fatalf("prefix history messages = %+v, want confirmed prefix result", history.messages)
	}
	if countHistoryToolMessages(history, "unknown-suffix") != 0 || countHistoryMessages(history, "unknown-suffix") != 1 {
		t.Fatalf("suffix history messages = %+v, want one unknown diagnostic and no confirmed credit", history.messages)
	}

	_, err = ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "returned 1 outcomes for 2 calls") {
		t.Fatalf("latched continuation error = %v, want original short outcome error", err)
	}
	if modelCalls != 1 || dispatchCalls != 1 {
		t.Fatalf("modelCalls=%d dispatchCalls=%d, want no continuation side effects", modelCalls, dispatchCalls)
	}
}

func TestController_ToolOutcomePersistenceFailureDoesNotFabricateProgress(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	history := &recordingHistory{}
	storeErr := errors.New("store tool result failed")
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return toolCallResponse("call-write", "edit_file", `{}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{
				Content:       "changed but not durably recorded",
				Success:       true,
				EffectClass:   "modifying",
				StateObserved: true,
				StateChanged:  true,
			}}, nil
		}),
		History:     history,
		RunLedger:   ledger,
		Evidence:    &toolResultFailingEvidenceStore{Store: ev, failure: storeErr},
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-storage-failure",
		TurnID:      "task-storage-failure/cp-001/turn-000",
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if !errors.Is(err, storeErr) {
		t.Fatalf("Run error = %v, want storage failure", err)
	}
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("Run error = %v, want typed incomplete interruption", err)
	}
	if result.ToolCalls != 0 || result.Progress.ToolCalls != 0 || result.Progress.StateChangedCalls != 0 {
		t.Fatalf("result=%+v, want no fabricated progress for unpersisted outcome", result)
	}
	if countHistoryToolMessages(history, "call-write") != 0 || countHistoryMessages(history, "call-write") != 1 {
		t.Fatalf("history messages = %+v, want only no-confirmed-result diagnostic", history.messages)
	}
}

func TestController_ObserverErrorRecordsAllKnownOutcomesAndLatches(t *testing.T) {
	history := &recordingHistory{}
	observerErr := errors.New("observer failed")
	modelCalls := 0
	observed := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls > 1 {
				t.Fatalf("unexpected continuation model call after observer failure")
			}
			return twoToolCallResponse("call-edit", "call-verify"), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{
				{Content: "changed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true},
				{Content: "verified", Success: true, EffectClass: "readonly", VerificationObserved: true, VerificationPassed: true},
			}, nil
		}),
		ObserveToolOutcome: func(_ context.Context, call model.ToolCall, _ ToolOutcome, _ bool) error {
			observed++
			if call.ID == "call-edit" {
				return observerErr
			}
			return nil
		},
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if !errors.Is(err, observerErr) {
		t.Fatalf("Run error = %v, want observer error", err)
	}
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("Run error = %v, want typed incomplete interruption", err)
	}
	if observed != 2 {
		t.Fatalf("observed outcomes = %d, want both known outcomes despite first observer error", observed)
	}
	if result.ToolCalls != 2 || result.Progress.ToolCalls != 2 || result.Progress.StateChangedCalls != 1 || result.Progress.VerificationPassedCalls != 1 {
		t.Fatalf("result=%+v, want all known outcomes recorded before latch", result)
	}
	if countHistoryMessages(history, "call-edit") != 1 || countHistoryMessages(history, "call-verify") != 1 {
		t.Fatalf("history messages = %+v, want both known tool results", history.messages)
	}

	_, err = ctrl.Run(context.Background())
	if !errors.Is(err, observerErr) {
		t.Fatalf("latched continuation error = %v, want observer error", err)
	}
	if modelCalls != 1 || observed != 2 || len(history.messages) != 3 {
		t.Fatalf("after latch modelCalls=%d observed=%d history=%d, want no duplicated model/tool/observer work", modelCalls, observed, len(history.messages))
	}
}

func TestController_ReplayLoadFailureKeepsEarlierKnownOutcomeVisible(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	const (
		taskID = "task-replay-load-failure"
		turnID = "task-replay-load-failure/cp-001/turn-000"
	)
	modelCalls := 0
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return twoToolCallResponse("call-first", "call-second"), nil
			}
			return textResponse("done", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{
				{Content: "first changed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true},
				{Content: "second verified", Success: true, EffectClass: "readonly", VerificationObserved: true, VerificationPassed: true},
			}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      taskID,
		TurnID:      turnID,
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	secondStepID := StableStepID(runID, taskID, turnID, 1, "tool", 1)
	secondStep, err := ledger.GetStep(ctx, runID, secondStepID)
	if err != nil {
		t.Fatalf("GetStep second: %v", err)
	}
	if secondStep.OutputEvidenceID == "" {
		t.Fatalf("second step has no output evidence: %+v", secondStep)
	}
	loadErr := errors.New("tool replay evidence unavailable")
	replayHistory := &recordingHistory{}
	config.History = replayHistory
	config.Evidence = &getFailingEvidenceStore{Store: ev, failID: secondStep.OutputEvidenceID, failure: loadErr}
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		t.Fatalf("durable replay should not call provider")
		return nil, nil
	})
	config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
		t.Fatalf("durable replay should not dispatch tools")
		return nil, nil
	})
	replay, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController replay: %v", err)
	}

	result, err := replay.Run(ctx)
	if !errors.Is(err, loadErr) {
		t.Fatalf("replay Run error = %v, want evidence load failure", err)
	}
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) || incomplete.Code != IncompleteToolRoundInterrupted {
		t.Fatalf("replay Run error = %v, want typed incomplete interruption", err)
	}
	if result.ToolCalls != 1 || result.Progress.ToolCalls != 1 || result.Progress.StateChangedCalls != 1 || result.Progress.VerificationObservedCalls != 0 {
		t.Fatalf("replay result=%+v, want first replayed outcome only", result)
	}
	if countHistoryToolMessages(replayHistory, "call-first") != 1 {
		t.Fatalf("replay history = %+v, want first confirmed tool result", replayHistory.messages)
	}
	if countHistoryToolMessages(replayHistory, "call-second") != 0 || countHistoryMessages(replayHistory, "call-second") != 1 {
		t.Fatalf("replay history = %+v, want second unresolved diagnostic without credit", replayHistory.messages)
	}
}

func twoToolCallResponse(firstID, secondID string) *model.ChatResponse {
	return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
		Role: "assistant",
		ToolCalls: []model.ToolCall{
			{ID: firstID, Type: "function", Function: model.FunctionCall{Name: "edit_file", Arguments: `{}`}},
			{ID: secondID, Type: "function", Function: model.FunctionCall{Name: "run_tests", Arguments: `{}`}},
		},
	}}}}
}

func countHistoryToolMessages(history *recordingHistory, callID string) int {
	if history == nil {
		return 0
	}
	count := 0
	for _, msg := range history.messages {
		content := strings.TrimSpace(model.ExtractTextContentOrEmpty(msg.Content))
		if msg.Role == "tool" && msg.ToolCallID == callID && content != "" && !strings.Contains(content, "Outcome unknown") && !strings.Contains(content, "No confirmed result") {
			count++
		}
	}
	return count
}

func countHistoryMessages(history *recordingHistory, callID string) int {
	if history == nil {
		return 0
	}
	count := 0
	for _, msg := range history.messages {
		if msg.Role == "tool" && msg.ToolCallID == callID {
			count++
		}
	}
	return count
}

type toolResultFailingEvidenceStore struct {
	evidence.Store
	failure error
}

func (s *toolResultFailingEvidenceStore) Put(ctx context.Context, object evidence.Object) (evidence.Object, error) {
	if object.Kind == evidence.KindToolResult {
		return evidence.Object{}, s.failure
	}
	return s.Store.Put(ctx, object)
}

type getFailingEvidenceStore struct {
	evidence.Store
	failID  string
	failure error
}

func (s *getFailingEvidenceStore) Get(ctx context.Context, id string) (evidence.Object, error) {
	if id == s.failID {
		return evidence.Object{}, s.failure
	}
	return s.Store.Get(ctx, id)
}
