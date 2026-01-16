package tool

import (
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// Hooks runs registered pre/post hooks around tool execution.
func Hooks(registry *HookRegistry) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if registry == nil || ctx == nil {
				return next(ctx)
			}

			for _, hook := range registry.PreHooks(ctx.ToolName) {
				result := hook(ctx)
				if result.ModifiedParams != nil {
					ctx.Params = result.ModifiedParams
				}
				if result.Abort {
					reason := strings.TrimSpace(result.AbortReason)
					if reason == "" {
						reason = "aborted by hook"
					}
					err := fmt.Errorf("aborted by hook: %s", reason)
					if result.AbortResult != nil {
						return result.AbortResult, err
					}
					return &builtin.Result{Success: false, Error: reason}, err
				}
			}

			res, err := next(ctx)

			hooks := registry.PostHooks(ctx.ToolName)
			for i := len(hooks) - 1; i >= 0; i-- {
				res, err = hooks[i](ctx, res, err)
			}

			return res, err
		}
	}
}
