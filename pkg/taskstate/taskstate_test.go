package taskstate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func validState() CheckpointState {
	return CheckpointState{
		Schema:  SchemaVersion,
		TaskID:  "task-001",
		Status:  StatusInProgress,
		Summary: "Ported two of five test files.",
		Completed: []CompletedItem{
			{Text: "Port session store tests", EvidenceID: "ev_1"},
		},
		Checks: []VerificationEntry{
			{Check: "unit tests", Scope: "pkg/storage", Status: VerificationPass, Required: true, EvidenceID: "ev_1"},
			{Check: "full suite", Scope: "repository", Status: VerificationPending, Required: true},
		},
		NextActions: []NextAction{
			{Text: "Run the full suite", Kind: "verify"},
			{Text: "Port the remaining fixtures", Kind: "edit"},
		},
		UpdatedAt: time.Now().UTC(),
	}
}

func TestCheckpointState_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*CheckpointState)
		wantErr string
	}{
		{name: "valid", mutate: func(*CheckpointState) {}},
		{
			name:    "wrong schema",
			mutate:  func(s *CheckpointState) { s.Schema = "v0" },
			wantErr: "schema",
		},
		{
			name:    "missing task id",
			mutate:  func(s *CheckpointState) { s.TaskID = " " },
			wantErr: "task_id",
		},
		{
			name:    "unknown status",
			mutate:  func(s *CheckpointState) { s.Status = "done" },
			wantErr: "unknown status",
		},
		{
			name: "pass without evidence",
			mutate: func(s *CheckpointState) {
				s.Checks[0].EvidenceID = ""
			},
			wantErr: "pass requires an evidence id",
		},
		{
			name: "completed with unmet required check",
			mutate: func(s *CheckpointState) {
				s.Status = StatusCompleted
			},
			wantErr: "required verification",
		},
		{
			name: "completed with unevidenced completed item",
			mutate: func(s *CheckpointState) {
				s.Status = StatusCompleted
				s.Checks[1].Status = VerificationPass
				s.Checks[1].EvidenceID = "ev_2"
				s.Completed = append(s.Completed, CompletedItem{Text: "undocumented claim"})
			},
			wantErr: "has no evidence id",
		},
		{
			name: "in flight with unevidenced completed item is debt, not an error",
			mutate: func(s *CheckpointState) {
				s.Completed = append(s.Completed, CompletedItem{Text: "pending claim"})
			},
		},
		{
			name: "completed with blocker",
			mutate: func(s *CheckpointState) {
				s.Status = StatusCompleted
				s.Checks = s.Checks[:1]
				s.Blocker = &Blocker{Reason: "waiting"}
			},
			wantErr: "active blocker",
		},
		{
			name:    "blocked without blocker record",
			mutate:  func(s *CheckpointState) { s.Status = StatusBlocked },
			wantErr: "requires a blocker",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := validState()
			tc.mutate(&s)
			err := s.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestTriggerEvaluator_PriorityAndThreshold(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		eval   TriggerEvaluator
		sig    Signals
		want   TriggerReason
		wantOK bool
	}{
		{name: "nothing", sig: Signals{}, wantOK: false},
		{name: "pressure below default", sig: Signals{PressureRatio: 0.64}, wantOK: false},
		{name: "pressure at default", sig: Signals{PressureRatio: 0.65}, want: TriggerPressure, wantOK: true},
		{
			name:   "configured threshold wins",
			eval:   TriggerEvaluator{PressureThreshold: 0.5},
			sig:    Signals{PressureRatio: 0.55},
			want:   TriggerPressure,
			wantOK: true,
		},
		{
			name:   "shutdown beats everything",
			sig:    Signals{ShuttingDown: true, EpochBoundary: true, BlockerRaised: true, PressureRatio: 1},
			want:   TriggerShutdown,
			wantOK: true,
		},
		{
			name:   "epoch beats blocker",
			sig:    Signals{EpochBoundary: true, BlockerRaised: true},
			want:   TriggerEpochBoundary,
			wantOK: true,
		},
		{
			name:   "blocker beats model change",
			sig:    Signals{BlockerRaised: true, ModelChanged: true},
			want:   TriggerBlocker,
			wantOK: true,
		},
		{
			name:   "test state beats decision",
			sig:    Signals{TestStateChanged: true, DecisionRecorded: true},
			want:   TriggerTestStateChange,
			wantOK: true,
		},
		{
			name:   "edit batch end alone",
			sig:    Signals{EditBatchEnded: true},
			want:   TriggerEditBatchEnd,
			wantOK: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tc.eval.Evaluate(tc.sig)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("Evaluate(%+v) = (%q, %v), want (%q, %v)", tc.sig, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestRenderMarkdown_Sections(t *testing.T) {
	t.Parallel()

	s := validState()
	s.GoalID = "goal-9"
	s.Spend = Spend{USD: 8.41, BudgetUSD: 12}
	s.Blocker = &Blocker{Reason: "needs DATABASE_URL", Needs: "integration env"}
	s.Status = StatusBlocked
	s.Questions = []Question{{Text: "Keep legacy fixtures?", BlockingTasks: []string{"task-005"}}}
	s.Files = []string{"pkg/storage/store.go"}

	out := RenderMarkdown(s)
	for _, want := range []string{
		"type: buckley-task-checkpoint",
		"schema: " + SchemaVersion,
		"task_id: task-001",
		"goal_id: goal-9",
		"status: blocked",
		"spend_usd: 8.41 / 12.00",
		"# Completed (evidence-linked)",
		"- [x] Port session store tests (`ev_1`)",
		"# Verification",
		"| unit tests | pkg/storage | pass | `ev_1` |",
		"| full suite | repository | pending | — |",
		"# Parked",
		"needs DATABASE_URL — needs: integration env",
		"# Questions for you",
		"1. Keep legacy fixtures? (blocks task-005)",
		"# Next actions",
		"1. Run the full suite [verify]",
		"# Files",
		"- pkg/storage/store.go",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func newTestManager(t *testing.T) (*Manager, *evidence.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	rl, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	mgr, err := NewManager(rl, ev)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, ev
}

func TestManager_SaveResumeRoundTrip(t *testing.T) {
	t.Parallel()
	mgr, ev := newTestManager(t)
	ctx := context.Background()

	first, err := mgr.Save(ctx, SaveInput{
		State:     validState(),
		Reason:    TriggerEditBatchEnd,
		SessionID: "sess-1",
		RunID:     "run-1",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if first.Version != 1 || first.CheckpointID == "" {
		t.Fatalf("first checkpoint = %+v, want version 1 with id", first)
	}
	if first.Reason != string(TriggerEditBatchEnd) {
		t.Fatalf("reason = %q, want %q", first.Reason, TriggerEditBatchEnd)
	}

	// The rendered Markdown view is a durable evidence object.
	obj, err := ev.Get(ctx, first.MarkdownEvidenceID)
	if err != nil {
		t.Fatalf("evidence.Get(%s): %v", first.MarkdownEvidenceID, err)
	}
	if !strings.Contains(string(obj.InlineBody), "type: buckley-task-checkpoint") {
		t.Fatalf("evidence body is not the rendered checkpoint:\n%s", obj.InlineBody)
	}

	// A second save becomes version 2 and chains to the first.
	updated := validState()
	updated.Checks[1].Status = VerificationPass
	updated.Checks[1].EvidenceID = "ev_2"
	updated.Status = StatusCompleted
	second, err := mgr.Save(ctx, SaveInput{
		State:     updated,
		Reason:    TriggerTestStateChange,
		SessionID: "sess-1",
		RunID:     "run-1",
	})
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if second.Version != 2 || second.ParentCheckpointID != first.CheckpointID {
		t.Fatalf("second checkpoint = %+v, want version 2 chained to %s", second, first.CheckpointID)
	}

	resumed, err := mgr.Resume(ctx, "task-001")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Checkpoint.CheckpointID != second.CheckpointID {
		t.Fatalf("Resume picked %s, want latest %s", resumed.Checkpoint.CheckpointID, second.CheckpointID)
	}
	if resumed.State.Status != StatusCompleted {
		t.Fatalf("resumed status = %q, want completed", resumed.State.Status)
	}
	for _, want := range []string{
		"Resuming task task-001",
		"version 2",
		"reason: test_state_change",
		"Next actions, in order:",
		"1. Run the full suite",
	} {
		if !strings.Contains(resumed.Prompt, want) {
			t.Fatalf("resume prompt missing %q:\n%s", want, resumed.Prompt)
		}
	}
}

func TestManager_SaveRejectsInvalidState(t *testing.T) {
	t.Parallel()
	mgr, _ := newTestManager(t)

	bad := validState()
	bad.Checks[0].EvidenceID = "" // pass without evidence
	if _, err := mgr.Save(context.Background(), SaveInput{State: bad, Reason: TriggerPressure}); err == nil {
		t.Fatal("Save accepted an unevidenced pass; the store must reject it")
	}

	good := validState()
	if _, err := mgr.Save(context.Background(), SaveInput{State: good}); err == nil {
		t.Fatal("Save accepted an empty reason")
	}
}

func TestManager_ResumeUnknownTask(t *testing.T) {
	t.Parallel()
	mgr, _ := newTestManager(t)
	if _, err := mgr.Resume(context.Background(), "task-none"); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("Resume unknown = %v, want ErrNoCheckpoint", err)
	}
}

// failingLedger simulates a transient database error on reads so tests can
// prove that Save and Resume fail loudly instead of silently forking the
// checkpoint chain (a read error is not the same as "no checkpoint yet").
type failingLedger struct {
	err error
}

func (f failingLedger) CreateTaskCheckpoint(context.Context, runledger.TaskCheckpoint) (runledger.TaskCheckpoint, error) {
	return runledger.TaskCheckpoint{}, errors.New("unreachable: save must fail before writing")
}

func (f failingLedger) LatestTaskCheckpoint(context.Context, string) (runledger.TaskCheckpoint, error) {
	return runledger.TaskCheckpoint{}, f.err
}

type nopEvidence struct{}

func (nopEvidence) Put(_ context.Context, obj evidence.Object) (evidence.Object, error) {
	obj.ID = "ev_nop"
	return obj, nil
}

func TestManager_TransientLedgerErrorFailsLoudly(t *testing.T) {
	t.Parallel()

	transient := errors.New("database is locked")
	mgr, err := NewManager(failingLedger{err: transient}, nopEvidence{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if _, err := mgr.Save(context.Background(), SaveInput{State: validState(), Reason: TriggerPressure}); !errors.Is(err, transient) {
		t.Fatalf("Save with transient read error = %v, want the transient error surfaced", err)
	}

	_, err = mgr.Resume(context.Background(), "task-001")
	if errors.Is(err, ErrNoCheckpoint) {
		t.Fatal("Resume treated a transient read error as ErrNoCheckpoint")
	}
	if !errors.Is(err, transient) {
		t.Fatalf("Resume with transient read error = %v, want the transient error surfaced", err)
	}
}

func TestVerificationDebt(t *testing.T) {
	t.Parallel()
	s := validState()
	if got := s.VerificationDebt(); got != 1 {
		t.Fatalf("debt = %d, want 1 (one pending check)", got)
	}
	s.Completed = append(s.Completed, CompletedItem{Text: "unverified claim"})
	if got := s.VerificationDebt(); got != 2 {
		t.Fatalf("debt = %d, want 2 (pending check plus unevidenced claim)", got)
	}
	s.Checks[1].Status = VerificationPass
	s.Checks[1].EvidenceID = "ev_2"
	s.Completed[1].EvidenceID = "ev_3"
	if got := s.VerificationDebt(); got != 0 {
		t.Fatalf("debt = %d, want 0", got)
	}
}
