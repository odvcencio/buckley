package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

const (
	taskVerificationStatusNotRequested = "not_requested"
	taskVerificationStatusPass         = "pass"
	taskVerificationStatusFail         = "fail"
	taskVerificationStatusUnverified   = "unverified"

	taskVerificationEvidenceSchemaV1 = "buckley.task_verification.v1"
)

type taskVerificationTool interface {
	ExecuteWithContext(context.Context, map[string]any) (*builtin.Result, error)
}

type taskVerificationToolFactory func(snapshotRoot, sourceRoot string, timeout time.Duration) (taskVerificationTool, func(), error)

type taskVerificationHost struct {
	makeTool taskVerificationToolFactory
}

func (r *Runner) verifyTaskExecutionRecord(ctx context.Context, plan *orchestrator.Plan, task orchestrator.Task, record *orchestrator.TaskExecutionRecord) error {
	return r.verifyTaskExecutionRecordWithHost(ctx, plan, task, record, taskVerificationHost{makeTool: defaultTaskVerificationTool})
}

func (r *Runner) verifyTaskExecutionRecordWithHost(ctx context.Context, plan *orchestrator.Plan, task orchestrator.Task, record *orchestrator.TaskExecutionRecord, host taskVerificationHost) error {
	status, results, err := r.runTaskVerificationChecks(ctx, plan, task, host)
	if record != nil {
		record.VerificationStatus = status
		record.VerificationResults = append(record.VerificationResults[:0], results...)
	}
	return err
}

func (r *Runner) runTaskVerificationChecks(ctx context.Context, plan *orchestrator.Plan, task orchestrator.Task, host taskVerificationHost) (string, []orchestrator.TaskVerificationResult, error) {
	if len(task.VerificationChecks) == 0 {
		if len(task.Verification) > 0 {
			return taskVerificationStatusUnverified, nil, fmt.Errorf("task %s has legacy verification prose but no structured verification checks", task.ID)
		}
		return taskVerificationStatusNotRequested, nil, nil
	}
	legacyErr := legacyTaskVerificationError(task)
	if err := validateTaskVerificationChecks(task.VerificationChecks); err != nil {
		return taskVerificationStatusUnverified, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := r.taskVerificationRoot(plan)
	if err != nil {
		return taskVerificationStatusUnverified, nil, err
	}
	snapshot, err := model.CaptureReviewSnapshot(ctx, root, model.ReviewSnapshotPolicy{
		Mode:             model.ReviewSnapshotWorktree,
		IncludeUntracked: true,
	})
	if err != nil {
		return taskVerificationStatusUnverified, nil, fmt.Errorf("capture task verification snapshot: %w", err)
	}
	workDir, cleanupWorkspace, err := model.PrepareReviewWorkspace(ctx, snapshot)
	if err != nil {
		return taskVerificationStatusUnverified, nil, fmt.Errorf("materialize task verification snapshot %s: %w", snapshot.ID(), err)
	}
	defer cleanupWorkspace()
	snapshotRoot, err := model.ReviewWorkspaceRepositoryRoot(ctx, workDir)
	if err != nil {
		return taskVerificationStatusUnverified, nil, fmt.Errorf("resolve task verification snapshot root: %w", err)
	}
	if err := model.VerifyReviewWorkspace(ctx, workDir, snapshot); err != nil {
		return taskVerificationStatusUnverified, nil, fmt.Errorf("verify task verification snapshot %s: %w", snapshot.ID(), err)
	}
	if host.makeTool == nil {
		host.makeTool = defaultTaskVerificationTool
	}
	tool, cleanupTool, err := host.makeTool(snapshotRoot, snapshot.RepositoryRoot(), 0)
	if err != nil {
		return taskVerificationStatusUnverified, nil, fmt.Errorf("create task verification tool: %w", err)
	}
	if cleanupTool != nil {
		defer cleanupTool()
	}

	results := make([]orchestrator.TaskVerificationResult, 0, len(task.VerificationChecks))
	overall := taskVerificationStatusPass
	var runErr error
	for _, check := range task.VerificationChecks {
		verification, err := runStructuredTaskVerification(ctx, tool, snapshot.ID(), task.ID, check)
		results = append(results, verification)
		if err != nil {
			runErr = errors.Join(runErr, err)
		}
		if !strings.EqualFold(verification.Status, "PASS") {
			overall = taskVerificationStatusFail
		}
	}
	if err := model.VerifyReviewWorkspace(ctx, workDir, snapshot); err != nil {
		mismatch := materializedTaskVerificationResult(task.ID, snapshot.ID(), err)
		results = append(results, mismatch)
		return taskVerificationStatusUnverified, results, errors.Join(runErr, legacyErr, fmt.Errorf("materialized task verification snapshot changed: %w", err))
	}

	current, err := model.CaptureReviewSnapshot(ctx, root, model.ReviewSnapshotPolicy{
		Mode:             model.ReviewSnapshotWorktree,
		IncludeUntracked: true,
	})
	if err != nil {
		return taskVerificationStatusUnverified, results, errors.Join(runErr, fmt.Errorf("recapture task verification source: %w", err))
	}
	if current.ID() != snapshot.ID() {
		stale := staleTaskVerificationResult(task.ID, snapshot.ID(), current.ID())
		results = append(results, stale)
		return taskVerificationStatusUnverified, results, errors.Join(runErr, legacyErr, fmt.Errorf("task verification source changed during checks: %s -> %s", snapshot.ID(), current.ID()))
	}
	if legacyErr != nil {
		return taskVerificationStatusUnverified, results, errors.Join(runErr, legacyErr)
	}
	if runErr != nil {
		return overall, results, runErr
	}
	return overall, results, nil
}

func (r *Runner) taskVerificationRoot(plan *orchestrator.Plan) (string, error) {
	root := ""
	if plan != nil {
		root = strings.TrimSpace(plan.Context.RepoRoot)
	}
	if root == "" {
		root = config.ResolveProjectRoot(r.cfg)
	}
	abs, err := filepath.Abs(root)
	if err != nil || strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("task verification repository root is required")
	}
	return abs, nil
}

func legacyTaskVerificationError(task orchestrator.Task) error {
	if len(task.Verification) == 0 {
		return nil
	}
	return fmt.Errorf("task %s has legacy verification prose that remains unverified", task.ID)
}

func validateTaskVerificationChecks(checks []orchestrator.TaskVerificationCheck) error {
	seen := make(map[string]struct{}, len(checks))
	for index, check := range checks {
		id := strings.TrimSpace(check.ID)
		if id == "" {
			return fmt.Errorf("task verification check %d: id is required", index)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("task verification check %q: duplicate id", id)
		}
		seen[id] = struct{}{}
		if check.TimeoutSeconds < 0 || check.TimeoutSeconds > 900 {
			return fmt.Errorf("task verification check %q: timeout_seconds must be between 0 and 900", id)
		}
	}
	return nil
}

func defaultTaskVerificationTool(snapshotRoot, sourceRoot string, timeout time.Duration) (taskVerificationTool, func(), error) {
	tool, err := builtin.NewRunVerificationToolWithSource(snapshotRoot, sourceRoot)
	if err != nil {
		return nil, nil, err
	}
	if timeout > 0 {
		tool.SetTimeoutLimit(timeout)
	}
	return tool, func() { _ = tool.Close() }, nil
}

func runStructuredTaskVerification(ctx context.Context, tool taskVerificationTool, snapshotID, taskID string, check orchestrator.TaskVerificationCheck) (orchestrator.TaskVerificationResult, error) {
	checkID := strings.TrimSpace(check.ID)
	params := map[string]any{
		"kind": strings.TrimSpace(check.Kind),
	}
	if language := strings.TrimSpace(check.Language); language != "" {
		params["language"] = language
	}
	if path := strings.TrimSpace(check.Path); path != "" {
		params["path"] = path
	}
	if pattern := strings.TrimSpace(check.Pattern); pattern != "" {
		params["pattern"] = pattern
	}
	if check.TimeoutSeconds > 0 {
		params["timeout_seconds"] = check.TimeoutSeconds
	}
	rawResult := json.RawMessage(`null`)
	status := "UNAVAILABLE"
	var err error
	if tool == nil {
		err = fmt.Errorf("task verification tool unavailable")
	} else {
		result, execErr := tool.ExecuteWithContext(ctx, params)
		rawResult = marshalTaskVerificationResult(result, execErr)
		status, err = taskVerificationResultStatus(result, execErr)
		if err != nil {
			err = fmt.Errorf("task %s verification %s is unverified: %w", taskID, checkID, err)
		} else if !strings.EqualFold(status, "PASS") {
			err = fmt.Errorf("task %s verification %s did not pass: %s", taskID, checkID, status)
		}
	}
	receipt := taskVerificationReceipt{
		Schema:     taskVerificationEvidenceSchemaV1,
		TaskID:     taskID,
		CheckID:    checkID,
		Check:      check,
		SnapshotID: snapshotID,
		Status:     status,
		Result:     rawResult,
	}
	return orchestrator.TaskVerificationResult{
		CheckID:    checkID,
		Status:     status,
		EvidenceID: taskVerificationEvidenceID(receipt),
		SnapshotID: snapshotID,
		Result:     append(json.RawMessage(nil), rawResult...),
	}, err
}

func marshalTaskVerificationResult(result *builtin.Result, execErr error) json.RawMessage {
	payload := map[string]any{}
	if result != nil {
		payload["success"] = result.Success
		if result.Error != "" {
			payload["error"] = result.Error
		}
		if len(result.Data) > 0 {
			payload["data"] = result.Data
		}
		if len(result.DisplayData) > 0 {
			payload["display_data"] = result.DisplayData
		}
		if result.ShouldAbridge {
			payload["should_abridge"] = true
		}
	}
	if execErr != nil {
		payload["execution_error"] = execErr.Error()
	}
	if len(payload) == 0 {
		return json.RawMessage(`null`)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"error":"marshal verification result"}`)
	}
	return raw
}

func taskVerificationResultStatus(result *builtin.Result, execErr error) (string, error) {
	status := "UNAVAILABLE"
	explicit := false
	if result != nil {
		if value, ok := result.Data["status"].(string); ok && strings.TrimSpace(value) != "" {
			status = strings.ToUpper(strings.TrimSpace(value))
			explicit = true
		}
	}
	if !explicit {
		return status, fmt.Errorf("verification result has no explicit status")
	}
	if strings.EqualFold(status, "PASS") {
		switch {
		case execErr != nil:
			return "UNAVAILABLE", fmt.Errorf("verification returned PASS with execution error: %w", execErr)
		case result == nil:
			return "UNAVAILABLE", fmt.Errorf("verification returned PASS without result")
		case !result.Success:
			return "UNAVAILABLE", fmt.Errorf("verification returned PASS with success=false")
		case strings.TrimSpace(result.Error) != "":
			return "UNAVAILABLE", fmt.Errorf("verification returned PASS with result error")
		}
	}
	if execErr != nil {
		return status, execErr
	}
	return status, nil
}

type taskVerificationReceipt struct {
	Schema     string                             `json:"schema"`
	TaskID     string                             `json:"task_id"`
	CheckID    string                             `json:"check_id"`
	Check      orchestrator.TaskVerificationCheck `json:"check"`
	SnapshotID string                             `json:"snapshot_id"`
	Status     string                             `json:"status"`
	Result     json.RawMessage                    `json:"result"`
}

func taskVerificationEvidenceID(receipt taskVerificationReceipt) string {
	raw, err := json.Marshal(receipt)
	if err != nil {
		raw = []byte(receipt.Schema + "\x00" + receipt.TaskID + "\x00" + receipt.CheckID + "\x00" + receipt.SnapshotID + "\x00" + receipt.Status)
	}
	sum := sha256.Sum256(raw)
	return "task-verification:v1:" + hex.EncodeToString(sum[:])
}

func staleTaskVerificationResult(taskID, snapshotID, currentID string) orchestrator.TaskVerificationResult {
	payload := map[string]any{
		"schema":      taskVerificationEvidenceSchemaV1,
		"task_id":     taskID,
		"snapshot_id": snapshotID,
		"current_id":  currentID,
		"error":       "source changed during verification",
	}
	raw, _ := json.Marshal(payload)
	receipt := taskVerificationReceipt{
		Schema:     taskVerificationEvidenceSchemaV1,
		TaskID:     taskID,
		CheckID:    "__source_snapshot__",
		SnapshotID: snapshotID,
		Status:     "UNAVAILABLE",
		Result:     raw,
	}
	return orchestrator.TaskVerificationResult{
		CheckID:    "__source_snapshot__",
		Status:     "UNAVAILABLE",
		EvidenceID: taskVerificationEvidenceID(receipt),
		SnapshotID: snapshotID,
		Result:     raw,
	}
}

func materializedTaskVerificationResult(taskID, snapshotID string, verifyErr error) orchestrator.TaskVerificationResult {
	payload := map[string]any{
		"schema":      taskVerificationEvidenceSchemaV1,
		"task_id":     taskID,
		"snapshot_id": snapshotID,
		"error":       verifyErr.Error(),
	}
	raw, _ := json.Marshal(payload)
	receipt := taskVerificationReceipt{
		Schema:     taskVerificationEvidenceSchemaV1,
		TaskID:     taskID,
		CheckID:    "__materialized_snapshot__",
		SnapshotID: snapshotID,
		Status:     "UNAVAILABLE",
		Result:     raw,
	}
	return orchestrator.TaskVerificationResult{
		CheckID:    "__materialized_snapshot__",
		Status:     "UNAVAILABLE",
		EvidenceID: taskVerificationEvidenceID(receipt),
		SnapshotID: snapshotID,
		Result:     raw,
	}
}
