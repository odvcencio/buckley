package dapr_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dapr/durabletask-go/workflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/durability/dapr"
	"m31labs.dev/buckley/pkg/durability/goalrunner"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/replay"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// restartEngine drives the kill/restart acceptance: turn 000 checkpoints and
// turn 001 completes. restartReceiptSink holds the second receipt delivery
// after its canonical append so the test can kill the worker in the precise
// post-checkpoint/pre-activity-ack window.
type restartEngine struct {
	mu    sync.Mutex
	calls map[string]int
	// completedEvidenceID must reference a real stored object so replay
	// verification can resolve the completion claim.
	completedEvidenceID string
}

func newRestartEngine() *restartEngine {
	return &restartEngine{
		calls: map[string]int{},
	}
}

func (e *restartEngine) RunTurn(_ context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.mu.Lock()
	e.calls[task.TurnID]++
	evidenceID := e.completedEvidenceID
	e.mu.Unlock()

	if strings.HasSuffix(task.TurnID, "/turn-000") {
		return goalloop.TurnOutcome{Rounds: 1, StateChanged: true, Summary: "first turn"}, nil
	}
	return goalloop.TurnOutcome{Rounds: 1, Completed: true, CompletedEvidenceID: evidenceID, Summary: "done"}, nil
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

type restartReceiptSink struct {
	mu      sync.Mutex
	calls   int
	receipt chan struct{}
	blocked bool
}

func newRestartReceiptSink() *restartReceiptSink {
	return &restartReceiptSink{receipt: make(chan struct{})}
}

func (s *restartReceiptSink) WriteEvent(ctx context.Context, event runledger.Event) error {
	if event.Type != runledger.EventDurableTurn {
		return nil
	}
	s.mu.Lock()
	s.calls++
	shouldBlock := s.calls == 2 && !s.blocked
	if shouldBlock {
		s.blocked = true
		close(s.receipt)
	}
	s.mu.Unlock()
	if !shouldBlock {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
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

// resumableGenerationEngine forces one bounded-yield generation, then
// completes the same canonical task when a later generation resumes it.
type resumableGenerationEngine struct {
	mu                  sync.Mutex
	calls               []goalloop.TaskContext
	completedEvidenceID string
}

// retryTimerEngine blocks its first logical turn on a future provider retry,
// then completes from a fresh turn identity after the durable timer wakes it.
type retryTimerEngine struct {
	mu                  sync.Mutex
	calls               []goalloop.TaskContext
	retryDelay          time.Duration
	retryAt             time.Time
	completedEvidenceID string
}

type alwaysPastRetryEngine struct {
	mu    sync.Mutex
	calls []goalloop.TaskContext
}

func (e *alwaysPastRetryEngine) RunTurn(_ context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.mu.Lock()
	e.calls = append(e.calls, task)
	e.mu.Unlock()
	retryAt := time.Now().UTC().Add(-time.Hour)
	return goalloop.TurnOutcome{
		Rounds: 1,
		Blocker: &taskstate.Blocker{
			Reason:     "provider rate limit",
			RetryAfter: &retryAt,
		},
		Summary: "provider deadline stayed stale",
	}, nil
}

func (e *alwaysPastRetryEngine) recordedCalls() []goalloop.TaskContext {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]goalloop.TaskContext(nil), e.calls...)
}

func (e *retryTimerEngine) RunTurn(_ context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.mu.Lock()
	e.calls = append(e.calls, task)
	call := len(e.calls)
	retryDelay := e.retryDelay
	evidenceID := e.completedEvidenceID
	e.mu.Unlock()
	if call == 1 {
		retryAt := time.Now().UTC().Add(retryDelay)
		e.mu.Lock()
		e.retryAt = retryAt
		e.mu.Unlock()
		return goalloop.TurnOutcome{
			Rounds:  1,
			Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &retryAt},
			Summary: "waiting for provider capacity",
		}, nil
	}
	return goalloop.TurnOutcome{
		Rounds:              1,
		Completed:           true,
		CompletedEvidenceID: evidenceID,
		Summary:             "completed after durable retry",
	}, nil
}

func (e *retryTimerEngine) recordedCalls() []goalloop.TaskContext {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]goalloop.TaskContext(nil), e.calls...)
}

func (e *retryTimerEngine) retryDeadline() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.retryAt
}

func (e *resumableGenerationEngine) RunTurn(_ context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.mu.Lock()
	e.calls = append(e.calls, task)
	call := len(e.calls)
	evidenceID := e.completedEvidenceID
	e.mu.Unlock()
	if call == 1 {
		return goalloop.TurnOutcome{Rounds: 1, StateChanged: true, Summary: "checkpoint for next generation"}, nil
	}
	return goalloop.TurnOutcome{Rounds: 1, Completed: true, CompletedEvidenceID: evidenceID, Summary: "resumed generation completed"}, nil
}

func (e *resumableGenerationEngine) recordedCalls() []goalloop.TaskContext {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]goalloop.TaskContext(nil), e.calls...)
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
	return newIntegrationGoalWithProgress(t, ctx, engine, statement, planner, nil)
}

func newIntegrationGoalWithProgress(t *testing.T, ctx context.Context, engine goalloop.TurnEngine, statement string, planner goalloop.Planner, progress *agentloop.ProgressController) (*goalloop.Loop, *evidence.SQLiteStore, *runledger.SQLiteStore, *goalloop.Intake, map[string]goalloop.TaskSpec) {
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
		Progress:    progress,
		SessionID:   "dapr-it",
	})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	intake, err := loop.Start(ctx, goalloop.Goal{Statement: statement, WorkspaceRoot: dir})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	specs := make(map[string]goalloop.TaskSpec, len(intake.Tasks))
	for _, task := range intake.Tasks {
		specs[task.TaskID] = task.Spec
	}
	return loop, ev, ledger, intake, specs
}

func newIntegrationRunner(t *testing.T, loop *goalloop.Loop, intake *goalloop.Intake, specs map[string]goalloop.TaskSpec) *goalrunner.Runner {
	t.Helper()
	runner, err := goalrunner.New(loop, intake.RunID, intake.Goal.WorkspaceRoot, intake.Goal, specs)
	if err != nil {
		t.Fatalf("goalrunner.New: %v", err)
	}
	return runner
}

func integrationGoalStart(intake *goalloop.Intake) durability.GoalStart {
	return durability.GoalStart{RunID: intake.RunID, WorkspaceRoot: intake.Goal.WorkspaceRoot}
}

func waitForIntegrationEvents(t *testing.T, ctx context.Context, ledger *runledger.SQLiteStore, runID string, eventTypes []string, want int) []runledger.Event {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID, Types: eventTypes})
		if err != nil {
			t.Fatalf("ListEvents(%v): %v", eventTypes, err)
		}
		if len(events) >= want {
			return events
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %d events of types %v: %v", want, eventTypes, ctx.Err())
		case <-ticker.C:
		}
	}
}

func newIntegrationWorkflowClient(t *testing.T, endpoint string) (*workflow.Client, func()) {
	t.Helper()
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			ctx = metadata.AppendToOutgoingContext(ctx, "dapr-app-id", dapr.DefaultAppID)
			return invoker(ctx, method, req, reply, cc, opts...)
		}),
	)
	if err != nil {
		t.Fatalf("connect workflow client: %v", err)
	}
	return workflow.NewClient(conn), func() { _ = conn.Close() }
}

func terminateIntegrationWorkflow(t *testing.T, ctx context.Context, endpoint, instanceID string) {
	t.Helper()
	client, closeClient := newIntegrationWorkflowClient(t, endpoint)
	defer closeClient()
	if err := client.TerminateWorkflow(ctx, instanceID); err != nil {
		t.Fatalf("TerminateWorkflow(%s): %v", instanceID, err)
	}
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
	loop, _, ledger, intake, specs := newIntegrationGoal(t, ctx, &completingEngine{}, "integration goal", nil)

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, integrationGoalStart(intake))
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
	run, err := ledger.GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "completed" || run.EndedAt == nil {
		t.Fatalf("run = %+v, want canonical completed lifecycle", run)
	}
	report, err := loop.Report(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Status != run.Status {
		t.Fatalf("report status = %s, run status = %s", report.Status, run.Status)
	}
}

// TestDaprBackend_IncompleteGenerationResumesAndCompletes exercises the real
// V4 production boundary: generation zero reaches its yield ceiling, records
// an immutable incomplete fence without sealing the canonical run, and a
// later invocation resumes exactly generation one to complete the same task.
func TestDaprBackend_IncompleteGenerationResumesAndCompletes(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine := &resumableGenerationEngine{}
	progress := &agentloop.ProgressController{
		Mode:  agentloop.ModeDynamic,
		Fuses: agentloop.Fuses{ModelRequests: 1},
	}
	loop, ev, ledger, intake, specs := newIntegrationGoalWithProgress(t, ctx, engine, "resumable generation goal", nil, progress)
	completed, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, MediaType: "text/plain", InlineBody: []byte("resumed generation completion evidence")})
	if err != nil {
		t.Fatalf("store completion evidence: %v", err)
	}
	engine.completedEvidenceID = completed.ID

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}

	start := integrationGoalStart(intake)
	start.MaxYields = 1
	rootID, err := backend.StartGoal(ctx, start)
	if err != nil {
		t.Fatalf("StartGoal generation zero: %v", err)
	}
	if want := dapr.InstanceIDForRun(intake.RunID); rootID != want {
		t.Fatalf("generation zero ID = %q, want %q", rootID, want)
	}
	rootStatus, err := backend.WaitForGoal(ctx, rootID)
	if err != nil {
		t.Fatalf("WaitForGoal generation zero: %v", err)
	}
	if rootStatus.RuntimeStatus != "COMPLETED" || rootStatus.Result.Status != durability.GoalResultIncomplete {
		t.Fatalf("generation zero status = %+v, want explicit incomplete completion", rootStatus)
	}
	if len(rootStatus.Result.DeferredTasks) != 1 || rootStatus.Result.DeferredTasks[0] != intake.Tasks[0].TaskID {
		t.Fatalf("generation zero deferred tasks = %v, want [%s]", rootStatus.Result.DeferredTasks, intake.Tasks[0].TaskID)
	}
	run, err := ledger.GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun after generation zero: %v", err)
	}
	if run.EndedAt != nil {
		t.Fatalf("generation zero sealed canonical run as %s", run.Status)
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableGoalGeneration}})
	if err != nil {
		t.Fatalf("ListEvents generation zero: %v", err)
	}
	if len(events) != 1 || events[0].Payload["incomplete"] != true {
		t.Fatalf("generation zero events = %+v, want one incomplete fence", events)
	}
	fence, _ := events[0].Payload["workflow_instance_id"].(string)
	if fence != rootID {
		t.Fatalf("generation zero fence = %q, want %q", fence, rootID)
	}

	start.ResumeAfterWorkflowInstanceID = fence
	resumedID, err := backend.StartGoal(ctx, start)
	if err != nil {
		t.Fatalf("StartGoal generation one: %v", err)
	}
	if want := dapr.InstanceIDForRunGeneration(intake.RunID, 1); resumedID != want {
		t.Fatalf("generation one ID = %q, want %q", resumedID, want)
	}
	resumedStatus, err := backend.WaitForGoal(ctx, resumedID)
	if err != nil {
		t.Fatalf("WaitForGoal generation one: %v", err)
	}
	if resumedStatus.RuntimeStatus != "COMPLETED" || resumedStatus.Result.Status != "" || len(resumedStatus.Result.Tasks) != 1 || resumedStatus.Result.Tasks[0].Status != taskstate.StatusCompleted {
		t.Fatalf("generation one status = %+v, want canonical task completion", resumedStatus)
	}

	// Reusing the same immutable fence observes generation one; it cannot
	// consume another generation even after the candidate completed quickly.
	attachedID, err := backend.StartGoal(ctx, start)
	if err != nil {
		t.Fatalf("StartGoal repeated fence: %v", err)
	}
	if attachedID != resumedID {
		t.Fatalf("repeated fence attached %q, want %q", attachedID, resumedID)
	}

	run, err = ledger.GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun after generation one: %v", err)
	}
	if run.Status != "completed" || run.EndedAt == nil {
		t.Fatalf("canonical run = %+v, want completed lifecycle", run)
	}
	events, err = ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableGoalGeneration}})
	if err != nil {
		t.Fatalf("ListEvents after generation one: %v", err)
	}
	if len(events) != 2 || events[1].Payload["workflow_instance_id"] != resumedID || events[1].Payload["incomplete"] != false {
		t.Fatalf("generation events = %+v, want incomplete root then completed generation one", events)
	}

	calls := engine.recordedCalls()
	if len(calls) != 2 {
		t.Fatalf("engine calls = %d, want one turn per generation", len(calls))
	}
	for i, call := range calls {
		if call.RunID != intake.RunID || call.TaskID != intake.Tasks[0].TaskID || call.Goal.WorkspaceRoot != intake.Goal.WorkspaceRoot {
			t.Fatalf("engine call %d identity = run:%q task:%q workspace:%q", i, call.RunID, call.TaskID, call.Goal.WorkspaceRoot)
		}
	}
	if calls[0].TurnID == calls[1].TurnID {
		t.Fatalf("resumed turn reused identity %q", calls[0].TurnID)
	}
}

// TestDaprBackend_WorkerRestartResumesWithoutReexecution is the Phase 1
// acceptance from spec.durable-execution-dapr: kill the worker after a
// completed turn's checkpoint and canonical receipt land but before the
// activity acknowledges, then reconcile it without re-executing the engine.
// Replay verification must agree afterward.
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
	sink := newRestartReceiptSink()
	ledger.SetRalphSink(sink)
	runner := newIntegrationRunner(t, loop, intake, specs)

	first, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New first: %v", err)
	}
	workerCtx, killWorker := context.WithCancel(ctx)
	if err := first.StartWorker(workerCtx, runner); err != nil {
		killWorker()
		t.Fatalf("StartWorker first: %v", err)
	}
	instanceID, err := first.StartGoal(ctx, integrationGoalStart(intake))
	if err != nil {
		killWorker()
		t.Fatalf("StartGoal: %v", err)
	}

	// The second durable.turn is already canonical when the secondary sink
	// blocks. Kill the listener before that activity can acknowledge it.
	select {
	case <-sink.receipt:
	case <-ctx.Done():
		killWorker()
		t.Fatal("turn 001 receipt never reached the post-checkpoint ack window")
	}
	killWorker()
	_ = first.Close()

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
	attachedID, err := second.StartGoal(ctx, integrationGoalStart(intake))
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
	if calls := engine.turnCalls("/turn-001"); calls != 1 {
		t.Fatalf("turn 001 executed %d times, want exactly 1 across receipt redelivery", calls)
	}

	// The durable audit agrees: replay verification passes on the run.
	report, err := replay.Verify(ctx, ledger, ledger, ev, intake.RunID)
	if err != nil {
		t.Fatalf("replay.Verify: %v", err)
	}
	if !report.Valid {
		t.Fatalf("replay report = %+v, want valid", report)
	}
	if report.RunStatus != "completed" {
		t.Fatalf("replay run status = %s, want completed", report.RunStatus)
	}
}

// TestDaprBackend_BlockedTimerSurvivesWorkerRestart proves the V5/V4 retry
// path against the standalone taskhub: the top workflow remains live while no
// worker owns it, the durable timer survives, and readmission opens one fresh
// turn after a replacement worker starts.
func TestDaprBackend_BlockedTimerSurvivesWorkerRestart(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := &retryTimerEngine{retryDelay: 3 * time.Second}
	loop, ev, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "blocked timer restart goal", nil)
	completed, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, MediaType: "text/plain", InlineBody: []byte("blocked timer completion evidence")})
	if err != nil {
		t.Fatalf("store completion evidence: %v", err)
	}
	engine.completedEvidenceID = completed.ID
	runner := newIntegrationRunner(t, loop, intake, specs)

	first, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New first: %v", err)
	}
	workerCtx, killWorker := context.WithCancel(ctx)
	if err := first.StartWorker(workerCtx, runner); err != nil {
		killWorker()
		t.Fatalf("StartWorker first: %v", err)
	}
	instanceID, err := first.StartGoal(ctx, integrationGoalStart(intake))
	if err != nil {
		killWorker()
		t.Fatalf("StartGoal: %v", err)
	}
	waiting := waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableRetryWaiting}, 1)
	if calls := engine.recordedCalls(); len(calls) != 1 {
		t.Fatalf("engine calls while timer pending = %d, want 1 blocked turn", len(calls))
	}
	client, closeClient := newIntegrationWorkflowClient(t, endpoint)
	metadataBeforeRestart, err := client.FetchWorkflowMetadata(ctx, instanceID)
	closeClient()
	if err != nil {
		t.Fatalf("FetchWorkflowMetadata before restart: %v", err)
	}
	if got := strings.TrimPrefix(metadataBeforeRestart.RuntimeStatus.String(), "ORCHESTRATION_STATUS_"); got != "RUNNING" {
		t.Fatalf("workflow status while timer pending = %s, want RUNNING", got)
	}

	// Stop the first listener after the waiting audit lands and before expiry.
	if deadline := engine.retryDeadline(); deadline.IsZero() || !time.Now().Before(deadline) {
		t.Fatalf("retry deadline = %v, want a future deadline before worker restart", deadline)
	}
	killWorker()
	_ = first.Close()

	second, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.StartWorker(ctx, runner); err != nil {
		t.Fatalf("StartWorker second: %v", err)
	}
	attachedID, err := second.StartGoal(ctx, integrationGoalStart(intake))
	if err != nil {
		t.Fatalf("StartGoal attach: %v", err)
	}
	if attachedID != instanceID {
		t.Fatalf("attached instance = %s, want %s", attachedID, instanceID)
	}
	status, err := second.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 || status.Result.Tasks[0].Status != taskstate.StatusCompleted {
		t.Fatalf("status = %+v, want one completed task after retry wake", status)
	}

	calls := engine.recordedCalls()
	if len(calls) != 2 {
		t.Fatalf("engine calls = %d, want exactly blocked then completed", len(calls))
	}
	if calls[0].TurnID == calls[1].TurnID {
		t.Fatalf("retry wake reused turn identity %q", calls[0].TurnID)
	}
	resolved := waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableRetryResolved}, 1)
	turns := waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableTurn}, 2)
	if len(waiting) != 1 || len(resolved) != 1 || len(turns) != 2 {
		t.Fatalf("audit counts waiting=%d resolved=%d turns=%d, want 1/1/2", len(waiting), len(resolved), len(turns))
	}
	if waiting[0].Payload["wait_id"] != resolved[0].Payload["wait_id"] {
		t.Fatalf("wait identity mismatch waiting=%v resolved=%v", waiting[0].Payload["wait_id"], resolved[0].Payload["wait_id"])
	}
	checkpoint, err := ledger.LatestTaskCheckpoint(ctx, intake.Tasks[0].TaskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if checkpoint.Version != 4 || checkpoint.Status != taskstate.StatusCompleted {
		t.Fatalf("latest checkpoint = version:%d status:%s, want intake, one block, one wake, one completion", checkpoint.Version, checkpoint.Status)
	}
	report, err := loop.Report(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Status != "completed" {
		t.Fatalf("report status = %s, want completed", report.Status)
	}
	replayReport, err := replay.Verify(ctx, ledger, ledger, ev, intake.RunID)
	if err != nil {
		t.Fatalf("replay.Verify: %v", err)
	}
	if !replayReport.Valid || replayReport.RunStatus != "completed" {
		t.Fatalf("replay report = %+v, want valid completed run", replayReport)
	}
}

func TestDaprBackend_StaleWakeDoesNotResolveOrRunAnotherTurn(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "newer-blocker"
		if terminal {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			endpoint := integrationEndpoint(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			engine := &retryTimerEngine{retryDelay: 2 * time.Second}
			loop, ev, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "stale retry wake "+name, nil)
			backend, err := dapr.New(endpoint)
			if err != nil {
				t.Fatalf("dapr.New: %v", err)
			}
			t.Cleanup(func() { _ = backend.Close() })
			if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
				t.Fatalf("StartWorker: %v", err)
			}
			instanceID, err := backend.StartGoal(ctx, integrationGoalStart(intake))
			if err != nil {
				t.Fatalf("StartGoal: %v", err)
			}
			waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableRetryWaiting}, 1)

			checkpoints, err := taskstate.NewManager(ledger, ev)
			if err != nil {
				t.Fatalf("taskstate.NewManager: %v", err)
			}
			taskID := intake.Tasks[0].TaskID
			resumed, err := checkpoints.Resume(ctx, taskID)
			if err != nil {
				t.Fatalf("Resume blocked checkpoint: %v", err)
			}
			reason := taskstate.TriggerBlocker
			if terminal {
				completion, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindCheckpoint, MediaType: "text/plain", InlineBody: []byte("external terminal completion")})
				if err != nil {
					t.Fatalf("completion evidence: %v", err)
				}
				resumed.State.Status = taskstate.StatusCompleted
				resumed.State.Blocker = nil
				resumed.State.Completed = []taskstate.CompletedItem{{Text: "externally completed", EvidenceID: completion.ID}}
				reason = taskstate.TriggerDecisionRecorded
			} else {
				resumed.State.Status = taskstate.StatusBlocked
				past := time.Now().UTC().Add(-time.Hour)
				resumed.State.Blocker = &taskstate.Blocker{Reason: "newer dependency blocker", RetryAfter: &past}
			}
			resumed.State.UpdatedAt = time.Now().UTC()
			if _, err := checkpoints.Save(ctx, taskstate.SaveInput{
				State: resumed.State, Reason: reason, SessionID: "dapr-it", RunID: intake.RunID,
			}); err != nil {
				t.Fatalf("save newer checkpoint: %v", err)
			}

			status, err := backend.WaitForGoal(ctx, instanceID)
			if err != nil {
				t.Fatalf("WaitForGoal: %v", err)
			}
			if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 {
				t.Fatalf("status = %+v", status)
			}
			wantTaskStatus := taskstate.StatusBlocked
			wantGoalStatus := durability.GoalResultIncomplete
			if terminal {
				wantTaskStatus = taskstate.StatusCompleted
				wantGoalStatus = ""
			}
			if status.Result.Tasks[0].Status != wantTaskStatus || status.Result.Status != wantGoalStatus {
				t.Fatalf("stale wake result = %+v, want task:%s goal:%q", status.Result, wantTaskStatus, wantGoalStatus)
			}
			if status.Result.Tasks[0].GenerationDeferred != !terminal {
				t.Fatalf("generation-deferred marker = %v, want %v", status.Result.Tasks[0].GenerationDeferred, !terminal)
			}
			if calls := engine.recordedCalls(); len(calls) != 1 {
				t.Fatalf("engine calls after stale wake = %d, want 1", len(calls))
			}
			resolved, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableRetryResolved}})
			if err != nil {
				t.Fatalf("ListEvents resolved: %v", err)
			}
			if len(resolved) != 0 {
				t.Fatalf("stale wake resolved events = %d, want 0", len(resolved))
			}
			checkpoint, err := ledger.LatestTaskCheckpoint(ctx, taskID)
			if err != nil {
				t.Fatalf("LatestTaskCheckpoint: %v", err)
			}
			if checkpoint.Version != 3 || checkpoint.Status != wantTaskStatus {
				t.Fatalf("stale wake checkpoint = version:%d status:%s", checkpoint.Version, checkpoint.Status)
			}
		})
	}
}

func TestDaprBackend_DeferredRetryDoesNotEnterApprovalProtocol(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := &retryTimerEngine{retryDelay: 2 * time.Second}
	loop, ev, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "superseded retry stays parked", nil)
	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	start := integrationGoalStart(intake)
	start.ApprovalWaitMS = 60_000
	instanceID, err := backend.StartGoal(ctx, start)
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableRetryWaiting}, 1)

	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	resumed, err := checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume blocked checkpoint: %v", err)
	}
	resumed.State.Status = taskstate.StatusParked
	resumed.State.Blocker = &taskstate.Blocker{Reason: "operator approval superseded provider retry"}
	resumed.State.UpdatedAt = time.Now().UTC()
	parked, err := checkpoints.Save(ctx, taskstate.SaveInput{
		State: resumed.State, Reason: taskstate.TriggerBlocker, SessionID: "dapr-it", RunID: intake.RunID,
	})
	if err != nil {
		t.Fatalf("save newer parked checkpoint: %v", err)
	}

	childInstanceID := instanceID + "::" + taskID + "::1"
	if err := backend.RaiseApproval(ctx, childInstanceID, durability.ApprovalDecision{Approved: true, Reason: "queued approval must be ignored"}); err != nil {
		t.Fatalf("RaiseApproval: %v", err)
	}
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || status.Result.Status != durability.GoalResultIncomplete || len(status.Result.Tasks) != 1 {
		t.Fatalf("status = %+v, want one incomplete deferred child", status)
	}
	outcome := status.Result.Tasks[0]
	if outcome.Status != taskstate.StatusParked || !outcome.GenerationDeferred || outcome.RetryExhausted {
		t.Fatalf("task outcome = %+v, want parked generation defer", outcome)
	}
	if calls := engine.recordedCalls(); len(calls) != 1 {
		t.Fatalf("engine calls = %d, want one", len(calls))
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, event := range events {
		switch event.Type {
		case runledger.EventDurableRetryResolved, runledger.EventDurableApprovalWaiting, runledger.EventDurableApprovalResolved:
			t.Fatalf("deferred retry emitted forbidden event: %+v", event)
		}
	}
	latest, err := ledger.LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if latest.CheckpointID != parked.CheckpointID || latest.Version != parked.Version || latest.Status != taskstate.StatusParked || latest.StateJSON != parked.StateJSON {
		t.Fatalf("parked checkpoint changed: got %+v, want %+v", latest, parked)
	}
}

func TestDaprBackend_StaleReplacementCannotResetConsumedRetryBudget(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := &alwaysPastRetryEngine{}
	loop, ev, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "superseded retry budget", nil)
	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, integrationGoalStart(intake))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableRetryWaiting}, 3)

	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	resumed, err := checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume third blocked checkpoint: %v", err)
	}
	past := time.Now().UTC().Add(-2 * time.Hour)
	resumed.State.Status = taskstate.StatusBlocked
	resumed.State.Blocker = &taskstate.Blocker{Reason: "superseding database dependency", RetryAfter: &past}
	resumed.State.UpdatedAt = time.Now().UTC()
	if _, err := checkpoints.Save(ctx, taskstate.SaveInput{
		State: resumed.State, Reason: taskstate.TriggerBlocker, SessionID: "dapr-it", RunID: intake.RunID,
	}); err != nil {
		t.Fatalf("save superseding blocker: %v", err)
	}

	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || status.Result.Status != durability.GoalResultIncomplete || len(status.Result.Tasks) != 1 {
		t.Fatalf("status = %+v, want one deferred child", status)
	}
	if !status.Result.Tasks[0].GenerationDeferred || status.Result.Tasks[0].RetryExhausted {
		t.Fatalf("task outcome = %+v, want superseded generation defer", status.Result.Tasks[0])
	}
	if calls := engine.recordedCalls(); len(calls) != 3 {
		t.Fatalf("engine calls = %d, want three consumed child turns", len(calls))
	}
	waiting, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableRetryWaiting}})
	if err != nil {
		t.Fatalf("List waiting: %v", err)
	}
	resolved, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableRetryResolved}})
	if err != nil {
		t.Fatalf("List resolved: %v", err)
	}
	if len(waiting) != 3 || len(resolved) != 2 {
		t.Fatalf("retry audits waiting/resolved = %d/%d, want 3/2", len(waiting), len(resolved))
	}
}

func TestDaprBackend_RetryBudgetIsGenerationWide(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := &alwaysPastRetryEngine{}
	loop, _, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "bounded stale retry deadlines", nil)
	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	defer backend.Close()
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, integrationGoalStart(intake))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || status.Result.Status != durability.GoalResultIncomplete || len(status.Result.Tasks) != 1 {
		t.Fatalf("status = %+v, want one incomplete exhausted child", status)
	}
	outcome := status.Result.Tasks[0]
	if !outcome.RetryExhausted || outcome.Status != taskstate.StatusBlocked || outcome.Turns != defaultExpectedRetryTurns() {
		t.Fatalf("task outcome = %+v, want bounded retry exhaustion", outcome)
	}
	if calls := engine.recordedCalls(); len(calls) != defaultExpectedRetryTurns() {
		t.Fatalf("engine calls = %d, want initial plus three retries", len(calls))
	}
	waiting, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableRetryWaiting}})
	if err != nil {
		t.Fatalf("List waiting: %v", err)
	}
	resolved, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableRetryResolved}})
	if err != nil {
		t.Fatalf("List resolved: %v", err)
	}
	turns, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableTurn}})
	if err != nil {
		t.Fatalf("List turns: %v", err)
	}
	if len(waiting) != 3 || len(resolved) != 3 || len(turns) != 4 {
		t.Fatalf("audit counts waiting/resolved/turns = %d/%d/%d, want 3/3/4", len(waiting), len(resolved), len(turns))
	}
	waitIDs := map[any]struct{}{}
	for _, event := range waiting {
		waitIDs[event.Payload["wait_id"]] = struct{}{}
	}
	if len(waitIDs) != 3 {
		t.Fatalf("retry wait IDs = %v, want three distinct identities", waitIDs)
	}
	checkpoint, err := ledger.LatestTaskCheckpoint(ctx, intake.Tasks[0].TaskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if checkpoint.Version != 8 || checkpoint.Status != taskstate.StatusBlocked {
		t.Fatalf("checkpoint = version:%d status:%s, want four blocks plus three wakes", checkpoint.Version, checkpoint.Status)
	}
}

func defaultExpectedRetryTurns() int { return 4 }

// TestDaprBackend_TerminationDuringRetryTimerDoesNotWake verifies recursive
// termination while the child is sleeping: the timer cannot schedule a wake,
// no fresh model/tool turn runs, and Buckley's canonical run remains unsealed
// for an operator-visible terminated generation.
func TestDaprBackend_TerminationDuringRetryTimerDoesNotWake(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := &retryTimerEngine{retryDelay: 2 * time.Second}
	loop, _, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "cancel blocked timer goal", nil)

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, integrationGoalStart(intake))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	waiting := waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableRetryWaiting}, 1)
	if len(waiting) != 1 {
		t.Fatalf("waiting events = %d, want 1", len(waiting))
	}
	terminateIntegrationWorkflow(t, ctx, endpoint, instanceID)
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal after terminate: %v", err)
	}
	if status.RuntimeStatus != "TERMINATED" {
		t.Fatalf("status = %+v, want TERMINATED generation", status)
	}

	deadline := engine.retryDeadline()
	if deadline.IsZero() {
		t.Fatal("retry deadline was not recorded")
	}
	if delay := time.Until(deadline.Add(500 * time.Millisecond)); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			t.Fatalf("waiting beyond terminated retry deadline: %v", ctx.Err())
		}
	}
	if calls := engine.recordedCalls(); len(calls) != 1 {
		t.Fatalf("engine calls after termination = %d, want only blocked turn", len(calls))
	}
	resolved, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableRetryResolved}})
	if err != nil {
		t.Fatalf("ListEvents resolved: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved events after termination = %d, want 0", len(resolved))
	}
	turns, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableTurn}})
	if err != nil {
		t.Fatalf("ListEvents turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("durable turns after termination = %d, want 1 blocked turn", len(turns))
	}
	checkpoint, err := ledger.LatestTaskCheckpoint(ctx, intake.Tasks[0].TaskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if checkpoint.Version != 2 || checkpoint.Status != taskstate.StatusBlocked {
		t.Fatalf("checkpoint after termination = version:%d status:%s, want intake plus original blocked checkpoint", checkpoint.Version, checkpoint.Status)
	}
	run, err := ledger.GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.EndedAt != nil || run.Status == "completed" || run.Status == "failed" {
		t.Fatalf("terminated generation sealed canonical run: %+v", run)
	}
}

func TestDaprBackend_ForeignOwnedExactSuccessorFailsClosed(t *testing.T) {
	endpoint := integrationEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := &retryTimerEngine{retryDelay: 2 * time.Second}
	loop, ev, ledger, intake, specs := newIntegrationGoal(t, ctx, engine, "foreign retry successor", nil)
	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, integrationGoalStart(intake))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	waiting := waitForIntegrationEvents(t, ctx, ledger, intake.RunID, []string{runledger.EventDurableRetryWaiting}, 1)
	waitID, _ := waiting[0].Payload["wait_id"].(string)
	if waitID == "" {
		t.Fatalf("waiting event has no wait ID: %+v", waiting[0].Payload)
	}
	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	resumed, err := checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume blocked checkpoint: %v", err)
	}
	resumed.State.Status = taskstate.StatusInProgress
	resumed.State.Blocker = nil
	resumed.State.UpdatedAt = time.Now().UTC()
	if _, err := checkpoints.Save(ctx, taskstate.SaveInput{
		State:  resumed.State,
		Reason: taskstate.TriggerReason("durable_retry_wake:" + waitID),
		// Geometry looks exactly like an acknowledged wake successor, but
		// its session ownership is foreign and must be rejected first.
		SessionID: "foreign-session",
		RunID:     intake.RunID,
	}); err != nil {
		t.Fatalf("save foreign exact successor: %v", err)
	}

	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "FAILED" {
		t.Fatalf("status = %+v, want failed foreign-ownership generation", status)
	}
	if calls := engine.recordedCalls(); len(calls) != 1 {
		t.Fatalf("engine calls after foreign successor = %d, want 1", len(calls))
	}
	resolved, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableRetryResolved}})
	if err != nil {
		t.Fatalf("List resolved: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("foreign successor resolved events = %d, want 0", len(resolved))
	}
	decisions, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventControllerDecision}})
	if err != nil {
		t.Fatalf("List decisions: %v", err)
	}
	for _, event := range decisions {
		if event.Payload["wait_id"] == waitID {
			t.Fatalf("foreign successor recorded retry wake: %+v", event)
		}
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
		return goalrunner.New(loop, runID, goal.WorkspaceRoot, goal, specs)
	})

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, resolver); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, integrationGoalStart(intake))
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
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	start := integrationGoalStart(intake)
	start.ApprovalWaitMS = approvalWaitMS
	instanceID, err := backend.StartGoal(ctx, start)
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
	if err := backend.StartWorker(ctx, newIntegrationRunner(t, loop, intake, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	start := integrationGoalStart(intake)
	start.MaxParallel = 2
	instanceID, err := backend.StartGoal(ctx, start)
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
