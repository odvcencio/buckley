package dapr_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/durability/dapr"
	"m31labs.dev/buckley/pkg/durability/goalrunner"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/replay"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// restartEngine drives the kill/restart acceptance: turn 000 completes
// normally, turn 001 signals the test and then blocks until released,
// so the test can kill the worker while the turn is in flight.
type restartEngine struct {
	mu       sync.Mutex
	calls    map[string]int
	turn2    chan struct{}
	release  chan struct{}
	signaled bool
	// completedEvidenceID must reference a real stored object so replay
	// verification can resolve the completion claim.
	completedEvidenceID string
}

func newRestartEngine() *restartEngine {
	return &restartEngine{
		calls:   map[string]int{},
		turn2:   make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *restartEngine) RunTurn(ctx context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.mu.Lock()
	e.calls[task.TurnID]++
	first := !e.signaled && strings.HasSuffix(task.TurnID, "/turn-001")
	if first {
		e.signaled = true
	}
	e.mu.Unlock()

	if strings.HasSuffix(task.TurnID, "/turn-000") {
		return goalloop.TurnOutcome{Rounds: 1, StateChanged: true, Summary: "first turn"}, nil
	}
	if first {
		close(e.turn2)
	}
	select {
	case <-e.release:
		return goalloop.TurnOutcome{Rounds: 1, Completed: true, CompletedEvidenceID: e.completedEvidenceID, Summary: "done"}, nil
	case <-ctx.Done():
		return goalloop.TurnOutcome{}, ctx.Err()
	}
}

func (e *restartEngine) turnCalls(turnSuffix string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0
	for turnID, count := range e.calls {
		if strings.HasSuffix(turnID, turnSuffix) {
			total += count
		}
	}
	return total
}

// barrierEngine proves real fan-out: every task's turn blocks until
// both tasks have started. Sequential scheduling deadlocks, so goal
// completion is a deterministic parallelism witness.
type barrierEngine struct {
	mu                  sync.Mutex
	started             map[string]bool
	both                chan struct{}
	completedEvidenceID string
}

func newBarrierEngine() *barrierEngine {
	return &barrierEngine{started: map[string]bool{}, both: make(chan struct{})}
}

func (e *barrierEngine) RunTurn(ctx context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.mu.Lock()
	e.started[task.TaskID] = true
	if len(e.started) == 2 {
		select {
		case <-e.both:
		default:
			close(e.both)
		}
	}
	e.mu.Unlock()
	select {
	case <-e.both:
		return goalloop.TurnOutcome{Rounds: 1, Completed: true, CompletedEvidenceID: e.completedEvidenceID, Summary: "parallel turn"}, nil
	case <-ctx.Done():
		return goalloop.TurnOutcome{}, ctx.Err()
	}
}

// approvalEngine parks on premature completion twice, then completes
// with valid evidence once approval unparks it: turn 1 claims
// completion without evidence (gate rejects, verify routes), turn 2
// claims again (second premature claim parks), turn 3 completes.
type approvalEngine struct {
	mu                  sync.Mutex
	calls               int
	completedEvidenceID string
}

func (e *approvalEngine) RunTurn(_ context.Context, _ goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()
	if call <= 2 {
		return goalloop.TurnOutcome{Rounds: 1, Completed: true, Summary: "claiming early"}, nil
	}
	return goalloop.TurnOutcome{Rounds: 1, Completed: true, CompletedEvidenceID: e.completedEvidenceID, Summary: "done after approval"}, nil
}

type staticPlanner struct{ specs []goalloop.TaskSpec }

func (p staticPlanner) Decompose(context.Context, goalloop.Goal) ([]goalloop.TaskSpec, error) {
	return p.specs, nil
}

type completingEngine struct{ calls int }

func (e *completingEngine) RunTurn(_ context.Context, _ goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.calls++
	if e.calls == 1 {
		return goalloop.TurnOutcome{Rounds: 1, StateChanged: true, Summary: "first turn"}, nil
	}
	return goalloop.TurnOutcome{Rounds: 1, Completed: true, CompletedEvidenceID: "ev_it", Summary: "done"}, nil
}

func integrationEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := os.Getenv("BUCKLEY_DAPR_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BUCKLEY_DAPR_TEST_ENDPOINT is not set; skipping sidecar integration test")
	}
	return endpoint
}

func newIntegrationGoal(t *testing.T, ctx context.Context, engine goalloop.TurnEngine, statement string, planner goalloop.Planner) (*goalloop.Loop, *evidence.SQLiteStore, *runledger.SQLiteStore, *goalloop.Intake, map[string]goalloop.TaskSpec) {
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
	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	loop, err := goalloop.New(goalloop.Config{
		Ledger:      ledger,
		Checkpoints: checkpoints,
		Engine:      engine,
		Planner:     planner,
		SessionID:   "dapr-it",
	})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	intake, err := loop.Start(ctx, goalloop.Goal{Statement: statement})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	specs := make(map[string]goalloop.TaskSpec, len(intake.Tasks))
	for _, task := range intake.Tasks {
		specs[task.TaskID] = task.Spec
	}
	return loop, ev, ledger, intake, specs
}

// TestDaprBackend_GoalCompletes runs the Phase 1 happy path against a
// real sidecar or the durabletask emulator. Set
// BUCKLEY_DAPR_TEST_ENDPOINT (for example localhost:50001 under
// `dapr run`, or the `durabletask-go` emulator port) to enable it; CI
// without an endpoint skips.
func TestDaprBackend_GoalCompletes(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	loop, _, _, intake, specs := newIntegrationGoal(t, ctx, &completingEngine{}, "integration goal", nil)

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if err := backend.StartWorker(ctx, goalrunner.New(loop, intake.Goal, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, durability.GoalStart{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 {
		t.Fatalf("status = %+v, want one completed task", status)
	}
	if status.Result.Tasks[0].Status != taskstate.StatusCompleted {
		t.Fatalf("task outcome = %+v", status.Result.Tasks[0])
	}
}

// TestDaprBackend_WorkerRestartResumesWithoutReexecution is the Phase 1
// acceptance from spec.durable-execution-dapr: kill the worker during a
// turn, restart it, and the workflow resumes without re-executing the
// completed turn. Replay verification must agree afterward.
func TestDaprBackend_WorkerRestartResumesWithoutReexecution(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := newRestartEngine()
	loop, ev, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "restart goal", nil)
	completed, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, MediaType: "text/plain", InlineBody: []byte("restart acceptance completion evidence")})
	if err != nil {
		t.Fatalf("store completion evidence: %v", err)
	}
	engine.completedEvidenceID = completed.ID
	runner := goalrunner.New(loop, intake.Goal, specs)

	first, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New first: %v", err)
	}
	workerCtx, killWorker := context.WithCancel(ctx)
	if err := first.StartWorker(workerCtx, runner); err != nil {
		killWorker()
		t.Fatalf("StartWorker first: %v", err)
	}
	instanceID, err := first.StartGoal(ctx, durability.GoalStart{RunID: intake.RunID})
	if err != nil {
		killWorker()
		t.Fatalf("StartGoal: %v", err)
	}

	// Wait until the second turn is in flight, then kill the worker
	// mid-turn: cancel its listener and drop its connection.
	select {
	case <-engine.turn2:
	case <-ctx.Done():
		killWorker()
		t.Fatal("turn 001 never started")
	}
	killWorker()
	_ = first.Close()
	close(engine.release)

	second, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.StartWorker(ctx, runner); err != nil {
		t.Fatalf("StartWorker second: %v", err)
	}
	// StartGoal on the restarted worker attaches to the running
	// instance instead of scheduling a duplicate.
	attachedID, err := second.StartGoal(ctx, durability.GoalStart{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("StartGoal attach: %v", err)
	}
	if attachedID != instanceID {
		t.Fatalf("attach = %s, want %s", attachedID, instanceID)
	}
	status, err := second.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 || status.Result.Tasks[0].Status != taskstate.StatusCompleted {
		t.Fatalf("status = %+v, want one completed task after restart", status)
	}

	// The completed first turn ran exactly once across both workers.
	if calls := engine.turnCalls("/turn-000"); calls != 1 {
		t.Fatalf("turn 000 executed %d times, want exactly 1", calls)
	}
	if calls := engine.turnCalls("/turn-001"); calls < 1 {
		t.Fatalf("turn 001 executed %d times, want at least 1", calls)
	}

	// The durable audit agrees: replay verification passes on the run.
	report, err := replay.Verify(ctx, ledger, ledger, ev, intake.RunID)
	if err != nil {
		t.Fatalf("replay.Verify: %v", err)
	}
	if !report.Valid {
		t.Fatalf("replay report = %+v, want valid", report)
	}
}

// TestDaprBackend_ResolverServesGoalFromLedger runs the standalone
// worker path: the activity host resolves the goal through LoadGoal on
// first use instead of being constructed for it.
func TestDaprBackend_ResolverServesGoalFromLedger(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	loop, _, _, intake, _ := newIntegrationGoal(t, ctx, &completingEngine{}, "resolver goal", nil)
	resolver := goalrunner.NewResolver(func(ctx context.Context, runID string) (*goalrunner.Runner, error) {
		goal, specs, err := loop.LoadGoal(ctx, runID)
		if err != nil {
			return nil, err
		}
		return goalrunner.New(loop, goal, specs), nil
	})

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, resolver); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, durability.GoalStart{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 || status.Result.Tasks[0].Status != taskstate.StatusCompleted {
		t.Fatalf("status = %+v, want one completed task via resolver", status)
	}
}

// runApprovalScenario drives one goal into a durable approval wait,
// lets decide act on the waiting instance, and returns the terminal
// goal status plus the run's ledger events.
func runApprovalScenario(t *testing.T, approvalWaitMS int64, decide func(t *testing.T, backend *dapr.Backend, waitingInstanceID string)) (durability.GoalStatus, []runledger.Event, int) {
	t.Helper()
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine := &approvalEngine{}
	loop, ev, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "approval goal", nil)
	completed, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, MediaType: "text/plain", InlineBody: []byte("approval completion evidence")})
	if err != nil {
		t.Fatalf("store completion evidence: %v", err)
	}
	engine.completedEvidenceID = completed.ID

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, goalrunner.New(loop, intake.Goal, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, durability.GoalStart{RunID: intake.RunID, ApprovalWaitMS: approvalWaitMS})
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}

	if decide != nil {
		// The wait lands on the ledger before the workflow blocks; poll
		// for it, then resolve.
		var waitingInstance string
		for waitingInstance == "" {
			select {
			case <-ctx.Done():
				t.Fatal("approval wait never recorded")
			case <-time.After(100 * time.Millisecond):
			}
			events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			for _, ev := range events {
				if ev.Type == runledger.EventDurableApprovalWaiting {
					waitingInstance, _ = ev.Payload["workflow_instance_id"].(string)
				}
			}
		}
		decide(t, backend, waitingInstance)
	}

	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	engine.mu.Lock()
	calls := engine.calls
	engine.mu.Unlock()
	return status, events, calls
}

func approvalOutcome(events []runledger.Event) string {
	outcome := ""
	for _, ev := range events {
		if ev.Type == runledger.EventDurableApprovalResolved {
			outcome, _ = ev.Payload["outcome"].(string)
		}
	}
	return outcome
}

// TestDaprBackend_ApprovalResumesParkedTask: approve → unpark → the
// task completes in a fresh generation.
func TestDaprBackend_ApprovalResumesParkedTask(t *testing.T) {
	status, events, turns := runApprovalScenario(t, 60_000, func(t *testing.T, backend *dapr.Backend, instanceID string) {
		if err := backend.RaiseApproval(context.Background(), instanceID, durability.ApprovalDecision{Approved: true, Reason: "go ahead"}); err != nil {
			t.Fatalf("RaiseApproval: %v", err)
		}
	})
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 || status.Result.Tasks[0].Status != taskstate.StatusCompleted {
		t.Fatalf("status = %+v, want completed task after approval", status)
	}
	if turns != 3 {
		t.Fatalf("engine turns = %d, want 3 (two premature claims, one approved completion)", turns)
	}
	if got := approvalOutcome(events); got != durability.ApprovalApproved {
		t.Fatalf("approval outcome = %q, want approved", got)
	}
}

// TestDaprBackend_ApprovalDenyKeepsTaskParked: deny → parked stays.
func TestDaprBackend_ApprovalDenyKeepsTaskParked(t *testing.T) {
	status, events, _ := runApprovalScenario(t, 60_000, func(t *testing.T, backend *dapr.Backend, instanceID string) {
		if err := backend.RaiseApproval(context.Background(), instanceID, durability.ApprovalDecision{Approved: false, Reason: "not yet"}); err != nil {
			t.Fatalf("RaiseApproval: %v", err)
		}
	})
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 || status.Result.Tasks[0].Status != taskstate.StatusParked {
		t.Fatalf("status = %+v, want parked task after denial", status)
	}
	if got := approvalOutcome(events); got != durability.ApprovalDenied {
		t.Fatalf("approval outcome = %q, want denied", got)
	}
}

// TestDaprBackend_ApprovalTimeoutParks: no decision → the durable timer
// fires and the task parks exactly as it would without the wait.
func TestDaprBackend_ApprovalTimeoutParks(t *testing.T) {
	status, events, _ := runApprovalScenario(t, 900, nil)
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 || status.Result.Tasks[0].Status != taskstate.StatusParked {
		t.Fatalf("status = %+v, want parked task after timeout", status)
	}
	if got := approvalOutcome(events); got != durability.ApprovalTimedOut {
		t.Fatalf("approval outcome = %q, want timed_out", got)
	}
}

// TestDaprBackend_FansOutClaimIndependentTasks is the Phase 2 fan-out
// acceptance: two tasks with disjoint workspace claims run their child
// workflows concurrently. The barrier engine deadlocks under sequential
// scheduling, so completion within the deadline proves the fan-out.
func TestDaprBackend_FansOutClaimIndependentTasks(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine := newBarrierEngine()
	planner := staticPlanner{specs: []goalloop.TaskSpec{
		{Title: "port pkg/a", Priority: 1, Claims: []string{"pkg/a"}},
		{Title: "port pkg/b", Priority: 2, Claims: []string{"pkg/b"}},
	}}
	loop, ev, _, intake, specs := newIntegrationGoal(t, ctx, engine, "parallel goal", planner)
	completed, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, MediaType: "text/plain", InlineBody: []byte("fan-out completion evidence")})
	if err != nil {
		t.Fatalf("store completion evidence: %v", err)
	}
	engine.completedEvidenceID = completed.ID

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, goalrunner.New(loop, intake.Goal, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, durability.GoalStart{RunID: intake.RunID, MaxParallel: 2})
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 2 {
		t.Fatalf("status = %+v, want two completed tasks", status)
	}
	for _, task := range status.Result.Tasks {
		if task.Status != taskstate.StatusCompleted {
			t.Fatalf("task outcome = %+v, want completed", task)
		}
	}
}
