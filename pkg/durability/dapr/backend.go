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
	"strings"

	"github.com/dapr/durabletask-go/api"
	"github.com/dapr/durabletask-go/workflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
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
	TaskWorkflowV1             = "buckley.task.v1"
	TaskWorkflowV2             = "buckley.task.v2"
	ActivityResumeSeed         = "buckley.resume_seed.v1"
	ActivityNextTask           = "buckley.next_task.v1"
	ActivityNextBatch          = "buckley.next_batch.v1"
	ActivityRunTurn            = "buckley.run_turn.v1"
	ActivityRecordApprovalWait = "buckley.record_approval_wait.v1"
	ActivityResolveApproval    = "buckley.resolve_approval.v1"
)

// DefaultEndpoint is the conventional local Dapr gRPC endpoint.
const DefaultEndpoint = "localhost:50001"

// Backend implements durability.Backend over a Dapr sidecar.
type Backend struct {
	endpoint string
	conn     *grpc.ClientConn
	client   *workflow.Client
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

// New connects a Backend to the sidecar endpoint. The connection is
// lazy; Health performs the first real round trip.
func New(endpoint string) (*Backend, error) {
	endpoint = ResolveEndpoint(endpoint)
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dapr: connect %s: %w", endpoint, err)
	}
	return &Backend{endpoint: endpoint, conn: conn, client: workflow.NewClient(conn)}, nil
}

// Health checks the sidecar with the standard gRPC health protocol. An
// Unimplemented response still proves the endpoint answers gRPC.
func (b *Backend) Health(ctx context.Context) error {
	_, err := healthpb.NewHealthClient(b.conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err == nil || status.Code(err) == codes.Unimplemented {
		return nil
	}
	return fmt.Errorf("dapr: sidecar %s is unreachable: %w (start it with `dapr run` or set execution.dapr_grpc_endpoint)", b.endpoint, err)
}

// StartWorker registers the Buckley workflows and activities and starts
// the work-item listener. It returns once the listener is running.
func (b *Backend) StartWorker(ctx context.Context, runner durability.TaskRunner) error {
	registry := workflow.NewRegistry()
	// V1 stays registered so in-flight instances keep their original
	// orchestration semantics (spec versioning rule); new goals start V2.
	if err := registry.AddWorkflowN(GoalWorkflowV1, goalWorkflow); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV1, err)
	}
	if err := registry.AddWorkflowN(GoalWorkflowV2, goalWorkflowV2); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV2, err)
	}
	if err := registry.AddWorkflowN(GoalWorkflowV3, goalWorkflowV3); err != nil {
		return fmt.Errorf("dapr: register %s: %w", GoalWorkflowV3, err)
	}
	if err := registry.AddWorkflowN(TaskWorkflowV1, taskWorkflow); err != nil {
		return fmt.Errorf("dapr: register %s: %w", TaskWorkflowV1, err)
	}
	if err := registry.AddWorkflowN(TaskWorkflowV2, taskWorkflowV2); err != nil {
		return fmt.Errorf("dapr: register %s: %w", TaskWorkflowV2, err)
	}
	activities := map[string]workflow.Activity{
		ActivityResumeSeed:         resumeSeedActivity(runner),
		ActivityNextTask:           nextTaskActivity(runner),
		ActivityNextBatch:          nextBatchActivity(runner),
		ActivityRunTurn:            runTurnActivity(runner),
		ActivityRecordApprovalWait: recordApprovalWaitActivity(runner),
		ActivityResolveApproval:    resolveApprovalActivity(runner),
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

// InstanceIDForRun is the stable one-workflow-per-goal identity.
func InstanceIDForRun(runID string) string {
	return "goal-" + runID
}

// StartGoal schedules the goal workflow, or attaches to the running
// instance when a prior `goal run` already scheduled it.
func (b *Backend) StartGoal(ctx context.Context, start durability.GoalStart) (string, error) {
	instanceID := InstanceIDForRun(start.RunID)
	metadata, err := b.client.FetchWorkflowMetadata(ctx, instanceID)
	switch {
	case err == nil:
		switch metadata.RuntimeStatus {
		case api.RUNTIME_STATUS_RUNNING, api.RUNTIME_STATUS_PENDING, api.RUNTIME_STATUS_SUSPENDED:
			return instanceID, nil
		default:
			return "", fmt.Errorf("dapr: workflow %s already finished with status %s; purge it or start a new run for a rerun generation", instanceID, runtimeStatusName(metadata))
		}
	case errors.Is(err, api.ErrInstanceNotFound):
		// Fresh goal: schedule below.
	default:
		return "", fmt.Errorf("dapr: inspect workflow %s: %w", instanceID, err)
	}

	if _, err := b.client.ScheduleWorkflow(ctx, GoalWorkflowV3,
		workflow.WithInstanceID(instanceID),
		workflow.WithInput(start),
	); err != nil {
		return "", fmt.Errorf("dapr: schedule %s: %w", GoalWorkflowV3, err)
	}
	return instanceID, nil
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
	if output := metadata.Output.GetValue(); output != "" {
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
