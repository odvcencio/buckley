package agentloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

// stallStep is one scripted model response. A step with a tool name calls that
// tool; the name also selects the outcome the fake dispatcher reports.
type stallStep struct {
	tool string
	text string
}

const (
	stepEdit        = "edit"
	stepPass        = "verify_pass"
	stepFail        = "verify_fail"
	stepFailEdit    = "verify_fail_and_change"
	stepUnavailable = "verify_unavailable"
)

func stallOutcome(kind string) ToolOutcome {
	switch kind {
	case stepEdit:
		return ToolOutcome{Success: true, StateObserved: true, StateChanged: true}
	case stepPass:
		return ToolOutcome{Success: true, StateObserved: true, VerificationObserved: true, VerificationPassed: true}
	case stepFail:
		return ToolOutcome{Success: false, StateObserved: true, VerificationObserved: true}
	case stepFailEdit:
		return ToolOutcome{Success: false, StateObserved: true, StateChanged: true, VerificationObserved: true}
	case stepUnavailable:
		return ToolOutcome{Success: false, VerificationUnavailable: true, VerificationUnavailableReason: "test is not an accepted check"}
	}
	return ToolOutcome{Success: true}
}

type stallRun struct {
	result        *Result
	err           error
	requests      int
	continuations int
	stops         []Termination
	history       *recordingHistory
}

func runStallScript(t *testing.T, contract CompletionContract, script []stallStep) stallRun {
	t.Helper()
	run := stallRun{history: &recordingHistory{}}
	contract.TaskIntent = MutationIntent
	contract.RequireObservableChange = true
	contract.RequirePostChangeVerification = true
	if contract.MaxContinuations == 0 {
		contract.MaxContinuations = 200
	}
	contract.OnContinuation = func(int, string) { run.continuations++ }
	controller, err := NewController(ControllerConfig{
		CompletionContract: &contract,
		History:            run.history,
		OnStop:             func(termination Termination) { run.stops = append(run.stops, termination) },
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test", Messages: run.history.messages}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			if run.requests >= len(script) {
				t.Fatalf("model asked for request %d but the script has %d steps", run.requests+1, len(script))
			}
			step := script[run.requests]
			run.requests++
			if step.tool == "" {
				return textResponse(step.text, model.Usage{}), nil
			}
			return toolCallResponse(fmt.Sprintf("%s-%d", step.tool, run.requests), "test_tool", fmt.Sprintf(`{"n":%d}`, run.requests), model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			kind, _, _ := strings.Cut(calls[0].ID, "-")
			return []ToolOutcome{stallOutcome(kind)}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run.result, run.err = controller.Run(context.Background())
	return run
}

func say(text string) stallStep { return stallStep{text: text} }

func script(parts ...any) []stallStep {
	var steps []stallStep
	for _, part := range parts {
		switch value := part.(type) {
		case string:
			steps = append(steps, stallStep{tool: value})
		case stallStep:
			steps = append(steps, value)
		case []stallStep:
			steps = append(steps, value...)
		}
	}
	return steps
}

func TestProgressTracker_UnavailableNeverReplacesTheLastVerification(t *testing.T) {
	var tracker progressTracker
	tracker.Observe("edit", stallOutcome(stepEdit))
	tracker.Observe("verify", stallOutcome(stepPass))
	tracker.Observe("verify", stallOutcome(stepUnavailable))
	tracker.Observe("verify", stallOutcome(stepUnavailable))
	snapshot := tracker.Snapshot()
	if !snapshot.LastVerificationPassed || snapshot.LastVerificationSequence != 2 || snapshot.VerificationObservedCalls != 1 {
		t.Fatalf("unavailable calls replaced the last real verification: %+v", snapshot)
	}
	if snapshot.VerificationUnavailableCalls != 2 || snapshot.VerificationUnavailableStreak != 2 {
		t.Fatalf("unavailable counters = %+v", snapshot)
	}
	tracker.Observe("verify", stallOutcome(stepPass))
	if got := tracker.Snapshot().VerificationUnavailableStreak; got != 0 {
		t.Fatalf("a check that ran must reset the unavailable streak, got %d", got)
	}
	if got := tracker.Snapshot().VerificationUnavailableCalls; got != 2 {
		t.Fatalf("the total must keep counting, got %d", got)
	}
}

func TestProgressTracker_FailureStreakCountsOnlyRepeatsWithNoChange(t *testing.T) {
	var tracker progressTracker
	streak := func() int { return tracker.Snapshot().VerificationFailureStreak }

	tracker.Observe("edit", stallOutcome(stepEdit))
	tracker.Observe("verify", stallOutcome(stepFail))
	if streak() != 1 {
		t.Fatalf("first failure streak = %d, want 1", streak())
	}
	tracker.Observe("verify", stallOutcome(stepFail))
	tracker.Observe("verify", stallOutcome(stepFail))
	if streak() != 3 {
		t.Fatalf("three failures with no change streak = %d, want 3", streak())
	}
	tracker.Observe("edit", stallOutcome(stepEdit))
	tracker.Observe("verify", stallOutcome(stepFail))
	if streak() != 1 {
		t.Fatalf("a change between failures must restart the streak, got %d", streak())
	}
	tracker.Observe("verify", stallOutcome(stepFailEdit))
	if streak() != 1 {
		t.Fatalf("a failure that itself changed the workspace restarts the streak, got %d", streak())
	}
	tracker.Observe("verify", stallOutcome(stepFail))
	tracker.Observe("verify", stallOutcome(stepPass))
	if streak() != 0 {
		t.Fatalf("a pass must clear the streak, got %d", streak())
	}
}

func TestController_NoVerificationSurfaceAsksOnceThenCompletesUnverified(t *testing.T) {
	run := runStallScript(t, CompletionContract{
		VerificationSurface: func() (bool, string) { return false, "no go.mod, package.json, or test file was found" },
	}, script(stepEdit, say("Updated."), say("Done.")))
	if run.err != nil {
		t.Fatalf("err = %v, want a conclusive result", run.err)
	}
	result := run.result
	if result.CompletionStatus != CompletionConclusive || result.Termination.Kind != TerminationCompletedUnverified || result.Termination.Code != TerminationCompletedUnverified {
		t.Fatalf("result = %+v, want conclusive completed_unverified", result)
	}
	if !strings.Contains(result.Termination.Reason, "no check applies to this workspace") || !strings.Contains(result.Termination.Reason, "no go.mod") {
		t.Fatalf("reason does not say why: %q", result.Termination.Reason)
	}
	if result.Content != "Done." || result.Message.Content != "Done." {
		t.Fatalf("the model's own confirmed answer must be kept: %+v", result.Message)
	}
	if run.requests != 3 || run.continuations != 1 {
		t.Fatalf("requests=%d continuations=%d, want one confirmation question and then the end", run.requests, run.continuations)
	}
	var question string
	for _, message := range run.history.messages {
		if text := model.ExtractTextContentOrEmpty(message.Content); message.Role == "user" && strings.Contains(text, "nothing that can verify your change") {
			question = text
		}
	}
	for _, want := range []string{"no check applies to this workspace", "Updated.", "If your work is finished", "say that the change is unverified", "If work remains, continue it now", "Do not create scratch test files"} {
		if !strings.Contains(question, want) {
			t.Errorf("confirmation question omits %q:\n%s", want, question)
		}
	}
	if len(run.stops) != 1 || run.stops[0].Kind != TerminationCompletedUnverified {
		t.Fatalf("OnStop = %+v, want one completed_unverified stop", run.stops)
	}
	if got := result.Termination.StopReason(); !strings.HasPrefix(got, "completed_unverified: no check applies") {
		t.Fatalf("StopReason() = %q, want the kind followed by the reason", got)
	}
}

// A model that only narrated a next step must get to keep working: the
// confirmation question is the last chance, not a stop.
func TestController_NoSurfaceQuestionLetsAnUnfinishedRunContinue(t *testing.T) {
	run := runStallScript(t, CompletionContract{
		VerificationSurface: func() (bool, string) { return false, "no build files" },
	}, script(stepEdit, say("Next step: write the second file."), stepEdit, say("Both files are written.")))
	if run.err != nil || run.result.Termination.Kind != TerminationCompletedUnverified {
		t.Fatalf("err=%v result=%+v", run.err, run.result)
	}
	if run.result.Content != "Both files are written." || run.requests != 4 || run.continuations != 1 {
		t.Fatalf("content=%q requests=%d continuations=%d, want the work to continue after the question", run.result.Content, run.requests, run.continuations)
	}
}

func TestController_PassingVerificationNeverConsultsTheSurface(t *testing.T) {
	consulted := false
	run := runStallScript(t, CompletionContract{
		VerificationSurface: func() (bool, string) { consulted = true; return false, "none" },
	}, script(stepEdit, stepPass, say("Done and verified.")))
	if run.err != nil || run.result.Termination.Kind != "" || consulted {
		t.Fatalf("err=%v termination=%+v consulted=%v, want a plain conclusive finish", run.err, run.result.Termination, consulted)
	}
}

func TestController_RepeatedUnavailableChecksCompleteUnverified(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationUnavailable: 3},
		script(stepEdit, stepUnavailable, stepUnavailable, stepUnavailable, say("Done.")))
	if run.err != nil {
		t.Fatalf("err = %v, want a conclusive result", run.err)
	}
	termination := run.result.Termination
	if run.result.CompletionStatus != CompletionConclusive || termination.Kind != TerminationCompletedUnverified {
		t.Fatalf("result = %+v", run.result)
	}
	if !strings.Contains(termination.Reason, "3 verification attempts in a row could not run") || !strings.Contains(termination.Reason, "test is not an accepted check") {
		t.Fatalf("reason = %q", termination.Reason)
	}
	if run.requests != 5 || run.continuations != 0 {
		t.Fatalf("requests=%d continuations=%d", run.requests, run.continuations)
	}
}

func TestController_UnavailableBelowTheLimitStillAsksForVerification(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationUnavailable: 3},
		script(stepEdit, stepUnavailable, stepUnavailable, say("Done."), stepPass, say("Done and verified.")))
	if run.err != nil || run.result.Termination.Kind != "" || run.result.CompletionStatus != CompletionConclusive {
		t.Fatalf("err=%v result=%+v", run.err, run.result)
	}
	if run.continuations != 1 || run.requests != 6 {
		t.Fatalf("continuations=%d requests=%d, want one continuation then a real check", run.continuations, run.requests)
	}
}

func TestController_RefusedCheckAfterAPassDoesNotUndoIt(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationUnavailable: 3},
		script(stepEdit, stepPass, stepUnavailable, say("Done and verified.")))
	if run.err != nil || run.result.Termination.Kind != "" || run.continuations != 0 {
		t.Fatalf("err=%v termination=%+v continuations=%d, want the earlier pass to stand", run.err, run.result.Termination, run.continuations)
	}
}

func TestController_SameFailureWithNoChangeStalls(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationStalls: 3},
		script(stepEdit, stepFail, stepFail, stepFail, say("Done.")))
	if run.err == nil {
		t.Fatal("a stalled verification must not be a conclusive result")
	}
	var incomplete *IncompleteTurnError
	if !errors.As(run.err, &incomplete) || incomplete.Code != TerminationVerificationStalled {
		t.Fatalf("err = %#v, want an incomplete turn with code %s", run.err, TerminationVerificationStalled)
	}
	if run.result.CompletionStatus != CompletionIncomplete || run.result.Termination.Kind != TerminationVerificationStalled {
		t.Fatalf("result = %+v", run.result)
	}
	if !strings.Contains(run.result.Termination.Reason, "failed 3 times in a row with no workspace change") {
		t.Fatalf("reason = %q", run.result.Termination.Reason)
	}
	if run.requests != 5 {
		t.Fatalf("requests = %d, want the run to stop at the first final answer after the streak", run.requests)
	}
	notice := PresentIncompleteResult(run.err)
	if notice.Code != TerminationVerificationStalled || !strings.Contains(notice.NextAction, "Fix or replace the check") {
		t.Fatalf("notice = %+v", notice)
	}
}

func TestController_FailuresSeparatedByChangesAreProgress(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationStalls: 2},
		script(stepEdit, stepFail, stepEdit, stepFail, stepEdit, stepPass, say("Done and verified.")))
	if run.err != nil || run.result.Termination.Kind != "" {
		t.Fatalf("err=%v termination=%+v, want a normal finish", run.err, run.result.Termination)
	}
}

func TestController_FailedCheckIsNeverReportedAsUnverified(t *testing.T) {
	run := runStallScript(t, CompletionContract{
		MaxVerificationUnavailable: 1,
		VerificationSurface:        func() (bool, string) { return false, "none" },
	}, script(stepEdit, stepFail, say("Done."), stepPass, say("Done and verified.")))
	if run.err != nil || run.result.Termination.Kind != "" {
		t.Fatalf("err=%v termination=%+v", run.err, run.result.Termination)
	}
	if run.continuations != 1 {
		t.Fatalf("continuations = %d, want the failing check to be sent back once", run.continuations)
	}
}

func TestController_VerificationContinuationCapEndsTheLoop(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationContinuations: 3},
		script(stepEdit, say("Next step: verify."), say("Next step: verify."), say("Next step: verify."), say("Next step: verify.")))
	if run.err == nil || run.result.Termination.Kind != TerminationVerificationStalled {
		t.Fatalf("err=%v result=%+v, want verification_stalled", run.err, run.result)
	}
	if run.requests != 5 || run.continuations != 3 {
		t.Fatalf("requests=%d continuations=%d, want 3 continuations then the cap", run.requests, run.continuations)
	}
	if !strings.Contains(run.result.Termination.Reason, "still unsettled after 3 continuations") {
		t.Fatalf("reason = %q", run.result.Termination.Reason)
	}
}

func TestController_VerificationLimitsAreOffByDefault(t *testing.T) {
	steps := script(stepEdit, say("Next step."), say("Next step."), say("Next step."), say("Next step."), say("Next step."), say("Next step."), say("Next step."), say("Next step."), say("Next step."), stepPass, say("Done and verified."))
	run := runStallScript(t, CompletionContract{}, steps)
	if run.err != nil || run.result.Termination.Kind != "" || run.continuations != 9 {
		t.Fatalf("err=%v termination=%+v continuations=%d, want zero-valued limits to change nothing", run.err, run.result.Termination, run.continuations)
	}
}

func TestController_NoChangeContinuationsAreNotVerificationContinuations(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationContinuations: 1, MaxNoChangeContinuations: 3},
		script(say("Report: nothing to change."), say("Report: nothing to change."), say("Report: nothing to change."), say("Report: nothing to change.")))
	if run.err == nil || run.result.Termination.Kind != "no_observable_change" {
		t.Fatalf("err=%v termination=%+v, want the no-change cap to end this run, not the verification cap", run.err, run.result.Termination)
	}
	if run.requests != 4 {
		t.Fatalf("requests = %d, want 4", run.requests)
	}
}

func TestController_UnverifiedStopSkippedWhenTheAnswerFailsItsOutputContract(t *testing.T) {
	run := runStallScript(t, CompletionContract{
		VerificationSurface: func() (bool, string) { return false, "none" },
		ValidateFinalResponse: func(text string) error {
			if !strings.HasPrefix(text, "OK:") {
				return fmt.Errorf("reply must start with OK:")
			}
			return nil
		},
	}, script(stepEdit, say("Done."), say("Done again."), say("OK: done")))
	if run.err != nil || run.result.Termination.Kind != TerminationCompletedUnverified {
		t.Fatalf("err=%v result=%+v", run.err, run.result)
	}
	if run.result.Content != "OK: done" || run.requests != 4 || run.continuations != 2 {
		t.Fatalf("content=%q requests=%d continuations=%d, want the invalid confirmed answer sent back once more", run.result.Content, run.requests, run.continuations)
	}
}

func TestController_UnavailableChecksBeforeLaterEditsDoNotEndTheRun(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationUnavailable: 3},
		script(stepEdit, stepUnavailable, stepUnavailable, stepUnavailable, stepEdit, say("Next step: check it."), stepPass, say("Done and verified.")))
	if run.err != nil || run.result.Termination.Kind != "" {
		t.Fatalf("err=%v termination=%+v, want the run to continue after a later edit", run.err, run.result.Termination)
	}
	if run.continuations != 1 || run.requests != 8 {
		t.Fatalf("continuations=%d requests=%d, want one continuation and then a real check", run.continuations, run.requests)
	}
}

func TestController_ToolNoticesWarnBeforeTheLimit(t *testing.T) {
	run := runStallScript(t, CompletionContract{MaxVerificationUnavailable: 3, MaxVerificationStalls: 3},
		script(stepEdit, stepUnavailable, stepUnavailable, stepUnavailable, say("Done.")))
	warned := 0
	for _, message := range run.history.messages {
		if message.Role != "tool" {
			continue
		}
		text := model.ExtractTextContentOrEmpty(message.Content)
		if strings.Contains(text, "Harness notice: 2 verification attempts in a row did not run") && strings.Contains(text, "completed_unverified") {
			warned++
		}
		if strings.Contains(text, "Harness notice: 1 verification attempts") || strings.Contains(text, "Harness notice: 3 verification attempts") {
			t.Errorf("notice at the wrong streak: %q", text)
		}
	}
	if warned != 1 {
		t.Fatalf("warned = %d, want exactly one notice one step before the limit", warned)
	}

	run = runStallScript(t, CompletionContract{MaxVerificationStalls: 3},
		script(stepEdit, stepFail, stepFail, stepFail, say("Done.")))
	warned = 0
	for _, message := range run.history.messages {
		if message.Role == "tool" && strings.Contains(model.ExtractTextContentOrEmpty(message.Content), "failed 2 times in a row with no workspace change between runs") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("failure notices = %d, want one", warned)
	}
}

func TestController_ContinuationInstructionExplainsHowToVerify(t *testing.T) {
	run := runStallScript(t, CompletionContract{}, script(stepEdit, say("Next step: check."), stepPass, say("Done and verified.")))
	if run.err != nil {
		t.Fatal(run.err)
	}
	var instruction string
	for _, message := range run.history.messages {
		if text := model.ExtractTextContentOrEmpty(message.Content); message.Role == "user" && strings.Contains(text, "Unmet completion criterion") {
			instruction = text
		}
	}
	for _, want := range []string{"missing successful verification", "run_tests", "run_verification", "go test ./...", "do not count", "unverified", "scratch test files"} {
		if !strings.Contains(instruction, want) {
			t.Errorf("continuation instruction omits %q:\n%s", want, instruction)
		}
	}

	noChange := continuationInstruction(&CompletionContractError{Reason: CompletionMissingObservableChange, Detail: "task requires observable workspace change but no mutations were recorded"}, "Report")
	if strings.Contains(noChange, "run_tests") {
		t.Errorf("a no-change continuation must not carry the verification hint:\n%s", noChange)
	}
	if !strings.Contains(PersistenceInstruction, "unverified") || !strings.Contains(PersistenceInstruction, "scratch files") {
		t.Errorf("the persistence instruction must tell the model what to do when nothing can verify: %s", PersistenceInstruction)
	}
}
