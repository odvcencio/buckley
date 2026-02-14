package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/pmezard/go-difflib/difflib"

	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
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
	r.publishShellEvent(telemetry.EventShellCommandStarted, map[string]any{
		"command":     command,
		"interactive": interactive,
	})

	res, err := execFn(params)
	duration := time.Since(start)

	payload := map[string]any{
		"command":     command,
		"duration_ms": duration.Milliseconds(),
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

	if err != nil || (res != nil && !res.Success) {
		if err != nil {
			payload["error"] = err.Error()
		}
		r.publishShellEvent(telemetry.EventShellCommandFailed, payload)
	} else {
		r.publishShellEvent(telemetry.EventShellCommandCompleted, payload)
	}

	return res, err
}

func (r *Registry) shouldGateChanges() bool {
	return r.requireMissionApproval && r.missionStore != nil && r.missionSession != ""
}

func (r *Registry) executeWithMissionWrite(ctx context.Context, params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	path, ok := params["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return &builtin.Result{Success: false, Error: "path parameter is required"}, nil
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("invalid path: %v", err)}, nil
	}

	// Build a diff description based on available parameters.
	// write_file provides "content", edit_file provides "old_string"/"new_string", etc.
	var diffText string
	if content, ok := params["content"].(string); ok {
		oldContent := ""
		if existing, err := os.ReadFile(absPath); err == nil {
			oldContent = string(existing)
		}
		if oldContent == content {
			return execFn(params)
		}
		diffText, err = r.buildUnifiedDiff(absPath, oldContent, content)
		if err != nil {
			return &builtin.Result{Success: false, Error: fmt.Sprintf("failed to build diff: %v", err)}, nil
		}
	} else if oldStr, ok := params["old_string"].(string); ok {
		newStr, _ := params["new_string"].(string)
		diffText = fmt.Sprintf("edit_file %s:\n- %s\n+ %s", absPath, oldStr, newStr)
	} else if text, ok := params["text"].(string); ok {
		line, _ := params["line"].(float64)
		diffText = fmt.Sprintf("insert_text %s at line %d:\n+ %s", absPath, int(line), text)
	} else if startLine, ok := params["start_line"].(float64); ok {
		endLine, _ := params["end_line"].(float64)
		diffText = fmt.Sprintf("delete_lines %s: lines %d-%d", absPath, int(startLine), int(endLine))
	} else {
		diffText = fmt.Sprintf("file modification: %s (params: %v)", absPath, params)
	}

	changeID, err := r.recordPendingChange(absPath, diffText, "write_file")
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

func (r *Registry) buildUnifiedDiff(path, from, to string) (string, error) {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(from),
		B:        difflib.SplitLines(to),
		FromFile: path,
		ToFile:   path,
		Context:  3,
	}
	return difflib.GetUnifiedDiffString(diff)
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
