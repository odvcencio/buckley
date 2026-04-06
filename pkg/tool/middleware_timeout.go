package tool

import (
	"context"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// Timeout applies a per-tool or default timeout by updating the context.
func Timeout(defaultTimeout time.Duration, perTool map[string]time.Duration) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if ctx == nil {
				return next(ctx)
			}
			timeout := defaultTimeout
			if perTool != nil {
				if t, ok := perTool[ctx.ToolName]; ok {
					timeout = t
				}
			}
			if timeout <= 0 {
				return next(ctx)
			}

			base := ctx.Context
			if base == nil {
				base = context.Background()
			}
			timeoutCtx, cancel := context.WithTimeout(base, timeout)
			defer cancel()

			ctx.Context = timeoutCtx
			return next(ctx)
		}
	}
}
