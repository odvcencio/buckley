package agentloop

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_ToolBudgetEvidenceStaysQuiet(t *testing.T) {
	history := &recordingHistory{}
	ctrl := &Controller{cfg: ControllerConfig{
		Governor: New(Config{MaxToolCalls: 10}), History: history,
		CompletionContract: &CompletionContract{TaskIntent: MutationIntent, RequireObservableChange: true},
	}}
	state := newToolRoundState([]model.ToolCall{{ID: "read", Function: model.FunctionCall{Name: "read_file", Arguments: `{}`}}})
	state.outcomes[0] = ToolOutcome{Content: "source evidence", Success: true}
	state.known[0] = true
	var progress progressTracker
	if _, err := ctrl.observeToolRound(context.Background(), state, &progress); err != nil {
		t.Fatal(err)
	}
	if len(history.messages) != 1 || history.messages[0].Content != "source evidence" {
		t.Fatalf("ample budget should preserve unadorned evidence: %+v", history.messages)
	}
}

func TestController_ToolBudgetCompletionEvidence(t *testing.T) {
	read := ToolOutcome{Content: "source evidence", Success: true, StateObserved: true}
	edit := ToolOutcome{Content: "edited evidence", Success: true, StateObserved: true, StateChanged: true}
	pass := ToolOutcome{Content: "test evidence", Success: true, VerificationObserved: true, VerificationPassed: true}
	fail := ToolOutcome{Content: "failed test evidence", VerificationObserved: true}
	mutation := &CompletionContract{TaskIntent: MutationIntent, RequireObservableChange: true, RequirePostChangeVerification: true}
	for _, tc := range []struct {
		name     string
		outcomes []ToolOutcome
		contract *CompletionContract
		want     string
	}{
		{"read before edit", []ToolOutcome{read}, mutation, "no mutations were recorded"},
		{"edit needs check", []ToolOutcome{edit}, mutation, "missing successful verification"},
		{"failed check", []ToolOutcome{edit, fail}, mutation, "latest verification after the final workspace change did not pass"},
		{"stale check", []ToolOutcome{pass, edit}, mutation, "missing successful verification"},
		{"check also changes state", []ToolOutcome{{Content: "check modified workspace", Success: true, StateObserved: true, StateChanged: true, VerificationObserved: true, VerificationPassed: true}}, mutation, "missing successful verification"},
		{"passed check", []ToolOutcome{edit, pass}, mutation, ""},
		{"read after check", []ToolOutcome{edit, pass, read}, mutation, ""},
		{"later failure", []ToolOutcome{edit, pass, fail}, mutation, "latest verification after the final workspace change did not pass"},
		{"observation failed", []ToolOutcome{{Content: "tool evidence", StateObservationFailed: true}}, mutation, "workspace state could not be observed"},
		{"readonly", []ToolOutcome{read}, &CompletionContract{TaskIntent: ReadOnlyIntent}, ""},
		{"no contract", []ToolOutcome{edit}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := &recordingHistory{}
			ctrl := &Controller{cfg: ControllerConfig{
				Governor: New(Config{MaxToolCalls: len(tc.outcomes) + 1, ExactRepeatLimit: 100, OutcomeRepeatLimit: 100}),
				History:  history, CompletionContract: tc.contract,
			}}
			calls := make([]model.ToolCall, len(tc.outcomes))
			for i := range calls {
				calls[i] = model.ToolCall{ID: fmt.Sprintf("call-%d", i), Function: model.FunctionCall{Name: "operation", Arguments: fmt.Sprintf(`{"i":%d}`, i)}}
			}
			state := newToolRoundState(calls)
			state.outcomes = tc.outcomes
			for i := range state.known {
				state.known[i] = true
			}
			var progress progressTracker
			decision, err := ctrl.observeToolRound(context.Background(), state, &progress)
			if err != nil || decision.Stop {
				t.Fatalf("observe: %+v %v", decision, err)
			}
			if ctrl.cfg.Governor.RemainingToolCalls() != 1 {
				t.Fatal("notice changed budget")
			}
			for i, message := range history.messages {
				content := message.Content.(string)
				if !strings.HasPrefix(content, tc.outcomes[i].Content) {
					t.Fatal("lost tool evidence")
				}
				if i != len(calls)-1 {
					if strings.Contains(content, "Completion evidence:") {
						t.Fatal("notice before final batch outcome")
					}
					continue
				}
				if !strings.Contains(content, "Harness budget: 1 tool calls remain") {
					t.Fatalf("missing budget: %s", content)
				}
				if tc.want == "" {
					if strings.Contains(content, "Completion evidence:") {
						t.Fatalf("invented debt: %s", content)
					}
				} else if !strings.Contains(content, "Completion evidence:") || !strings.Contains(content, tc.want) {
					t.Fatalf("missing current debt %q: %s", tc.want, content)
				}
			}
		})
	}
}
