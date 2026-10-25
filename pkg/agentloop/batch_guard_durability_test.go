package agentloop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestController_BatchedGuardParallelOptIn(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(map[bool]string{false: "sequential", true: "parallel"}[parallel], func(t *testing.T) {
			executed := 0
			history := &recordingHistory{}
			cfg := batchGuardConfig(history, &parallel, &executed)
			controller, err := NewController(cfg)
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.Run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if parallel {
				want = 5
			}
			if executed != want || result.ToolCalls != want || result.Progress.ToolCalls != want {
				t.Fatalf("executed=%d result=%+v; want %d actual calls", executed, result, want)
			}
			tools, skipped := 0, 0
			for _, message := range history.messages {
				if message.Role != "tool" {
					continue
				}
				tools++
				if strings.HasPrefix(model.ExtractTextContentOrEmpty(message.Content), "Not executed:") {
					skipped++
				}
			}
			if tools != 5 || skipped != 5-want {
				t.Fatalf("tool responses=%d skipped=%d; want full protocol-valid transcript", tools, skipped)
			}
		})
	}
}

func TestController_BatchedStopDurableReplay(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new sequential", true: "legacy eager batch"}[legacy], func(t *testing.T) {
			ledger, ev, runID := newDurableControllerStores(t)
			history := &recordingHistory{}
			executed := 0
			cfg := batchGuardConfig(history, nil, &executed)
			cfg.RunLedger, cfg.StepJournal, cfg.Evidence = ledger, ledger, ev
			cfg.RunID, cfg.TaskID, cfg.TurnID = runID, "task-batch", "turn-batch"
			controller, err := NewController(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var first *Result
			want := 3
			if legacy {
				// Record the eager dispatch used by older releases, even though
				// the request never opted into parallel calls.
				want = 5
				first = &Result{Rounds: 1}
				response, _, err := controller.executeModelRound(t.Context(), first, 1)
				if err != nil {
					t.Fatal(err)
				}
				history.Append(response.response.Choices[0].Message)
				state := newToolRoundState(response.response.Choices[0].Message.ToolCalls)
				if err := controller.prepareToolRound(t.Context(), state, 1); err != nil {
					t.Fatal(err)
				}
				if err := controller.dispatchToolRound(t.Context(), state, 1); err != nil {
					t.Fatal(err)
				}
				var progress progressTracker
				stop, err := controller.observeToolRound(t.Context(), state, &progress)
				if err != nil {
					t.Fatal(err)
				}
				first.ToolCalls, first.Progress = state.knownOutcomeCount(), progress.Snapshot()
				first.GuardDecision, first.FinishReason = stop, FinishReasonLoopGuard
				first.Termination = Termination{Kind: stop.Kind, Reason: stop.Reason}
				first, err = controller.finalizeStoppedTurn(t.Context(), first, 1)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				first, err = controller.Run(t.Context())
				if err != nil {
					t.Fatal(err)
				}
			}
			for i := range 5 {
				step, err := ledger.GetStep(t.Context(), runID, StableStepID(runID, cfg.TaskID, cfg.TurnID, 1, "tool", i))
				if i < want {
					if err != nil || step.Status != runledger.StepCompleted || step.OutputEvidenceID == "" {
						t.Fatalf("step %d=%+v err=%v", i, step, err)
					}
				} else if !errors.Is(err, runledger.ErrStepNotFound) {
					t.Fatalf("unstarted step %d was claimed: %+v err=%v", i, step, err)
				}
			}
			replayHistory := &recordingHistory{}
			cfg.History = replayHistory
			cfg.BuildRequest = batchGuardConfig(replayHistory, nil, &executed).BuildRequest
			cfg.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
				t.Fatal("replay called provider")
				return nil, nil
			})
			cfg.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
				t.Fatal("replay executed tools")
				return nil, nil
			})
			replay, err := NewController(cfg)
			if err != nil {
				t.Fatal(err)
			}
			second, err := replay.Run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if executed != want || second.ToolCalls != first.ToolCalls || second.Progress != first.Progress || second.Content != first.Content || !reflect.DeepEqual(history.messages, replayHistory.messages) {
				t.Fatalf("replay changed durable results: first=%+v second=%+v historyEqual=%v", first, second, reflect.DeepEqual(history.messages, replayHistory.messages))
			}
		})
	}
}

func TestController_BatchedProductiveCallsKeepStableDispatchIdentity(t *testing.T) {
	history := &recordingHistory{}
	executed := 0
	cfg := batchGuardConfig(history, nil, &executed)
	cfg.RunID, cfg.TaskID, cfg.TurnID = "run", "task", "turn"
	requests := 0
	caller := cfg.CallModel
	cfg.CallModel = ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, stream bool) (*model.ChatResponse, error) {
		requests++
		if requests > 1 {
			return textResponse("done", model.Usage{}), nil
		}
		return caller.Call(ctx, req, stream)
	})
	cfg.DispatchTools = ContextualToolDispatcherFunc(func(_ context.Context, calls []ToolDispatchCall) ([]ToolOutcome, error) {
		if len(calls) != 1 {
			t.Fatalf("default dispatch batch size=%d", len(calls))
		}
		call := calls[0]
		stepID := StableStepID(cfg.RunID, cfg.TaskID, cfg.TurnID, 1, "tool", executed)
		if call.ToolIndex != executed || call.StepID != stepID || call.ApprovalID != StableApprovalID(cfg.RunID, cfg.TaskID, cfg.TurnID, stepID, 1, executed) {
			t.Fatalf("unstable dispatch identity at %d: %+v", executed, call)
		}
		executed++
		return []ToolOutcome{{Content: "advanced", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
	})
	controller, err := NewController(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(t.Context())
	if err != nil || executed != 5 || result.Progress.StateChangedCalls != 5 || result.Termination.Kind != "" {
		t.Fatalf("productive batch: executed=%d result=%+v err=%v", executed, result, err)
	}
}

func TestController_BatchedCancellationPreservesCompletedPrefixOnResume(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	history := &recordingHistory{}
	executed := 0
	cfg := batchGuardConfig(history, nil, &executed)
	cfg.RunLedger, cfg.StepJournal, cfg.Evidence = ledger, ledger, ev
	cfg.RunID, cfg.TaskID, cfg.TurnID = runID, "task-cancel", "turn-cancel"
	cfg.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
		executed++
		cancel()
		return []ToolOutcome{{Content: "prefix committed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
	})
	controller, err := NewController(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := controller.Run(ctx)
	if !errors.Is(err, context.Canceled) || executed != 1 || first.ToolCalls != 1 || first.Progress.StateChangedCalls != 1 {
		t.Fatalf("cancelled prefix: executed=%d result=%+v err=%v", executed, first, err)
	}
	replayHistory := &recordingHistory{}
	cfg.History = replayHistory
	cfg.BuildRequest = batchGuardConfig(replayHistory, nil, &executed).BuildRequest
	cfg.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		return textResponse("done", model.Usage{}), nil
	})
	cfg.DispatchTools = ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
		if len(calls) != 1 || calls[0].ID == "unavailable-0" {
			t.Fatalf("resume repeated completed prefix: %+v", calls)
		}
		executed++
		return []ToolOutcome{{Content: "suffix committed", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
	})
	resume, err := NewController(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resume.Run(t.Context())
	if err != nil || executed != 5 || result.ToolCalls != 5 || result.Progress.StateChangedCalls != 5 {
		t.Fatalf("resume: executed=%d result=%+v err=%v", executed, result, err)
	}
}
