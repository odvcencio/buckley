package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func (r *Registry) executeWithShellTelemetry(execFn func(map[string]any) (*builtin.Result, error), params map[string]any) (*builtin.Result, error) {
	command := sanitizeShellCommand(params)
	interactive := false
	if params != nil {
		if val, ok := params["interactive"].(bool); ok {
			interactive = val
		}
	}
	start := time.Now()
	bestEffortTelemetry(func() {
		r.publishShellEvent(telemetry.EventShellCommandStarted, map[string]any{
			"command":     command,
			"interactive": interactive,
		})
	})
	defer func() {
		if recovered := recover(); recovered != nil {
			r.publishShellTerminalBestEffort(
				telemetry.EventShellCommandFailed,
				command,
				interactive,
				start,
				nil,
				fmt.Errorf("run_shell panicked"),
			)
			panic(recovered)
		}
	}()

	res, err := execFn(params)
	eventType := telemetry.EventShellCommandCompleted
	if err != nil || (res != nil && !res.Success) {
		eventType = telemetry.EventShellCommandFailed
	}
	r.publishShellTerminalBestEffort(eventType, command, interactive, start, res, err)

	return res, err
}

func (r *Registry) publishShellTerminalBestEffort(eventType telemetry.EventType, command string, interactive bool, start time.Time, res *builtin.Result, err error) {
	bestEffortTelemetry(func() {
		payload := map[string]any{
			"command":     command,
			"duration_ms": time.Since(start).Milliseconds(),
			"interactive": interactive,
		}

		if res != nil {
			if exitCode, ok := res.Data["exit_code"]; ok {
				payload["exit_code"] = exitCode
			}
			if note, ok := res.DisplayData["message"].(string); ok && note != "" {
				payload["note"] = note
			}
			if stderr, ok := res.Data["stderr"].(string); ok && stderr != "" {
				payload["stderr_preview"] = truncateForTelemetry(stderr)
			}
			if stdout, ok := res.Data["stdout"].(string); ok && stdout != "" {
				payload["stdout_preview"] = truncateForTelemetry(stdout)
			}
			if res.Error != "" {
				payload["error"] = res.Error
			}
		}

		if err != nil {
			payload["error"] = err.Error()
		}
		r.publishShellEvent(eventType, payload)
	})
}

func (r *Registry) shouldGateChanges() bool {
	return r.requireMissionApproval && r.missionStore != nil && r.missionSession != ""
}

func (r *Registry) executeWithMissionWrite(ctx context.Context, tool Tool, params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	params = cloneApprovalParams(params)
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &builtin.Result{Error: "path parameter is required"}, nil
	}
	absPath, err := filepath.Abs(path)
	resolver, resolves := tool.(interface{ ResolvePath(string) (string, error) })
	if resolves {
		absPath, err = resolver.ResolvePath(path)
	}
	if err != nil {
		return &builtin.Result{Error: err.Error()}, nil
	}
	r.mu.RLock()
	container, compose, workDir := r.containerExecute, r.containerCompose, r.containerWorkDir
	r.mu.RUnlock()
	var diffText string
	var validate func() error
	if previewer, ok := tool.(interface {
		Preview(map[string]any) (*builtin.Result, error)
	}); ok && !container {
		preview, err := previewer.Preview(params)
		if err != nil || preview == nil || !preview.Success {
			return preview, err
		}
		if preview.DiffPreview == nil || preview.ValidateApproval == nil {
			return &builtin.Result{Error: "tool returned incomplete approval evidence"}, nil
		}
		absPath = preview.DiffPreview.FilePath
		diffText = preview.DiffPreview.UnifiedDiff
		validate = preview.ValidateApproval
		// Bind execution to the workspace target used to compute the diff.
		params["path"] = absPath
	}
	if diffText == "" {
		encoded, err := json.Marshal(params)
		if err != nil {
			return &builtin.Result{Error: fmt.Sprintf("encode approval arguments: %v", err)}, nil
		}
		diffText = fmt.Sprintf("%s requested for %s:\n%s", tool.Name(), absPath, encoded)
	}
	changeID, err := r.recordPendingChange(absPath, diffText, tool.Name())
	if err != nil {
		return &builtin.Result{Error: fmt.Sprintf("failed to create pending change: %v", err)}, nil
	}
	change, err := r.awaitDecision(ctx, changeID)
	if err != nil {
		return &builtin.Result{Error: fmt.Sprintf("approval wait failed: %v", err)}, nil
	}
	if change.Status != "approved" {
		return &builtin.Result{Error: fmt.Sprintf("change %s %s by %s", change.ID, change.Status, change.ReviewedBy)}, nil
	}
	r.mu.RLock()
	backendChanged := container != r.containerExecute || compose != r.containerCompose || workDir != r.containerWorkDir
	r.mu.RUnlock()
	if backendChanged {
		return &builtin.Result{Error: "execution backend changed since approval; retry"}, nil
	}
	if resolves {
		current, err := resolver.ResolvePath(path)
		if err != nil || current != absPath {
			return &builtin.Result{Error: "workspace target changed since approval; retry"}, nil
		}
	}
	if validate != nil {
		if err := validate(); err != nil {
			return &builtin.Result{Error: err.Error()}, nil
		}
	}
	return execFn(params)
}

func cloneApprovalParams(params map[string]any) map[string]any {
	copy := make(map[string]any, len(params))
	for key, value := range params {
		copy[key] = cloneApprovalValue(value)
	}
	return copy
}

func cloneApprovalValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneApprovalParams(v)
	case []any:
		copy := make([]any, len(v))
		for i, item := range v {
			copy[i] = cloneApprovalValue(item)
		}
		return copy
	case []string:
		return append([]string(nil), v...)
	default:
		return value
	}
}

func (r *Registry) executeWithMissionPatch(ctx context.Context, params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	rawPatch, ok := params["patch"].(string)
	if !ok || strings.TrimSpace(rawPatch) == "" {
		return &builtin.Result{Success: false, Error: "patch parameter must be a non-empty string"}, nil
	}

	target := derivePatchTarget(rawPatch)
	changeID, err := r.recordPendingChange(target, rawPatch, "apply_patch")
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("failed to create pending change: %v", err)}, nil
	}

	change, err := r.awaitDecision(ctx, changeID)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("approval wait failed: %v", err)}, nil
	}
	if change.Status != "approved" {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("change %s %s by %s", change.ID, change.Status, change.ReviewedBy)}, nil
	}

	return execFn(params)
}

func (r *Registry) executeWithMissionClipboardRead(ctx context.Context, params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	rawSession, ok := params["session_id"]
	if !ok {
		return &builtin.Result{Success: false, Error: "session_id parameter is required"}, nil
	}
	sessionID := strings.TrimSpace(fmt.Sprintf("%v", rawSession))
	if sessionID == "" || sessionID == "<nil>" {
		return &builtin.Result{Success: false, Error: "session_id parameter is required"}, nil
	}

	expectedState := ""
	if rawState, ok := params["expected_state_version"]; ok {
		if trimmed := strings.TrimSpace(fmt.Sprintf("%v", rawState)); trimmed != "" && trimmed != "<nil>" {
			expectedState = trimmed
		}
	}

	diff := fmt.Sprintf("clipboard read requested\nsession_id: %s", sessionID)
	if expectedState != "" {
		diff = fmt.Sprintf("%s\nexpected_state_version: %s", diff, expectedState)
	}

	changeID, err := r.recordPendingChange(fmt.Sprintf("browser/clipboard/%s", sessionID), diff, "browser_clipboard_read")
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("failed to create pending change: %v", err)}, nil
	}

	change, err := r.awaitDecision(ctx, changeID)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("approval wait failed: %v", err)}, nil
	}
	if change.Status != "approved" {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("change %s %s by %s", change.ID, change.Status, change.ReviewedBy)}, nil
	}

	return execFn(params)
}

func (r *Registry) recordPendingChange(filePath, diff, toolName string) (string, error) {
	if r.missionStore == nil {
		return "", fmt.Errorf("mission store not configured")
	}

	changeID := ulid.Make().String()
	change := &mission.PendingChange{
		ID:        changeID,
		AgentID:   defaultAgent(r.missionAgent),
		SessionID: r.missionSession,
		FilePath:  filePath,
		Diff:      diff,
		Reason:    fmt.Sprintf("%s requested by %s", toolName, defaultAgent(r.missionAgent)),
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	return changeID, r.missionStore.CreatePendingChange(change)
}

func (r *Registry) awaitDecision(parentCtx context.Context, changeID string) (*mission.PendingChange, error) {
	timeout := r.missionTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	// Create a context that respects both the parent context and the timeout
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	return r.missionStore.WaitForDecision(ctx, changeID, 750*time.Millisecond)
}

func derivePatchTarget(rawPatch string) string {
	lines := strings.Split(rawPatch, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return strings.TrimSpace(fields[1])
			}
		}
	}
	return "apply_patch"
}

func defaultAgent(agent string) string {
	if strings.TrimSpace(agent) == "" {
		return "buckley-cli"
	}
	return agent
}
