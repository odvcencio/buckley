package goalloop

import (
	"context"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// TestLoop_CompletionGateRoutesToVerify locks the G7 gate: a completion
// claim with an unmet required check does not complete — the gate
// rejection lands on the ledger and the next turn runs in the verify
// phase. Once the check passes with evidence, completion goes through.
func TestLoop_CompletionGateRoutesToVerify(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{
			Rounds: 2, StateChanged: true, Summary: "ported the files",
			Completed: true, CompletedEvidenceID: "ev_claim",
			Checks: []taskstate.VerificationEntry{
				{Check: "unit tests", Status: taskstate.VerificationPending, Required: true},
			},
		},
		{
			Rounds: 1, Summary: "tests pass",
			Completed: true, CompletedEvidenceID: "ev_claim",
			Checks: []taskstate.VerificationEntry{
				{Check: "unit tests", Status: taskstate.VerificationPass, Required: true, EvidenceID: "ev_tests"},
			},
		},
	}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "port files"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID

	result, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if result.Status != taskstate.StatusCompleted || result.Turns != 2 {
		t.Fatalf("result = %+v, want completion on turn 2 after a verify pass", result)
	}
	if engine.seen[1].Phase != PhaseVerify {
		t.Fatalf("second turn phase = %q, want verify after the gate rejection", engine.seen[1].Phase)
	}

	events, _ := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	var sawRejection bool
	for _, ev := range events {
		if ev.Type == runledger.EventControllerDecision &&
			ev.Payload["decision"] == "verification_gate_rejected_completion" {
			sawRejection = true
		}
	}
	if !sawRejection {
		t.Fatal("gate rejection was not recorded on the ledger")
	}

	// The completed checkpoint carries the evidenced check.
	resumed, err := loop.checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(resumed.State.Checks) != 1 || resumed.State.Checks[0].EvidenceID != "ev_tests" {
		t.Fatalf("final checks = %+v, want the evidenced pass", resumed.State.Checks)
	}
}

// TestLoop_SecondPrematureCompletionParks locks the escape hatch: an
// engine that keeps claiming completion without evidence parks after
// the second rejection instead of looping forever.
func TestLoop_SecondPrematureCompletionParks(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 1, Completed: true /* no evidence */, Summary: "trust me"},
	}}
	loop, _ := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "unverifiable work"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := loop.RunTask(ctx, intake.RunID, intake.Tasks[0].TaskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if result.Status != taskstate.StatusParked || result.Turns != 2 {
		t.Fatalf("result = %+v, want parked after two premature claims", result)
	}
}

// TestLoop_DebtRoutesVerifyPhase locks the controller integration: state
// change with outstanding required checks routes the next turn into the
// verify phase rather than yielding the drive.
func TestLoop_DebtRoutesVerifyPhase(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{
			Rounds: 1, StateChanged: true, Summary: "edited the store",
			Checks: []taskstate.VerificationEntry{
				{Check: "unit tests", Status: taskstate.VerificationPending, Required: true},
			},
		},
		{
			Rounds: 1, Summary: "tests green",
			Checks: []taskstate.VerificationEntry{
				{Check: "unit tests", Status: taskstate.VerificationPass, Required: true, EvidenceID: "ev_ok"},
			},
		},
		{Rounds: 1, Completed: true, CompletedEvidenceID: "ev_ok", Summary: "done"},
	}}
	loop, _ := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "edit then verify"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := loop.RunTask(ctx, intake.RunID, intake.Tasks[0].TaskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if result.Status != taskstate.StatusCompleted || result.Turns != 3 {
		t.Fatalf("result = %+v, want completion on turn 3", result)
	}
	phases := []string{engine.seen[0].Phase, engine.seen[1].Phase, engine.seen[2].Phase}
	if phases[0] != PhaseExecute || phases[1] != PhaseVerify || phases[2] != PhaseExecute {
		t.Fatalf("phases = %v, want execute, verify, execute", phases)
	}
}

// TestLoop_RetryAfterUnparks locks retry-timer unparking: a blocked task
// with a past retry timer re-enters the queue as pending; a future timer
// keeps it out.
func TestLoop_RetryAfterUnparks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		retryIn   time.Duration
		wantQueue int
	}{
		{name: "past retry timer requeues", retryIn: -time.Minute, wantQueue: 1},
		{name: "future retry timer stays out", retryIn: time.Hour, wantQueue: 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			retryAt := time.Now().Add(tc.retryIn)
			engine := &scriptedEngine{outcomes: []TurnOutcome{
				{Rounds: 1, Blocker: &taskstate.Blocker{Reason: "rate limited", RetryAfter: &retryAt}},
			}}
			loop, _ := newTestLoop(t, Config{Engine: engine})
			ctx := context.Background()

			intake, err := loop.Start(ctx, Goal{Statement: "flaky dependency"})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if _, err := loop.RunTask(ctx, intake.RunID, intake.Tasks[0].TaskID, intake.Goal, intake.Tasks[0].Spec); err != nil {
				t.Fatalf("RunTask: %v", err)
			}

			queue, err := loop.BuildQueue(ctx, intake.RunID)
			if err != nil {
				t.Fatalf("BuildQueue: %v", err)
			}
			if len(queue) != tc.wantQueue {
				t.Fatalf("queue = %+v, want %d entries", queue, tc.wantQueue)
			}
			if tc.wantQueue == 1 && queue[0].Status != taskstate.StatusPending {
				t.Fatalf("requeued status = %q, want pending", queue[0].Status)
			}
		})
	}
}

// TestLoop_QuestionsPersistOnCheckpoints locks question deferral: a
// turn's questions land on the checkpoint and survive into the next
// drive's resume context — the loop never blocks on them.
func TestLoop_QuestionsPersistOnCheckpoints(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{
			Rounds: 1, StateChanged: true, Summary: "found a fork in the road",
			Questions: []taskstate.Question{{Text: "Keep legacy fixtures?", BlockingTasks: []string{"task-005"}}},
			Completed: true, CompletedEvidenceID: "ev_done",
		},
	}}
	loop, _ := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "port with open questions"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	if _, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	resumed, err := loop.checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(resumed.State.Questions) != 1 || resumed.State.Questions[0].Text != "Keep legacy fixtures?" {
		t.Fatalf("questions = %+v, want the deferred question on the checkpoint", resumed.State.Questions)
	}
}
