package agentloop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestGovernor_ObservedChangeResetsStagnation(t *testing.T) {
	for _, maxReadOnlyCalls := range []int{0, 10} {
		t.Run(fmt.Sprintf("read_only_limit_%d", maxReadOnlyCalls), func(t *testing.T) {
			governor := New(Config{MaxReadOnlyCalls: maxReadOnlyCalls})
			for range 2 {
				governor.BeginRound()
				governor.Observe("run_tests", `{}`, "PASS", true)
			}
			governor.ObserveProgress("modifying", true, true, true)
			if got := governor.RepetitionPressure(); got != 0 {
				t.Fatalf("repetition after confirmed change = %v, want 0", got)
			}
			if novelty, observed := governor.EvidenceNovelty(); novelty != 0 || observed {
				t.Fatalf("novelty after confirmed change = (%v, %v), want no samples", novelty, observed)
			}
			if governor.ToolCalls() != 2 || governor.Rounds() != 2 {
				t.Fatalf("confirmed change reset hard limits: tools=%d rounds=%d", governor.ToolCalls(), governor.Rounds())
			}
			for attempt := 1; attempt <= 3; attempt++ {
				decision := governor.Observe("run_tests", `{}`, "PASS", true)
				if attempt == 1 && (decision.Stop || decision.Nudge != "") {
					t.Fatalf("first check after change = %+v, want fresh evidence", decision)
				}
				if attempt == 2 && (decision.Stop || decision.Nudge == "") {
					t.Fatalf("repeated check without change = %+v, want warning", decision)
				}
				if attempt == 3 && (!decision.Stop || decision.Kind != "exact_repeat") {
					t.Fatalf("third check without change = %+v, want stagnation stop", decision)
				}
			}
		})
	}
}

func TestGovernor_UnconfirmedChangePreservesStagnation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		success       bool
		stateObserved bool
		stateChanged  bool
	}{
		{"no-op", true, true, false},
		{"effect label only", true, false, false},
		{"unobserved change", true, false, true},
		{"failed action", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			governor := New(DefaultConfig())
			for range 2 {
				governor.Observe("run_tests", `{}`, "PASS", true)
			}
			governor.ObserveProgress("modifying", tc.success, tc.stateObserved, tc.stateChanged)
			if got := governor.Observe("run_tests", `{}`, "PASS", true); !got.Stop || got.Kind != "exact_repeat" {
				t.Fatalf("unconfirmed progress bypassed stagnation: %+v", got)
			}
		})
	}
}

func TestGovernor_ObservedChangeRestartsEvidenceWindow(t *testing.T) {
	governor := New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, CycleRepeats: 100})
	for range 8 {
		governor.Observe("run_tests", `{}`, "same failure", false)
	}
	governor.ObserveProgress("modifying", true, true, true)
	for i := range 4 {
		governor.Observe("read_file", fmt.Sprintf(`{"path":"file%d.go"}`, i), "evidence", true)
		novelty, observed := governor.EvidenceNovelty()
		if novelty != 1 || observed != (i == 3) {
			t.Fatalf("sample %d after change: novelty=(%v, %v), want fresh sampling window", i+1, novelty, observed)
		}
	}
}

func TestGovernor_ObservedChangeBreaksPriorActionCycle(t *testing.T) {
	governor := New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100})
	for i := range 4 {
		governor.Observe("read_file", fmt.Sprintf(`{"path":"file%d.go"}`, i%2), "unchanged", true)
	}
	governor.ObserveProgress("modifying", true, true, true)
	for i := range 6 {
		decision := governor.Observe("read_file", fmt.Sprintf(`{"path":"file%d.go"}`, i%2), "unchanged", true)
		if i < 5 && decision.Stop {
			t.Fatalf("prior workspace's evidence caused a cycle stop: %+v", decision)
		}
		if i == 5 && (!decision.Stop || decision.Kind != "action_cycle") {
			t.Fatalf("new stalled cycle = %+v, want action_cycle stop", decision)
		}
	}
}

func TestController_RepeatedMutationResultsRespectProgressAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name       string
		governor   Config
		changed    func(int) bool
		wantCalls  int
		wantReason string
	}{
		{"tool ceiling", Config{MaxToolCalls: 4}, func(int) bool { return true }, 4, "tool_call_limit"},
		{"round ceiling", Config{MaxRounds: 4}, func(int) bool { return true }, 4, "round_limit"},
		{"change before repeat limit", DefaultConfig(), func(call int) bool { return call == 3 }, 5, "exact_repeat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			controller, err := NewController(ControllerConfig{
				Governor: New(tc.governor),
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					return toolCallResponse("call", "run_shell", `{"command":"generate"}`, model.Usage{}), nil
				}),
				DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					calls++
					return []ToolOutcome{{Content: "done", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: tc.changed(calls)}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.Run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if calls != tc.wantCalls || result.ToolCalls != tc.wantCalls || result.Termination.Kind != tc.wantReason {
				t.Fatalf("calls=%d result=%+v, want %d calls then %s", calls, result, tc.wantCalls, tc.wantReason)
			}
			if result.CompletionStatus != CompletionIncomplete {
				t.Fatalf("limit stop accepted as complete: %+v", result)
			}
		})
	}
}

func TestController_EditVerificationCyclesRemainProductive(t *testing.T) {
	ledger, evidence, runID := newDurableControllerStores(t)
	providerCalls, dispatchCalls := 0, 0
	history := &recordingHistory{}
	config := ControllerConfig{
		CompletionContract: &CompletionContract{
			TaskIntent: MutationIntent, RequireObservableChange: true, RequirePostChangeVerification: true,
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			providerCalls++
			for _, msg := range req.Messages {
				if strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), "this exact action produced the same result") {
					return nil, fmt.Errorf("productive model received a false stagnation warning")
				}
			}
			if providerCalls > 8 {
				return textResponse("Completed four edits and verified the final workspace.", model.Usage{TotalTokens: 1}), nil
			}
			name, args := "run_tests", `{}`
			if providerCalls%2 == 1 {
				name, args = "edit_file", fmt.Sprintf(`{"change":%d}`, providerCalls)
			}
			return toolCallResponse(fmt.Sprintf("call-%d", providerCalls), name, args, model.Usage{TotalTokens: 1}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			if calls[0].Function.Name == "edit_file" {
				return []ToolOutcome{{Content: "updated", Success: true, EffectClass: "modifying", StateObserved: true, StateChanged: true}}, nil
			}
			return []ToolOutcome{{Content: "PASS", Success: true, EffectClass: "readonly", StateObserved: true, VerificationObserved: true, VerificationPassed: true}}, nil
		}),
		History: history, RunLedger: ledger, Evidence: evidence, StepJournal: ledger,
		RunID: runID, SessionID: "durable-test", TaskID: "edit-test", TurnID: "edit-test/turn-1",
	}
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay_%t", replay), func(t *testing.T) {
			if replay {
				history.messages = nil
				config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					t.Fatal("replay called the provider")
					return nil, nil
				})
				config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					t.Fatal("replay repeated tool effects")
					return nil, nil
				})
			}
			controller, err := NewController(config)
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.Run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := result.RequireConclusive(); err != nil {
				t.Fatalf("productive edit/check cycle stopped: %v", err)
			}
			if result.ToolCalls != 8 || result.Progress.StateChangedCalls != 4 || result.Progress.VerificationPassedCalls != 4 {
				t.Fatalf("lost productive work: %+v", result)
			}
			if result.ModelRequests != 9 || result.Usage.TotalTokens != 9 || providerCalls != 9 || dispatchCalls != 8 {
				t.Fatalf("accounting or replay changed: requests=%d usage=%+v provider=%d tools=%d", result.ModelRequests, result.Usage, providerCalls, dispatchCalls)
			}
		})
	}
}
