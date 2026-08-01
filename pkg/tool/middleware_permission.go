package tool

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/v2/pkg/policy"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
	"m31labs.dev/buckley/v2/pkg/types"
)

// PermissionGate supplies the layered glob-permission configuration
// consulted by NewPermissionMiddleware: the posture/project/user rule
// layers in priority order (posture first), the workspace root used to
// compute whether an argument resolves inside the workspace, the active
// posture name, an optional rules engine for the arbiter-backed built-in
// defaults path, and an optional sink that collects parked "ask" decisions.
type PermissionGate struct {
	// Layers are evaluated in priority order (highest first, e.g. posture,
	// project, user); the built-in defaults layer is always consulted last.
	Layers []policy.PermissionLayer
	// WorkspaceRoot is the absolute path used to compute
	// PermissionRequest.WorkspaceRelative for file and shell arguments.
	WorkspaceRoot string
	// Posture is the active posture name (see policy.SelectPosture).
	Posture string
	// ParkAskDecisions routes "ask" decisions to ParkedSink instead of
	// blocking on human approval. Set for postures with nobody present to
	// answer (e.g. unattended).
	ParkAskDecisions bool
	// Evaluator, when non-nil, lets built-in defaults evaluate via the
	// embedded rules engine; a nil evaluator falls back to the deterministic
	// Go glob layer (see policy.EvaluateBuiltinDefaults).
	Evaluator types.RuleEvaluator
	// ParkedSink receives parked decisions; nil discards them.
	ParkedSink policy.ParkedDecisionSink
}

// NewPermissionMiddleware evaluates every tool call against the layered
// glob-permission rules (pkg/policy): posture, project, user, and built-in
// defaults, in that priority order, with built-in defaults always
// consulted. A deny blocks the call outright, in every approval mode. An
// "ask" is parked (see PermissionGate.ParkAskDecisions) instead of
// blocking under postures where nobody is present to answer; otherwise it
// passes through unchanged, deferring to whatever coarser approval
// mechanism is already in the chain (ADR 0006 tiers stay the coarse gate;
// these rules refine within a tier).
func NewPermissionMiddleware(gate *PermissionGate) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if gate == nil || ctx == nil {
				return next(ctx)
			}
			req, ok := derivePermissionRequest(ctx.ToolName, ctx.Params, gate.WorkspaceRoot, gate.Posture)
			if !ok {
				return next(ctx)
			}

			dec := policy.EvaluatePermissionLayersWithBuiltins(gate.Evaluator, req, gate.Layers...)
			switch dec.Action {
			case policy.PermissionDeny:
				return &builtin.Result{
					Success: false,
					Error:   fmt.Sprintf("permission denied by %s rule: %s %q", dec.Layer, ctx.ToolName, req.Arg),
				}, nil
			case policy.PermissionAsk:
				if !gate.ParkAskDecisions {
					return next(ctx)
				}
				parked := policy.ParkedDecision{
					ID:        ctx.CallID,
					Tool:      ctx.ToolName,
					Arg:       req.Arg,
					Layer:     dec.Layer,
					Rule:      dec.Rule,
					Posture:   gate.Posture,
					CreatedAt: time.Now(),
				}
				if gate.ParkedSink != nil {
					gate.ParkedSink.RecordParkedDecision(parked)
				}
				return &builtin.Result{
					Success: false,
					Error:   fmt.Sprintf("parked under posture %q: %s %q requires approval", gate.Posture, ctx.ToolName, req.Arg),
					Data: map[string]any{
						"parked":  true,
						"posture": gate.Posture,
						"layer":   dec.Layer,
					},
				}, nil
			default:
				return next(ctx)
			}
		}
	}
}

// derivePermissionRequest builds a policy.PermissionRequest from a tool
// call, or returns ok=false when the tool carries no argument relevant to
// glob-permission evaluation (command string or file path).
func derivePermissionRequest(toolName string, params map[string]any, workspaceRoot, posture string) (policy.PermissionRequest, bool) {
	toolName = strings.TrimSpace(toolName)

	switch toolName {
	case "run_shell":
		cmd, _ := params["command"].(string)
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			return policy.PermissionRequest{}, false
		}
		return policy.PermissionRequest{
			Tool:              toolName,
			Category:          "shell",
			Arg:               cmd,
			WorkspaceRelative: isShellCommandWorkspaceRelative(cmd, workspaceRoot),
			Posture:           posture,
		}, true
	case "run_code":
		cmd, err := builtin.DynamicCodeCommand(params)
		cmd = strings.TrimSpace(cmd)
		if err != nil || cmd == "" {
			return policy.PermissionRequest{}, false
		}
		return policy.PermissionRequest{
			Tool:              toolName,
			Category:          "shell",
			Arg:               cmd,
			WorkspaceRelative: isShellCommandWorkspaceRelative(cmd, workspaceRoot),
			Posture:           posture,
		}, true
	}

	path := extractPathArg(params)
	if path == "" {
		return policy.PermissionRequest{}, false
	}
	return policy.PermissionRequest{
		Tool:              toolName,
		Category:          filePathCategory(toolName),
		Arg:               path,
		WorkspaceRelative: isWorkspaceRelative(path, workspaceRoot),
		Posture:           posture,
	}, true
}

// extractPathArg looks up the path argument used by file tools.
func extractPathArg(params map[string]any) string {
	if params == nil {
		return ""
	}
	if p, ok := params["path"].(string); ok && strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p)
	}
	if p, ok := params["file_path"].(string); ok && strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p)
	}
	return ""
}

// filePathCategory classifies a file tool as a read or write category
// based on its name (write/edit/delete/insert/patch/rename/extract/create
// imply a write).
func filePathCategory(toolName string) string {
	name := strings.ToLower(toolName)
	writeMarkers := []string{"write", "edit", "delete", "insert", "patch", "rename", "extract", "create"}
	for _, marker := range writeMarkers {
		if strings.Contains(name, marker) {
			return string(policy.CategoryFileWrite)
		}
	}
	return string(policy.CategoryFileRead)
}

// isWorkspaceRelative reports whether path resolves inside workspaceRoot.
// An empty workspaceRoot means there is no confirmed workspace, so NOTHING
// counts as workspace-relative: OutsideWorkspaceOnly protections must fire
// when the workspace is unknown, never silently stand down (fail-safe).
func isWorkspaceRelative(path, workspaceRoot string) bool {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return true
	}
	return absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(filepath.Separator))
}

// isShellCommandWorkspaceRelative reports whether cmd appears confined to
// the workspace: it flags a command as non-workspace-relative when it
// references an absolute path (or "~") outside workspaceRoot. This is a
// heuristic (commands can reach outside the workspace in ways a token scan
// can't see), matching the existing approach in pkg/approval.
func isShellCommandWorkspaceRelative(cmd, workspaceRoot string) bool {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		// No confirmed workspace: fail-safe, matching isWorkspaceRelative.
		return false
	}
	if strings.Contains(cmd, "~") {
		return false
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return false
	}
	for _, token := range strings.Fields(cmd) {
		token = strings.Trim(token, `"'`)
		if !strings.HasPrefix(token, "/") {
			// Relative tokens containing traversal are also suspect: a
			// cleaned relative path that climbs out of the workspace is
			// outside it.
			if strings.Contains(token, "..") {
				return false
			}
			continue
		}
		// Clean before comparing so /workspace/../etc resolves to /etc and
		// fails the prefix check instead of slipping past it.
		cleaned := filepath.Clean(token)
		if cleaned != absRoot && !strings.HasPrefix(cleaned, absRoot+string(filepath.Separator)) {
			return false
		}
	}
	return true
}
