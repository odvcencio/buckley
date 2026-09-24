package agentloop

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_PersistentMutation(t *testing.T) {
	for _, scenario := range []struct {
		name              string
		budget            int
		responses         []string
		wantCode          string
		wantContinuations int
	}{
		{"verified", 3, []string{"edit", "Next step: run tests", "Next step: check build", "verify", "Done"}, "", 2},
		{"blocked", 3, []string{`{"status":"BLOCKED","kind":"credentials","reason":"registry login required","required_input":"registry token"}`}, "blocked", 0},
		{"budget", 2, []string{"Next step: edit", "Next step: edit", "Next step: edit"}, "continuation_limit", 2},
		{"invalid blocker", 1, []string{`{"status":"BLOCKED","kind":"failed_test","reason":"tests failed","required_input":"fix them"}`, "Not done"}, "continuation_limit", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			history := &recordingHistory{}
			requests, continuations := 0, 0
			controller, err := NewController(ControllerConfig{
				CompletionContract: &CompletionContract{TaskIntent: MutationIntent, RequireObservableChange: true, RequirePostChangeVerification: true, MaxContinuations: scenario.budget,
					OnContinuation: func(n int, reason string) {
						continuations++
						if n != continuations || reason == "" {
							t.Errorf("continuation=%d reason=%q", n, reason)
						}
					}},
				History: history,
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test", Messages: history.messages}), nil
				},
				CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
					if requests >= len(scenario.responses) {
						t.Fatal("unexpected model request")
					}
					response := scenario.responses[requests]
					requests++
					if response == "edit" || response == "verify" {
						return toolCallResponse(response, "test_tool", `{"step":"`+response+`"}`, model.Usage{}), nil
					}
					return textResponse(response, model.Usage{}), nil
				}),
				DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
					edit := calls[0].ID == "edit"
					return []ToolOutcome{{Success: true, StateObserved: true, StateChanged: edit, VerificationObserved: !edit, VerificationPassed: !edit}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.Run(context.Background())
			if (err != nil) != (scenario.wantCode != "") || result.Termination.Code != scenario.wantCode || continuations != scenario.wantContinuations || requests != len(scenario.responses) {
				t.Fatalf("result=%+v err=%v continuations=%d requests=%d", result, err, continuations, requests)
			}
			if scenario.name == "verified" {
				found := false
				for _, msg := range history.messages {
					text := model.ExtractTextContentOrEmpty(msg.Content)
					if msg.Role == "user" && strings.Contains(text, "Next step: run tests") && strings.Contains(text, "missing successful verification") {
						found = true
					}
				}
				if !found || result.Progress.VerificationPassedCalls != 1 {
					t.Fatalf("missing continuation context or verification: %+v", result)
				}
			}
		})
	}
}

func TestCompletionContract_ObservationFailureCanRecover(t *testing.T) {
	contract := CompletionContract{TaskIntent: MutationIntent, RequireObservableChange: true, RequirePostChangeVerification: true, TolerateObservationErrors: true}
	snapshot := ProgressSnapshot{StateChangedCalls: 1, LastStateChangeSequence: 1, StateObservationFailures: 1, LastStateFailureSequence: 3, LastVerificationSequence: 2, LastVerificationPassed: true}
	if contract.Validate(snapshot) == nil {
		t.Fatal("observation error must invalidate older verification")
	}
	snapshot.LastVerificationSequence = 4
	if err := contract.Validate(snapshot); err != nil {
		t.Fatalf("later verification must recover: %v", err)
	}
}

func TestController_PersistenceRetainsRequestFuse(t *testing.T) {
	requests := 0
	controller, err := NewController(ControllerConfig{
		MaxModelRequests:   2,
		CompletionContract: &CompletionContract{TaskIntent: MutationIntent, RequireObservableChange: true, MaxContinuations: 200},
		History:            &recordingHistory{},
		BuildRequest:       func(context.Context, int) (model.ChatRequest, error) { return model.ChatRequest{Model: "test"}, nil },
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			requests++
			return textResponse("Next step: edit", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(context.Background())
	if err == nil || requests != 2 || result.Termination.Kind != "emergency_fuse" {
		t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
	}
}

func TestGovernor_ErrorKindsAndReadRanges(t *testing.T) {
	config := DefaultConfig()
	config.ExactRepeatLimit = 20
	config.CycleRepeats = 20
	config.OutcomeRepeatLimit = 3
	g := New(config)
	for _, args := range []string{`{"path":"large","start_line":1}`, `{"path":"large","start_line":101}`, `{"path":"large","start_line":201}`} {
		if d := g.Observe("read_file", args, "[line_too_large] use bounded bytes", false); d.Stop {
			t.Fatalf("distinct ranges stopped: %+v", d)
		}
	}
	args := `{"path":"large","start_line":1}`
	if d := g.Observe("read_file", args, "[binary_file] use a hex dump", false); d.Stop {
		t.Fatalf("distinct error stopped: %+v", d)
	}
	g.Observe("read_file", args, "[line_too_large] changed detail", false)
	if d := g.Observe("read_file", args, "[line_too_large] another detail", false); !d.Stop || d.Kind != "outcome_repeat" {
		t.Fatalf("same kind and args did not stop: %+v", d)
	}
}
