package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func (r *Registry) approvalMiddleware() Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if r == nil || ctx == nil || !r.shouldGateChanges() {
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
			default:
				return next(ctx)
			}
		}
	}
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
