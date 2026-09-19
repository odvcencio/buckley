// Package dapr adapts the durability ports to the Dapr workflow runtime
// through the durabletask-go SDK (spec.durable-execution-dapr, Phase 1).
// Workflow code in this package is deterministic orchestration only:
// activities own every Buckley side effect through durability.TaskRunner.
package dapr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dapr/durabletask-go/api"
	"github.com/dapr/durabletask-go/workflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"m31labs.dev/buckley/pkg/durability"
)

// Registered workflow and activity names. Existing instances keep their
// orchestration semantics; behavior changes require new versioned names
// (spec.durable-execution-dapr, versioning rule).
const (
	GoalWorkflowV1             = "buckley.goal.v1"
	GoalWorkflowV2             = "buckley.goal.v2"
	GoalWorkflowV3             = "buckley.goal.v3"
	GoalWorkflowV4             = "buckley.goal.v4"
	GoalWorkflowV5             = "buckley.goal.v5"
	TaskWorkflowV1             = "buckley.task.v1"
	TaskWorkflowV2             = "buckley.task.v2"
	TaskWorkflowV3             = "buckley.task.v3"
	TaskWorkflowV4             = "buckley.task.v4"
	ActivityResumeSeed         = "buckley.resume_seed.v1"
	ActivityNextTask           = "buckley.next_task.v1"
	ActivityNextBatch          = "buckley.next_batch.v1"
	ActivityNextBatchV2        = "buckley.next_batch.v2"
	ActivityRunTurn            = "buckley.run_turn.v1"
	ActivityRunTurnV2          = "buckley.run_turn.v2"
	ActivityRunTurnV3          = "buckley.run_turn.v3"
	ActivityRecordApprovalWait = "buckley.record_approval_wait.v1"
	ActivityResolveApproval    = "buckley.resolve_approval.v1"
	ActivityFinalizeGoal       = "buckley.finalize_goal.v1"
	ActivityRecordRetryWaiting = "buckley.record_retry_waiting.v1"
	ActivityWakeRetry          = "buckley.wake_retry.v1"
	ActivityWakeRetryV2        = "buckley.wake_retry.v2"
	ActivityResolveRetry       = "buckley.resolve_retry.v1"
)

// DefaultEndpoint is the conventional local Dapr gRPC endpoint.
const DefaultEndpoint = "localhost:50001"

const scheduleReconcileTimeout = 5 * time.Second

// Backend implements durability.Backend over a Dapr sidecar.
type Backend struct {
	endpoint string
	conn     *grpc.ClientConn
	client   workflowClient
}

type workflowClient interface {
	StartWorker(context.Context, *workflow.Registry) error
	FetchWorkflowMetadata(context.Context, string, ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error)
	ScheduleWorkflow(context.Context, string, ...workflow.NewWorkflowOptions) (string, error)
	WaitForWorkflowCompletion(context.Context, string, ...workflow.FetchWorkflowMetadataOptions) (*workflow.WorkflowMetadata, error)
	RaiseEvent(context.Context, string, string, ...workflow.RaiseEventOptions) error
}

// ResolveEndpoint picks the sidecar gRPC endpoint: the explicit
// configuration value, then DAPR_GRPC_ENDPOINT, then DAPR_GRPC_PORT on
// localhost, then the conventional default.
func ResolveEndpoint(configured string) string {
	if endpoint := strings.TrimSpace(configured); endpoint != "" {
		return endpoint
	}
	if endpoint := strings.TrimSpace(os.Getenv("DAPR_GRPC_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	if port := strings.TrimSpace(os.Getenv("DAPR_GRPC_PORT")); port != "" {
		return "localhost:" + port
	}
	return DefaultEndpoint
}

// DefaultAppID is the dapr-app-id the backend presents when none is
// configured. A real sidecar proxies workflow calls by this metadata
// and rejects requests without it; it must match the sidecar's own
// --app-id. The emulator ignores it.
const DefaultAppID = "buckley"

// ResolveAppID picks the app ID: the explicit value, then DAPR_APP_ID,
// then the default.
func ResolveAppID(configured string) string {
	if appID := strings.TrimSpace(configured); appID != "" {
		return appID
	}
	if appID := strings.TrimSpace(os.Getenv("DAPR_APP_ID")); appID != "" {
		return appID
	}
	return DefaultAppID
}

// New connects a Backend to the sidecar endpoint. The connection is
// lazy; Health performs the first real round trip.
func New(endpoint string) (*Backend, error) {
	endpoint = ResolveEndpoint(endpoint)
	appID := ResolveAppID("")
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(metadata.AppendToOutgoingContext(ctx, "dapr-app-id", appID), method, req, reply, cc, opts...)
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(metadata.AppendToOutgoingContext(ctx, "dapr-app-id", appID), desc, cc, method, opts...)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dapr: connect %s: %w", endpoint, err)
	}
	return &Backend{endpoint: endpoint, conn: conn, client: workflow.NewClient(conn)}, nil
}

// Health probes the workflow API itself: fetching a sentinel instance
// answers fast from both the sidecar and the emulator, and not-found is
// the healthy answer. The generic gRPC health service is unusable here:
// a sidecar proxies it to the (possibly absent) app channel.
func (b *Backend) Health(ctx context.Context) error {
	_, err := b.client.FetchWorkflowMetadata(ctx, "buckley-health-probe")
	switch {
	case err == nil, errors.Is(err, api.ErrInstanceNotFound):
		return nil
	case status.Code(err) == codes.Unimplemented, status.Code(err) == codes.NotFound:
		return nil
	}
	return fmt.Errorf("dapr: sidecar %s is unreachable: %w (start it with `dapr run` or set execution.dapr_grpc_endpoint)", b.endpoint, err)
}

// StartWorker registers the Buckley workflows and activities and starts
// the work-item listener. It returns once the listener is running.
func (b *Backend) StartWorker(ctx context.Context, runner durability.TaskRunner) error {
	registry := workflow.NewRegistry()
	// Prior versions stay registered so in-flight instances keep their
	// orchestration semantics (spec versioning rule); new goals start V5.
	if err := registry.AddWorkflowN(GoalWorkflowV1, goalWorkflow); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV1, err)
	}
	if err := registry.AddWorkflowN(GoalWorkflowV2, goalWorkflowV2); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV2, err)
	}
	if err := registry.AddWorkflowN(GoalWorkflowV3, goalWorkflowV3); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV3, err)
	}
	if err := registry.AddWorkflowN(GoalWorkflowV4, goalWorkflowV4); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV4, err)
	}
	if err := registry.AddWorkflowN(GoalWorkflowV5, goalWorkflowV5); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV5, err)
	}
	if err := registry.AddWorkflowN(TaskWorkflowV1, taskWorkflow); err != nil {
		return fmt.Errorf("dapr: register %s: %w", TaskWorkflowV1, err)
	}
	if err := registry.AddWorkflowN(TaskWorkflowV2, taskWorkflowV2); err != nil {
		return fmt.Errorf("dapr: register %s: %w", TaskWorkflowV2, err)
	}
	if err := registry.AddWorkflowN(TaskWorkflowV3, taskWorkflowV3); err != nil {
		return fmt.Errorf("dapr: register %s: %w", TaskWorkflowV3, err)
	}
	if err := registry.AddWorkflowN(TaskWorkflowV4, taskWorkflowV4); err != nil {
		return fmt.Errorf("dapr: register %s: %w", TaskWorkflowV4, err)
	}
	activities := map[string]workflow.Activity{
		ActivityResumeSeed:         resumeSeedActivity(runner),
		ActivityNextTask:           nextTaskActivity(runner),
		ActivityNextBatch:          nextBatchActivity(runner),
		ActivityNextBatchV2:        nextBatchActivityV2(runner),
		ActivityRunTurn:            runTurnActivity(runner),
		ActivityRunTurnV2:          runTurnActivityV2(runner),
		ActivityRunTurnV3:          runTurnActivityV3(runner),
		ActivityRecordApprovalWait: recordApprovalWaitActivity(runner),
		ActivityResolveApproval:    resolveApprovalActivity(runner),
		ActivityFinalizeGoal:       finalizeGoalActivity(runner),
		ActivityRecordRetryWaiting: recordRetryWaitingActivity(runner),
		ActivityWakeRetry:          wakeRetryActivity(runner),
		ActivityWakeRetryV2:        wakeRetryActivityV2(runner),
		ActivityResolveRetry:       resolveRetryActivity(runner),
	}
	for name, activity := range activities {
		if err := registry.AddActivityN(name, activity); err != nil {
			return fmt.Errorf("dapr: register %s: %w", name, err)
		}
	}
	if err := b.client.StartWorker(ctx, registry); err != nil {
		return fmt.Errorf("dapr: start worker: %w", err)
	}
	return nil
}

// InstanceIDForRun is the stable first workflow instance for a canonical run.
// Resumable generations keep this root and add a deterministic suffix.
func InstanceIDForRun(runID string) string {
	return "goal-" + runID
}

// InstanceIDForRunGeneration returns the deterministic workflow identity for
// one execution generation of a canonical run. Generation zero preserves the
// V1-V4 identity used before resumable generations were introduced.
func InstanceIDForRunGeneration(runID string, generation int) string {
	root := InstanceIDForRun(runID)
	if generation <= 0 {
		return root
	}
	return fmt.Sprintf("%s::resume::%d", root, generation)
}

// WorkflowStateError reports workflow metadata that is not safe to attach to
// or advance. In particular, Buckley never guesses whether a completed V4
// generation was incomplete when its output is missing or malformed.
type WorkflowStateError struct {
	InstanceID string
	Reason     string
}

func (e *WorkflowStateError) Error() string {
	return fmt.Sprintf("dapr: workflow %s has invalid state: %s", e.InstanceID, e.Reason)
}

// StartGoal schedules or attaches to exactly one workflow generation. The
// caller's immutable ResumeAfterWorkflowInstanceID ledger fence selects the
// next generation; without a fence the target is generation zero. A completed
// target is always observation-only for this invocation.
//
// Deterministic generation IDs make the transition atomic at Dapr's unique
// instance boundary. If concurrent callers race to schedule the same missing
// generation, the loser fetches and attaches to that exact candidate instead
// of relying on provider-specific error classification. Only a later
// invocation with a newer immutable ledger fence can select another target.
func (b *Backend) StartGoal(ctx context.Context, start durability.GoalStart) (string, error) {
	if strings.TrimSpace(start.RunID) == "" {
		return "", fmt.Errorf("dapr: goal run ID is required")
	}

	canonical := start
	canonical.ResumeAfterWorkflowInstanceID = ""
	targetGeneration := 0
	if fence := strings.TrimSpace(start.ResumeAfterWorkflowInstanceID); fence != "" {
		fenceGeneration, err := generationForInstanceID(start.RunID, fence)
		if err != nil {
			return "", err
		}
		for generation := 0; generation <= fenceGeneration; generation++ {
			instanceID := InstanceIDForRunGeneration(start.RunID, generation)
			metadata, err := b.client.FetchWorkflowMetadata(ctx, instanceID)
			if err != nil {
				return "", fmt.Errorf("dapr: inspect resume fence workflow %s: %w", instanceID, err)
			}
			inspection, err := inspectGoalGeneration(metadata, instanceID, generation, start, canonical, true, true)
			if err != nil {
				return "", err
			}
			if metadata.RuntimeStatus == api.RUNTIME_STATUS_RUNNING ||
				metadata.RuntimeStatus == api.RUNTIME_STATUS_PENDING ||
				metadata.RuntimeStatus == api.RUNTIME_STATUS_SUSPENDED {
				// The finalization activity can land the immutable ledger fence just
				// before Dapr marks this generation completed. Attach until the
				// runtime exposes its explicit incomplete output.
				return instanceID, nil
			}
			if metadata.RuntimeStatus != api.RUNTIME_STATUS_COMPLETED {
				return "", invalidWorkflowState(instanceID, fmt.Sprintf("resume fence has terminal status %s, want COMPLETED", runtimeStatusName(metadata)))
			}
			if !inspection.incomplete {
				return "", invalidWorkflowState(instanceID, "resume fence generation is not explicitly incomplete")
			}
			if inspection.canonical != nil {
				canonical = *inspection.canonical
				canonical.ResumeAfterWorkflowInstanceID = ""
			}
		}
		targetGeneration = fenceGeneration + 1
	}

	instanceID := InstanceIDForRunGeneration(start.RunID, targetGeneration)
	metadata, err := b.client.FetchWorkflowMetadata(ctx, instanceID)
	if err == nil {
		if _, inspectErr := inspectGoalGeneration(metadata, instanceID, targetGeneration, start, canonical, true, targetGeneration > 0); inspectErr != nil {
			return "", inspectErr
		}
		if !attachableWorkflowStatus(metadata.RuntimeStatus) {
			return "", fmt.Errorf("dapr: workflow %s has unsupported status %s; refusing to schedule over an existing instance", instanceID, runtimeStatusName(metadata))
		}
		return instanceID, nil
	}
	if !errors.Is(err, api.ErrInstanceNotFound) {
		return "", fmt.Errorf("dapr: inspect workflow %s: %w", instanceID, err)
	}
	if strings.TrimSpace(canonical.WorkspaceRoot) == "" {
		return "", fmt.Errorf("dapr: workspace root is required for run %s", start.RunID)
	}
	if _, scheduleErr := b.client.ScheduleWorkflow(ctx, GoalWorkflowV5,
		workflow.WithInstanceID(instanceID),
		workflow.WithInput(canonical),
	); scheduleErr == nil {
		return instanceID, nil
	} else {
		// Schedule errors are ambiguous across Dapr transports: a timeout or
		// duplicate response may still mean the exact target exists.
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scheduleReconcileTimeout)
		metadata, err = b.client.FetchWorkflowMetadata(reconcileCtx, instanceID)
		cancel()
		if errors.Is(err, api.ErrInstanceNotFound) {
			return "", fmt.Errorf("dapr: schedule %s as %s: %w", GoalWorkflowV5, instanceID, scheduleErr)
		}
		if err != nil {
			return "", fmt.Errorf("dapr: reconcile schedule of %s after %v: %w", instanceID, scheduleErr, err)
		}
		if _, inspectErr := inspectGoalGeneration(metadata, instanceID, targetGeneration, start, canonical, true, true); inspectErr != nil {
			return "", inspectErr
		}
		return instanceID, nil
	}
}

func attachableWorkflowStatus(status api.OrchestrationStatus) bool {
	switch status {
	case api.RUNTIME_STATUS_RUNNING,
		api.RUNTIME_STATUS_PENDING,
		api.RUNTIME_STATUS_SUSPENDED,
		api.RUNTIME_STATUS_COMPLETED,
		api.RUNTIME_STATUS_FAILED,
		api.RUNTIME_STATUS_CANCELED,
		api.RUNTIME_STATUS_TERMINATED:
		return true
	default:
		return false
	}
}

func generationForInstanceID(runID, instanceID string) (int, error) {
	root := InstanceIDForRun(runID)
	if instanceID == root {
		return 0, nil
	}
	prefix := root + "::resume::"
	if !strings.HasPrefix(instanceID, prefix) {
		return 0, invalidWorkflowState(instanceID, fmt.Sprintf("resume fence does not belong to run %s", runID))
	}
	raw := strings.TrimPrefix(instanceID, prefix)
	generation, err := strconv.Atoi(raw)
	if err != nil || generation <= 0 || strconv.Itoa(generation) != raw {
		return 0, invalidWorkflowState(instanceID, "resume generation suffix is not a canonical positive integer")
	}
	return generation, nil
}

type goalGenerationInspection struct {
	incomplete bool
	canonical  *durability.GoalStart
}

func inspectGoalGeneration(metadata *workflow.WorkflowMetadata, instanceID string, generation int, requested, canonical durability.GoalStart, classifyOutput, requireV4 bool) (goalGenerationInspection, error) {
	if metadata == nil {
		return goalGenerationInspection{}, invalidWorkflowState(instanceID, "metadata is nil")
	}

	// Generation zero may be an attachable V1-V3 history. Those histories
	// predate resumable V4 output and workspace input, so they remain
	// observation-only and permissive. New generated instances are V5;
	// completed V4 generations remain attachable.
	if metadata.Name != GoalWorkflowV4 && metadata.Name != GoalWorkflowV5 {
		if generation == 0 && !requireV4 {
			return goalGenerationInspection{}, nil
		}
		return goalGenerationInspection{}, invalidWorkflowState(instanceID, fmt.Sprintf("workflow name is %q, want %q or %q", metadata.Name, GoalWorkflowV4, GoalWorkflowV5))
	}

	stored, err := decodeGoalStart(metadata, instanceID)
	if err != nil {
		return goalGenerationInspection{}, err
	}
	if strings.TrimSpace(stored.RunID) != strings.TrimSpace(requested.RunID) {
		return goalGenerationInspection{}, invalidWorkflowState(instanceID, fmt.Sprintf("input run ID is %q, want %q", stored.RunID, requested.RunID))
	}
	if strings.TrimSpace(stored.WorkspaceRoot) != strings.TrimSpace(requested.WorkspaceRoot) {
		return goalGenerationInspection{}, invalidWorkflowState(instanceID, fmt.Sprintf("input workspace is %q, want %q", stored.WorkspaceRoot, requested.WorkspaceRoot))
	}
	if generation > 0 && !sameGoalStart(stored, canonical) {
		return goalGenerationInspection{}, invalidWorkflowState(instanceID, "generated input differs from the canonical generation-zero input")
	}

	inspection := goalGenerationInspection{canonical: &stored}
	if !classifyOutput || metadata.RuntimeStatus != api.RUNTIME_STATUS_COMPLETED {
		return inspection, nil
	}
	result, err := decodeCompletedV4GoalResult(metadata, instanceID)
	if err != nil {
		return goalGenerationInspection{}, err
	}
	if result.Status == durability.GoalResultIncomplete {
		inspection.incomplete = true
	}
	return inspection, nil
}

func decodeCompletedV4GoalResult(metadata *workflow.WorkflowMetadata, instanceID string) (durability.GoalResult, error) {
	if metadata == nil {
		return durability.GoalResult{}, invalidWorkflowState(instanceID, "metadata is nil")
	}
	output := metadata.Output.GetValue()
	if strings.TrimSpace(output) == "" {
		return durability.GoalResult{}, invalidWorkflowState(instanceID, "completed V4 output is missing")
	}
	var result durability.GoalResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return durability.GoalResult{}, invalidWorkflowState(instanceID, fmt.Sprintf("decode completed V4 output: %v", err))
	}
	switch result.Status {
	case "", durability.GoalResultIncomplete:
		return result, nil
	default:
		return durability.GoalResult{}, invalidWorkflowState(instanceID, fmt.Sprintf("completed V4 result status is %q", result.Status))
	}
}

func decodeGoalStart(metadata *workflow.WorkflowMetadata, instanceID string) (durability.GoalStart, error) {
	input := metadata.Input.GetValue()
	if strings.TrimSpace(input) == "" {
		return durability.GoalStart{}, invalidWorkflowState(instanceID, "V4 input is missing")
	}
	var start durability.GoalStart
	if err := json.Unmarshal([]byte(input), &start); err != nil {
		return durability.GoalStart{}, invalidWorkflowState(instanceID, fmt.Sprintf("decode V4 input: %v", err))
	}
	return start, nil
}

func sameGoalStart(left, right durability.GoalStart) bool {
	return strings.TrimSpace(left.RunID) == strings.TrimSpace(right.RunID) &&
		strings.TrimSpace(left.WorkspaceRoot) == strings.TrimSpace(right.WorkspaceRoot) &&
		left.MaxYields == right.MaxYields &&
		left.MaxParallel == right.MaxParallel &&
		left.ApprovalWaitMS == right.ApprovalWaitMS
}

func invalidWorkflowState(instanceID, reason string) error {
	return &WorkflowStateError{InstanceID: instanceID, Reason: reason}
}

// WaitForGoal blocks until the goal workflow reaches a terminal state.
func (b *Backend) WaitForGoal(ctx context.Context, instanceID string) (durability.GoalStatus, error) {
	metadata, err := b.client.WaitForWorkflowCompletion(ctx, instanceID)
	if err != nil {
		return durability.GoalStatus{}, fmt.Errorf("dapr: wait for %s: %w", instanceID, err)
	}
	result := durability.GoalStatus{
		InstanceID:    instanceID,
		RuntimeStatus: runtimeStatusName(metadata),
	}
	if (metadata.Name == GoalWorkflowV4 || metadata.Name == GoalWorkflowV5) && metadata.RuntimeStatus == api.RUNTIME_STATUS_COMPLETED {
		decoded, decodeErr := decodeCompletedV4GoalResult(metadata, instanceID)
		if decodeErr != nil {
			return result, decodeErr
		}
		result.Result = decoded
	} else if output := metadata.Output.GetValue(); output != "" {
		if err := json.Unmarshal([]byte(output), &result.Result); err != nil {
			return result, fmt.Errorf("dapr: decode workflow output: %w", err)
		}
	}
	if failure := metadata.FailureDetails; failure != nil {
		result.Failure = failure.GetErrorMessage()
	}
	return result, nil
}

// RaiseApproval delivers an approval decision to a waiting task
// workflow instance.
func (b *Backend) RaiseApproval(ctx context.Context, workflowInstanceID string, decision durability.ApprovalDecision) error {
	if err := b.client.RaiseEvent(ctx, workflowInstanceID, durability.ApprovalEventName,
		workflow.WithEventPayload(decision),
	); err != nil {
		return fmt.Errorf("dapr: raise approval on %s: %w", workflowInstanceID, err)
	}
	return nil
}

// runtimeStatusName renders the workflow runtime status without copying
// the proto value (it embeds a mutex).
func runtimeStatusName(metadata *workflow.WorkflowMetadata) string {
	return strings.TrimPrefix(metadata.RuntimeStatus.String(), "ORCHESTRATION_STATUS_")
}

// Close releases the sidecar connection.
func (b *Backend) Close() error {
	return b.conn.Close()
}

var _ durability.Backend = (*Backend)(nil)
var _ workflowClient = (*workflow.Client)(nil)
