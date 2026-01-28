package tool

import (
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
				execCtx = ctx.Context
			}

			switch strings.TrimSpace(ctx.ToolName) {
			case "write_file":
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
			default:
				return next(ctx)
			}
		}
	}
}
