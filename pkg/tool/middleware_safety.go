package tool

import (
	"fmt"
	"runtime/debug"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// PanicRecovery converts panics into tool errors and records stack traces in metadata.
func PanicRecovery() Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (result *builtin.Result, err error) {
			defer func() {
				if r := recover(); r != nil {
					if ctx != nil {
						if ctx.Metadata == nil {
							ctx.Metadata = map[string]any{}
						}
						ctx.Metadata["panic_stack"] = string(debug.Stack())
						ctx.Metadata["panic_value"] = fmt.Sprintf("%v", r)
					}
					name := "tool"
					if ctx != nil && ctx.ToolName != "" {
						name = fmt.Sprintf("tool %s", ctx.ToolName)
					}
					err = fmt.Errorf("%s panicked", name)
					result = &builtin.Result{Success: false, Error: err.Error()}
				}
			}()
			return next(ctx)
		}
	}
}
