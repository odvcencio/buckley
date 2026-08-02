package tool

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func (r *Registry) approvalMiddleware() Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if r == nil || ctx == nil {
				return next(ctx)
			}

			// Built-in permission defaults (deny reads of .env/credential-shaped
			// paths and id_rsa keys) run unconditionally for every tool call,
			// regardless of mission-control state or approval mode: a deny
			// blocks the call even in yolo mode. See pkg/policy.BuiltinDefaultRules
			// and ADR 0006 ("denied paths are enforced even in Yolo mode").
			if result, denied := r.checkBuiltinPermissionDefaults(ctx); denied {
				return result, nil
			}

			if !r.shouldGateChanges() {
				return next(ctx)
			}

			execCtx := ctx.Context
			if execCtx == nil {
				execCtx = context.Background()
			}

			switch strings.TrimSpace(ctx.ToolName) {
			case "write_file", "edit_file", "insert_text", "delete_lines",
				"search_replace", "patch_file", "rename_symbol", "extract_function":
				return r.executeWithMissionWrite(execCtx, ctx.Params, func(params map[string]any) (*builtin.Result, error) {
					ctx.Params = params
					return next(ctx)
				})
			case "apply_patch":
				return r.executeWithMissionPatch(execCtx, ctx.Params, func(params map[string]any) (*builtin.Result, error) {
					ctx.Params = params
					return next(ctx)
				})
			case "browser_clipboard_read":
				return r.executeWithMissionClipboardRead(execCtx, ctx.Params, func(params map[string]any) (*builtin.Result, error) {
					ctx.Params = params
					return next(ctx)
				})
			case "run_shell":
				return r.executeWithMissionShell(execCtx, ctx.Params, func(params map[string]any) (*builtin.Result, error) {
					ctx.Params = params
					return next(ctx)
				})
			case "run_code":
				return r.executeWithMissionCode(execCtx, ctx.Params, func(params map[string]any) (*builtin.Result, error) {
					ctx.Params = params
					return next(ctx)
				})
			default:
				return next(ctx)
			}
		}
	}
}

// checkBuiltinPermissionDefaults evaluates the built-in glob-permission
// defaults (pkg/policy.BuiltinDefaultRules) against a tool call using the
// deterministic Go glob layer, with no dependency on registry configuration.
// It only reports denied=true for an outright deny; "ask" decisions are left
// to the coarser approval mechanisms already in the chain (mission control,
// or a caller-registered permission middleware with posture-aware parking).
func (r *Registry) checkBuiltinPermissionDefaults(ctx *ExecutionContext) (*builtin.Result, bool) {
	req, ok := derivePermissionRequest(ctx.ToolName, ctx.Params, "", "")
	if !ok {
		return nil, false
	}
	dec := policy.EvaluateBuiltinDefaultsGo(req)
	if !dec.Matched || dec.Action != policy.PermissionDeny {
		return nil, false
	}
	return &builtin.Result{
		Success: false,
		Error:   fmt.Sprintf("permission denied: %s %q matches a built-in default deny rule", ctx.ToolName, req.Arg),
	}, true
}

// executeWithMissionShell gates shell commands through mission control approval.
// Unlike executeWithMissionWrite, it extracts the "command" parameter instead of "path"/"content".
func (r *Registry) executeWithMissionShell(ctx context.Context, params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	command, ok := params["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return &builtin.Result{Success: false, Error: "command parameter is required"}, nil
	}

	diff := fmt.Sprintf("shell command requested:\n$ %s", command)

	changeID, err := r.recordPendingChange("shell://run_shell", diff, "run_shell")
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

// executeWithMissionCode gives dynamic code the same approval boundary as a
// shell command while presenting readable source rather than the encoded shell
// wrapper used by the execution backend.
func (r *Registry) executeWithMissionCode(ctx context.Context, params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	language, _ := params["language"].(string)
	code, _ := params["code"].(string)
	language = strings.TrimSpace(language)
	code = strings.TrimSpace(code)
	if language == "" {
		return &builtin.Result{Success: false, Error: "language parameter is required"}, nil
	}
	if code == "" {
		return &builtin.Result{Success: false, Error: "code parameter is required"}, nil
	}
	// Reuse the tool's canonical validator before displaying or approving work.
	if _, err := builtin.DynamicCodeCommand(params); err != nil {
		return &builtin.Result{Success: false, Error: err.Error()}, nil
	}

	preview := code
	const maxPreviewBytes = 16 * 1024
	if len(preview) > maxPreviewBytes {
		preview = preview[:maxPreviewBytes] + fmt.Sprintf("\n... %d code bytes omitted from approval preview ...", len(code)-maxPreviewBytes)
	}
	diff := fmt.Sprintf("%s code requested:\n\n%s", language, preview)

	changeID, err := r.recordPendingChange("code://run_code", diff, "run_code")
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
