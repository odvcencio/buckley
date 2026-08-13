package dapr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dapr/durabletask-go/api"
	"github.com/dapr/durabletask-go/api/protos"
	"github.com/dapr/durabletask-go/workflow"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/goalloop"
)

type versionedTurnRunner struct {
	minimalTaskRunner
	boundCalls  int
	legacyCalls int
}

type minimalTaskRunner struct {
	runCalls int
}

func (*minimalTaskRunner) ResumeSeed(context.Context, string, string) (durability.ResumeSeed, error) {
	return durability.ResumeSeed{}, nil
}

func (*minimalTaskRunner) NextTask(context.Context, durability.NextTaskRequest) (durability.NextTaskResponse, error) {
	return durability.NextTaskResponse{Done: true}, nil
}

func (*minimalTaskRunner) NextBatch(context.Context, durability.NextBatchRequest) (durability.NextBatchResponse, error) {
	return durability.NextBatchResponse{Done: true}, nil
}

func (r *minimalTaskRunner) RunTurn(context.Context, durability.TurnRequest) (durability.TurnResponse, error) {
	r.runCalls++
	return durability.TurnResponse{}, nil
}

func (*minimalTaskRunner) RecordApprovalWait(context.Context, durability.ApprovalWait) error {
	return nil
}

func (*minimalTaskRunner) ResolveApproval(context.Context, durability.ApprovalResolution) error {
	return nil
}

var _ durability.TaskRunner = (*minimalTaskRunner)(nil)

type fakeChildTask struct {
	calls   int
	outcome durability.TaskOutcome
	err     error
}

func (t *fakeChildTask) Await(value any) error {
	t.calls++
	if t.err != nil {
		return t.err
	}
	outcome, ok := value.(*durability.TaskOutcome)
	if !ok {
		return fmt.Errorf("unexpected await target %T", value)
	}
	*outcome = t.outcome
	return nil
}

type stubWorkflowClient struct {
	metadata      *workflow.WorkflowMetadata
	fetchErr      error
	scheduleCalls int
}

func goalMetadata(t *testing.T, instanceID string, status api.OrchestrationStatus, start durability.GoalStart, result durability.GoalResult) *workflow.WorkflowMetadata {
	t.Helper()
	input, err := json.Marshal(start)
	if err != nil {
		t.Fatalf("marshal goal start: %v", err)
	}
	output, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal goal result: %v", err)
	}
	return &workflow.WorkflowMetadata{
		InstanceId:    instanceID,
		Name:          GoalWorkflowV4,
		RuntimeStatus: status,
		Input:         wrapperspb.String(string(input)),
		Output:        wrapperspb.String(string(output)),
	}
}

type generationWorkflowClient struct {
	mu                sync.Mutex
	instances         map[string]*workflow.WorkflowMetadata
	scheduleAttempts  int
	scheduleSuccesses int
	fastComplete      bool
	candidateFetches  atomic.Int32
	fetchBarrier      chan struct{}
	wantFetches       int32
}

type ambiguousScheduleClient struct {
	candidate    *workflow.WorkflowMetadata
	fetches      int
	honorContext bool
}

func (*ambiguousScheduleClient) StartWorker(context.Context, *workflow.Registry) error { return nil }
func (c *ambiguousScheduleClient) FetchWorkflowMetadata(ctx context.Context, _ string, _ ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error) {
	c.fetches++
	if c.fetches == 1 {
		return nil, api.ErrInstanceNotFound
	}
	if c.honorContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return c.candidate, nil
}
func (*ambiguousScheduleClient) ScheduleWorkflow(context.Context, string, ...workflow.NewWorkflowOptions) (string, error) {
	return "", fmt.Errorf("ambiguous transport failure")
}
func (*ambiguousScheduleClient) WaitForWorkflowCompletion(context.Context, string, ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error) {
	return nil, fmt.Errorf("unexpected wait")
}
func (*ambiguousScheduleClient) RaiseEvent(context.Context, string, string, ...workflow.RaiseEventOptions) error {
	return nil
}

func (c *generationWorkflowClient) StartWorker(context.Context, *workflow.Registry) error { return nil }

func (c *generationWorkflowClient) FetchWorkflowMetadata(ctx context.Context, instanceID string, _ ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error) {
	if c.fetchBarrier != nil && strings.HasSuffix(instanceID, "::resume::1") {
		if c.candidateFetches.Add(1) == c.wantFetches {
			close(c.fetchBarrier)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.fetchBarrier:
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	metadata, ok := c.instances[instanceID]
	if !ok {
		return nil, api.ErrInstanceNotFound
	}
	return metadata, nil
}

func (c *generationWorkflowClient) ScheduleWorkflow(_ context.Context, name string, opts ...workflow.NewWorkflowOptions) (string, error) {
	req := &protos.CreateInstanceRequest{Name: name}
	for _, opt := range opts {
		if err := api.NewWorkflowOptions(opt)(req); err != nil {
			return "", err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scheduleAttempts++
	if _, exists := c.instances[req.InstanceId]; exists {
		return "", api.ErrDuplicateInstance
	}
	status := api.RUNTIME_STATUS_PENDING
	result := durability.GoalResult{}
	if c.fastComplete {
		status = api.RUNTIME_STATUS_COMPLETED
		result = durability.GoalResult{Status: durability.GoalResultIncomplete, DeferredTasks: []string{"task-1"}}
	}
	c.instances[req.InstanceId] = &workflow.WorkflowMetadata{
		InstanceId:    req.InstanceId,
		Name:          req.Name,
		RuntimeStatus: status,
		Input:         req.Input,
	}
	if status == api.RUNTIME_STATUS_COMPLETED {
		output, _ := json.Marshal(result)
		c.instances[req.InstanceId].Output = wrapperspb.String(string(output))
	}
	c.scheduleSuccesses++
	return req.InstanceId, nil
}

func (c *generationWorkflowClient) WaitForWorkflowCompletion(context.Context, string, ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error) {
	return nil, fmt.Errorf("unexpected wait")
}

func (c *generationWorkflowClient) RaiseEvent(context.Context, string, string, ...workflow.RaiseEventOptions) error {
	return nil
}

func (c *stubWorkflowClient) StartWorker(context.Context, *workflow.Registry) error {
	return nil
}

func (c *stubWorkflowClient) FetchWorkflowMetadata(context.Context, string, ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error) {
	return c.metadata, c.fetchErr
}

func (c *stubWorkflowClient) ScheduleWorkflow(context.Context, string, ...workflow.NewWorkflowOptions) (string, error) {
	c.scheduleCalls++
	return "scheduled", nil
}

func (c *stubWorkflowClient) WaitForWorkflowCompletion(context.Context, string, ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error) {
	return c.metadata, c.fetchErr
}

func (c *stubWorkflowClient) RaiseEvent(context.Context, string, string, ...workflow.RaiseEventOptions) error {
	return nil
}

type legacyWorkflowRunner struct {
	mu          sync.Mutex
	done        bool
	boundCalls  int
	legacyCalls int
}

func (r *legacyWorkflowRunner) ResumeSeed(context.Context, string, string) (durability.ResumeSeed, error) {
	return durability.ResumeSeed{}, nil
}

func (r *legacyWorkflowRunner) NextTask(context.Context, durability.NextTaskRequest) (durability.NextTaskResponse, error) {
	return durability.NextTaskResponse{}, fmt.Errorf("unexpected legacy NextTask call")
}

func (r *legacyWorkflowRunner) NextBatch(context.Context, durability.NextBatchRequest) (durability.NextBatchResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return durability.NextBatchResponse{Done: true}, nil
	}
	return durability.NextBatchResponse{Tasks: []durability.TaskClaim{{TaskID: "legacy-task"}}}, nil
}

func (r *legacyWorkflowRunner) RunTurn(context.Context, durability.TurnRequest) (durability.TurnResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.boundCalls++
	return durability.TurnResponse{}, fmt.Errorf("legacy workflow used workspace-bound activity")
}

func (r *legacyWorkflowRunner) RunLegacyTurn(context.Context, durability.TurnRequest) (durability.TurnResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.legacyCalls++
	r.done = true
	return durability.TurnResponse{Kind: string(goalloop.StepCompleted), Status: "completed"}, nil
}

func (r *legacyWorkflowRunner) RecordApprovalWait(context.Context, durability.ApprovalWait) error {
	return nil
}

func (r *legacyWorkflowRunner) ResolveApproval(context.Context, durability.ApprovalResolution) error {
	return nil
}

func (r *legacyWorkflowRunner) FinalizeGoal(context.Context, durability.GoalFinalization) error {
	return nil
}

func (r *versionedTurnRunner) RunTurn(context.Context, durability.TurnRequest) (durability.TurnResponse, error) {
	r.boundCalls++
	return durability.TurnResponse{}, nil
}

func (r *versionedTurnRunner) RunLegacyTurn(context.Context, durability.TurnRequest) (durability.TurnResponse, error) {
	r.legacyCalls++
	return durability.TurnResponse{}, nil
}

func TestResolveEndpoint_Precedence(t *testing.T) {
	t.Setenv("DAPR_GRPC_ENDPOINT", "remote:4001")
	t.Setenv("DAPR_GRPC_PORT", "4002")
	if got := ResolveEndpoint("explicit:4000"); got != "explicit:4000" {
		t.Fatalf("explicit endpoint = %s", got)
	}
	if got := ResolveEndpoint(""); got != "remote:4001" {
		t.Fatalf("env endpoint = %s", got)
	}
	t.Setenv("DAPR_GRPC_ENDPOINT", "")
	if got := ResolveEndpoint(""); got != "localhost:4002" {
		t.Fatalf("port endpoint = %s", got)
	}
	t.Setenv("DAPR_GRPC_PORT", "")
	if got := ResolveEndpoint(""); got != DefaultEndpoint {
		t.Fatalf("default endpoint = %s", got)
	}
}

func TestStartGoal_RequiresWorkspaceBinding(t *testing.T) {
	client := &stubWorkflowClient{fetchErr: api.ErrInstanceNotFound}
	backend := &Backend{client: client}

	_, err := backend.StartGoal(context.Background(), durability.GoalStart{RunID: "run-test"})
	if err == nil || !strings.Contains(err.Error(), "workspace root is required") {
		t.Fatalf("StartGoal error = %v, want missing workspace binding", err)
	}
	if client.scheduleCalls != 0 {
		t.Fatalf("schedule calls = %d, want no unbound V4 schedule", client.scheduleCalls)
	}
}

func TestStartGoal_LegacyAttachDoesNotRequireWorkspaceBinding(t *testing.T) {
	client := &stubWorkflowClient{metadata: &workflow.WorkflowMetadata{RuntimeStatus: api.RUNTIME_STATUS_RUNNING}}
	backend := &Backend{client: client}

	instanceID, err := backend.StartGoal(context.Background(), durability.GoalStart{RunID: "run-legacy"})
	if err != nil {
		t.Fatalf("StartGoal legacy attach: %v", err)
	}
	if instanceID != InstanceIDForRun("run-legacy") {
		t.Fatalf("instance ID = %q, want %q", instanceID, InstanceIDForRun("run-legacy"))
	}
	if client.scheduleCalls != 0 {
		t.Fatalf("schedule calls = %d, want attach without a new schedule", client.scheduleCalls)
	}
}

func TestStartGoal_TerminalInstanceAttachesForReconciliationWithoutRescheduling(t *testing.T) {
	for _, status := range []api.OrchestrationStatus{
		api.RUNTIME_STATUS_COMPLETED,
		api.RUNTIME_STATUS_FAILED,
		api.RUNTIME_STATUS_CANCELED,
		api.RUNTIME_STATUS_TERMINATED,
	} {
		t.Run(status.String(), func(t *testing.T) {
			client := &stubWorkflowClient{metadata: &workflow.WorkflowMetadata{RuntimeStatus: status}}
			backend := &Backend{client: client}

			instanceID, err := backend.StartGoal(context.Background(), durability.GoalStart{
				RunID:         "run-terminal",
				WorkspaceRoot: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("StartGoal terminal attach: %v", err)
			}
			if instanceID != InstanceIDForRun("run-terminal") {
				t.Fatalf("instance ID = %q, want existing terminal identity", instanceID)
			}
			if client.scheduleCalls != 0 {
				t.Fatalf("schedule calls = %d, want observation-only terminal attach", client.scheduleCalls)
			}
		})
	}
}

func TestStartGoal_RefusesUnsupportedExistingStateWithoutRescheduling(t *testing.T) {
	client := &stubWorkflowClient{metadata: &workflow.WorkflowMetadata{RuntimeStatus: api.RUNTIME_STATUS_CONTINUED_AS_NEW}}
	backend := &Backend{client: client}

	_, err := backend.StartGoal(context.Background(), durability.GoalStart{
		RunID:         "run-existing",
		WorkspaceRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to schedule over an existing instance") {
		t.Fatalf("StartGoal error = %v, want existing-state refusal", err)
	}
	if client.scheduleCalls != 0 {
		t.Fatalf("schedule calls = %d, want no replacement execution", client.scheduleCalls)
	}
}

func TestStartGoal_IncompleteV4StartsNextGenerationWithCanonicalInput(t *testing.T) {
	canonical := durability.GoalStart{
		RunID:          "run-resume",
		WorkspaceRoot:  t.TempDir(),
		MaxYields:      3,
		MaxParallel:    4,
		ApprovalWaitMS: 9000,
	}
	root := InstanceIDForRun(canonical.RunID)
	client := &generationWorkflowClient{instances: map[string]*workflow.WorkflowMetadata{
		root: goalMetadata(t, root, api.RUNTIME_STATUS_COMPLETED, canonical, durability.GoalResult{
			Status:        durability.GoalResultIncomplete,
			DeferredTasks: []string{"task-1"},
		}),
	}}
	backend := &Backend{client: client}

	// Rerun-only CLI flags differ, but the next generation must inherit the
	// immutable generation-zero input.
	requested := canonical
	requested.MaxYields = 99
	requested.MaxParallel = 1
	requested.ApprovalWaitMS = 1
	requested.ResumeAfterWorkflowInstanceID = root
	instanceID, err := backend.StartGoal(context.Background(), requested)
	if err != nil {
		t.Fatalf("StartGoal resume: %v", err)
	}
	wantID := InstanceIDForRunGeneration(canonical.RunID, 1)
	if instanceID != wantID {
		t.Fatalf("instance ID = %q, want %q", instanceID, wantID)
	}
	client.mu.Lock()
	generated := client.instances[wantID]
	client.mu.Unlock()
	stored, err := decodeGoalStart(generated, wantID)
	if err != nil {
		t.Fatalf("decode generated input: %v", err)
	}
	if !sameGoalStart(stored, canonical) {
		t.Fatalf("generated start = %+v, want canonical %+v", stored, canonical)
	}
}

func TestStartGoal_IncompleteV4WithoutLedgerFenceIsObservationOnly(t *testing.T) {
	start := durability.GoalStart{RunID: "run-unfenced", WorkspaceRoot: t.TempDir()}
	root := InstanceIDForRun(start.RunID)
	client := &generationWorkflowClient{instances: map[string]*workflow.WorkflowMetadata{
		root: goalMetadata(t, root, api.RUNTIME_STATUS_COMPLETED, start, durability.GoalResult{
			Status:        durability.GoalResultIncomplete,
			DeferredTasks: []string{"task-1"},
		}),
	}}
	backend := &Backend{client: client}

	instanceID, err := backend.StartGoal(context.Background(), start)
	if err != nil {
		t.Fatalf("StartGoal unfenced observation: %v", err)
	}
	if instanceID != root {
		t.Fatalf("instance ID = %q, want root observation %q", instanceID, root)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.scheduleAttempts != 0 {
		t.Fatalf("schedule attempts = %d, want no unfenced resume", client.scheduleAttempts)
	}
}

func TestStartGoal_IncompleteFenceAttachesWhilePredecessorIsActive(t *testing.T) {
	start := durability.GoalStart{RunID: "run-finalizing", WorkspaceRoot: t.TempDir(), MaxParallel: 4}
	root := InstanceIDForRun(start.RunID)
	start.ResumeAfterWorkflowInstanceID = root
	metadata := goalMetadata(t, root, api.RUNTIME_STATUS_RUNNING, start, durability.GoalResult{})
	metadata.Output = nil
	client := &generationWorkflowClient{instances: map[string]*workflow.WorkflowMetadata{root: metadata}}
	backend := &Backend{client: client}

	instanceID, err := backend.StartGoal(context.Background(), start)
	if err != nil {
		t.Fatalf("StartGoal active fence attach: %v", err)
	}
	if instanceID != root {
		t.Fatalf("instance ID = %q, want active predecessor %q", instanceID, root)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.scheduleAttempts != 0 {
		t.Fatalf("schedule attempts = %d, want none before predecessor completes", client.scheduleAttempts)
	}
}

func TestStartGoal_ConcurrentFastCompletionCreatesExactlyOneGeneration(t *testing.T) {
	const callers = 32
	start := durability.GoalStart{RunID: "run-race", WorkspaceRoot: t.TempDir(), MaxYields: 2, MaxParallel: 8}
	root := InstanceIDForRun(start.RunID)
	client := &generationWorkflowClient{
		instances: map[string]*workflow.WorkflowMetadata{
			root: goalMetadata(t, root, api.RUNTIME_STATUS_COMPLETED, start, durability.GoalResult{Status: durability.GoalResultIncomplete}),
		},
		fastComplete: true,
		fetchBarrier: make(chan struct{}),
		wantFetches:  callers,
	}
	start.ResumeAfterWorkflowInstanceID = root
	backend := &Backend{client: client}
	wantID := InstanceIDForRunGeneration(start.RunID, 1)

	ready := make(chan struct{})
	results := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			instanceID, err := backend.StartGoal(context.Background(), start)
			if err != nil {
				errs <- err
				return
			}
			results <- instanceID
		}()
	}
	close(ready)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent StartGoal: %v", err)
	}
	for instanceID := range results {
		if instanceID != wantID {
			t.Fatalf("concurrent instance ID = %q, want %q", instanceID, wantID)
		}
	}
	client.mu.Lock()
	if client.scheduleSuccesses != 1 {
		t.Fatalf("schedule successes = %d, want exactly 1", client.scheduleSuccesses)
	}
	if _, cascaded := client.instances[InstanceIDForRunGeneration(start.RunID, 2)]; cascaded {
		t.Fatal("concurrent callers cascaded into a second resume generation")
	}
	client.fetchBarrier = nil
	client.mu.Unlock()

	// A later invocation may observe generation one as incomplete and create
	// exactly the next generation.
	start.ResumeAfterWorkflowInstanceID = wantID
	nextID, err := backend.StartGoal(context.Background(), start)
	if err != nil {
		t.Fatalf("later StartGoal: %v", err)
	}
	if want := InstanceIDForRunGeneration(start.RunID, 2); nextID != want {
		t.Fatalf("later instance ID = %q, want %q", nextID, want)
	}
}

func TestStartGoal_AmbiguousScheduleRequiresExactV4Candidate(t *testing.T) {
	start := durability.GoalStart{RunID: "run-ambiguous", WorkspaceRoot: t.TempDir()}
	root := InstanceIDForRun(start.RunID)
	client := &ambiguousScheduleClient{
		candidate: &workflow.WorkflowMetadata{
			InstanceId:    root,
			Name:          GoalWorkflowV3,
			RuntimeStatus: api.RUNTIME_STATUS_PENDING,
		},
	}
	backend := &Backend{client: client}

	_, err := backend.StartGoal(context.Background(), start)
	var stateErr *WorkflowStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("StartGoal error = %v, want WorkflowStateError", err)
	}
}

func TestStartGoal_AmbiguousScheduleReconcilesAfterParentCancellation(t *testing.T) {
	start := durability.GoalStart{RunID: "run-canceled-reconcile", WorkspaceRoot: t.TempDir()}
	root := InstanceIDForRun(start.RunID)
	client := &ambiguousScheduleClient{
		candidate:    goalMetadata(t, root, api.RUNTIME_STATUS_PENDING, start, durability.GoalResult{}),
		honorContext: true,
	}
	backend := &Backend{client: client}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	instanceID, err := backend.StartGoal(ctx, start)
	if err != nil {
		t.Fatalf("StartGoal canceled reconciliation: %v", err)
	}
	if instanceID != root {
		t.Fatalf("instance ID = %q, want reconciled candidate %q", instanceID, root)
	}
}

func TestStartGoal_AmbiguousScheduleCompletedV4OutputFailsClosed(t *testing.T) {
	start := durability.GoalStart{RunID: "run-ambiguous-output", WorkspaceRoot: t.TempDir()}
	root := InstanceIDForRun(start.RunID)
	candidate := goalMetadata(t, root, api.RUNTIME_STATUS_COMPLETED, start, durability.GoalResult{})
	candidate.Output = wrapperspb.String(`{"tasks":[],"status":"future"}`)
	backend := &Backend{client: &ambiguousScheduleClient{candidate: candidate}}

	_, err := backend.StartGoal(context.Background(), start)
	var stateErr *WorkflowStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("StartGoal error = %v, want WorkflowStateError", err)
	}
}

func TestStartGoal_CompletedV4OutputFailsClosed(t *testing.T) {
	start := durability.GoalStart{RunID: "run-output", WorkspaceRoot: t.TempDir()}
	root := InstanceIDForRun(start.RunID)
	cases := []struct {
		name   string
		output *wrapperspb.StringValue
	}{
		{name: "missing"},
		{name: "malformed", output: wrapperspb.String("{")},
		{name: "unknown status", output: wrapperspb.String(`{"tasks":[],"status":"future"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := goalMetadata(t, root, api.RUNTIME_STATUS_COMPLETED, start, durability.GoalResult{})
			metadata.Output = tc.output
			backend := &Backend{client: &stubWorkflowClient{metadata: metadata}}
			_, err := backend.StartGoal(context.Background(), start)
			var stateErr *WorkflowStateError
			if !errors.As(err, &stateErr) {
				t.Fatalf("StartGoal error = %v, want WorkflowStateError", err)
			}
		})
	}
}

func TestWaitForGoal_CompletedV4OutputFailsClosed(t *testing.T) {
	start := durability.GoalStart{RunID: "run-wait-output", WorkspaceRoot: t.TempDir()}
	root := InstanceIDForRun(start.RunID)
	cases := []struct {
		name   string
		output *wrapperspb.StringValue
	}{
		{name: "missing"},
		{name: "malformed", output: wrapperspb.String("{")},
		{name: "unknown status", output: wrapperspb.String(`{"tasks":[],"status":"future"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := goalMetadata(t, root, api.RUNTIME_STATUS_COMPLETED, start, durability.GoalResult{})
			metadata.Output = tc.output
			backend := &Backend{client: &stubWorkflowClient{metadata: metadata}}
			_, err := backend.WaitForGoal(context.Background(), root)
			var stateErr *WorkflowStateError
			if !errors.As(err, &stateErr) {
				t.Fatalf("WaitForGoal error = %v, want WorkflowStateError", err)
			}
		})
	}
}

func TestWaitForGoal_CompletedV4AcceptsTerminalStatuses(t *testing.T) {
	start := durability.GoalStart{RunID: "run-wait-valid", WorkspaceRoot: t.TempDir()}
	root := InstanceIDForRun(start.RunID)
	for _, status := range []string{"", durability.GoalResultIncomplete} {
		t.Run(status, func(t *testing.T) {
			metadata := goalMetadata(t, root, api.RUNTIME_STATUS_COMPLETED, start, durability.GoalResult{Status: status})
			backend := &Backend{client: &stubWorkflowClient{metadata: metadata}}
			got, err := backend.WaitForGoal(context.Background(), root)
			if err != nil {
				t.Fatalf("WaitForGoal: %v", err)
			}
			if got.Result.Status != status {
				t.Fatalf("result status = %q, want %q", got.Result.Status, status)
			}
		})
	}
}

func TestSelectTurnActivity_VersionsKeepLegacyTrustNarrow(t *testing.T) {
	runner := &versionedTurnRunner{}
	if _, err := selectTurnActivity(runner, true)(context.Background(), durability.TurnRequest{}); err != nil {
		t.Fatalf("legacy invocation: %v", err)
	}
	if _, err := selectTurnActivity(runner, false)(context.Background(), durability.TurnRequest{}); err != nil {
		t.Fatalf("bound invocation: %v", err)
	}
	if runner.legacyCalls != 1 || runner.boundCalls != 1 {
		t.Fatalf("calls = legacy:%d bound:%d, want one each", runner.legacyCalls, runner.boundCalls)
	}
}

func TestPreV4TaskRunner_RegistersAndUsesCompatibilityFallbacks(t *testing.T) {
	runner := &minimalTaskRunner{}
	backend := &Backend{client: &stubWorkflowClient{}}
	if err := backend.StartWorker(context.Background(), runner); err != nil {
		t.Fatalf("StartWorker with pre-V4 TaskRunner: %v", err)
	}

	if _, err := selectTurnActivity(runner, true)(context.Background(), durability.TurnRequest{}); err != nil {
		t.Fatalf("legacy RunTurn fallback: %v", err)
	}
	if runner.runCalls != 1 {
		t.Fatalf("RunTurn calls = %d, want legacy request routed to base capability", runner.runCalls)
	}

	err := finalizeGoal(context.Background(), runner, durability.GoalFinalization{RunID: "run-pre-v4"})
	if err == nil || !strings.Contains(err.Error(), "does not support V4 goal finalization") {
		t.Fatalf("finalizeGoal error = %v, want explicit unsupported capability", err)
	}
}

func TestDaprBackend_LegacyWorkflowUsesLegacyTurnAdapter(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BUCKLEY_DAPR_TEST_ENDPOINT"))
	if endpoint == "" {
		t.Skip("BUCKLEY_DAPR_TEST_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	runner := &legacyWorkflowRunner{}
	backend, err := New(endpoint)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.StartWorker(ctx, runner); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID := fmt.Sprintf("legacy-%d", time.Now().UnixNano())
	if _, err := backend.client.ScheduleWorkflow(ctx, GoalWorkflowV3,
		workflow.WithInstanceID(instanceID),
		workflow.WithInput(durability.GoalStart{RunID: "legacy-run"}),
	); err != nil {
		t.Fatalf("ScheduleWorkflow: %v", err)
	}
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" {
		t.Fatalf("runtime status = %s, want COMPLETED", status.RuntimeStatus)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.legacyCalls != 1 || runner.boundCalls != 0 {
		t.Fatalf("turn calls = legacy:%d bound:%d, want legacy:1 bound:0", runner.legacyCalls, runner.boundCalls)
	}
}

func TestNextTurnIdentity(t *testing.T) {
	cases := []struct {
		kind           goalloop.StepKind
		gen, idx       int
		wantGen, wanti int
	}{
		{goalloop.StepContinue, 0, 3, 0, 4},
		{goalloop.StepVerify, 1, 0, 1, 1},
		{goalloop.StepCheckpoint, 1, 5, 2, 0},
	}
	for _, tc := range cases {
		gen, idx := nextTurnIdentity(tc.kind, tc.gen, tc.idx)
		if gen != tc.wantGen || idx != tc.wanti {
			t.Fatalf("nextTurnIdentity(%s, %d, %d) = (%d, %d), want (%d, %d)", tc.kind, tc.gen, tc.idx, gen, idx, tc.wantGen, tc.wanti)
		}
	}
}

func TestTurnDone(t *testing.T) {
	running := []goalloop.StepKind{goalloop.StepContinue, goalloop.StepVerify, goalloop.StepCheckpoint}
	for _, kind := range running {
		if turnDone(kind) {
			t.Fatalf("turnDone(%s) = true, want false", kind)
		}
	}
	done := []goalloop.StepKind{goalloop.StepCompleted, goalloop.StepBlocked, goalloop.StepPark, goalloop.StepYield}
	for _, kind := range done {
		if !turnDone(kind) {
			t.Fatalf("turnDone(%s) = false, want true", kind)
		}
	}
}

func TestDeferredTasks_SortedAndBounded(t *testing.T) {
	yields := map[string]int{"task-c": 2, "task-a": 3, "task-b": 1}
	got := deferredTasks(yields, 2)
	if len(got) != 2 || got[0] != "task-a" || got[1] != "task-c" {
		t.Fatalf("deferredTasks = %v, want [task-a task-c]", got)
	}
}

func TestAwaitScheduledChildren_V4WaitsForEverySiblingAfterFailure(t *testing.T) {
	first := &fakeChildTask{err: fmt.Errorf("first failed")}
	second := &fakeChildTask{outcome: durability.TaskOutcome{Status: "completed"}}
	third := &fakeChildTask{err: fmt.Errorf("third failed")}

	outcomes, err := awaitScheduledChildren([]scheduledChild{
		{taskID: "task-a", task: first},
		{taskID: "task-b", task: second},
		{taskID: "task-c", task: third},
	}, true)
	if err == nil || !strings.Contains(err.Error(), "task workflow task-a") || !strings.Contains(err.Error(), "task workflow task-c") {
		t.Fatalf("fan-in error = %v, want both child failures", err)
	}
	if first.calls != 1 || second.calls != 1 || third.calls != 1 {
		t.Fatalf("await calls = [%d %d %d], want every scheduled child awaited", first.calls, second.calls, third.calls)
	}
	if len(outcomes) != 1 || outcomes[0].TaskID != "task-b" || outcomes[0].Status != "completed" {
		t.Fatalf("successful sibling outcomes = %+v, want task-b preserved", outcomes)
	}
}

func TestAwaitScheduledChildren_LegacyModeRemainsFailFast(t *testing.T) {
	first := &fakeChildTask{err: fmt.Errorf("first failed")}
	second := &fakeChildTask{outcome: durability.TaskOutcome{TaskID: "task-b", Status: "completed"}}

	_, err := awaitScheduledChildren([]scheduledChild{
		{taskID: "task-a", task: first},
		{taskID: "task-b", task: second},
	}, false)
	if err == nil {
		t.Fatal("legacy fan-in returned no error")
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("legacy await calls = [%d %d], want fail-fast compatibility", first.calls, second.calls)
	}
}

func TestMarkDeferredGoalResult_IsExplicitAndDeterministic(t *testing.T) {
	result := markDeferredGoalResult(durability.GoalResult{}, map[string]int{
		"task-c": 2,
		"task-a": 3,
		"task-b": 1,
	}, 2)
	if result.Status != durability.GoalResultIncomplete {
		t.Fatalf("status = %q, want %q", result.Status, durability.GoalResultIncomplete)
	}
	if len(result.DeferredTasks) != 2 || result.DeferredTasks[0] != "task-a" || result.DeferredTasks[1] != "task-c" {
		t.Fatalf("deferred tasks = %v, want [task-a task-c]", result.DeferredTasks)
	}

	terminal := markDeferredGoalResult(durability.GoalResult{}, map[string]int{"task-a": 1}, 2)
	if terminal.Status != "" || len(terminal.DeferredTasks) != 0 {
		t.Fatalf("terminal result = %+v, want no incomplete marker", terminal)
	}
}
