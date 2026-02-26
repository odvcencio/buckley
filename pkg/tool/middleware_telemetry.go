package tool

import (
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/touch"
)

func (r *Registry) telemetryMiddleware() Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if r == nil || r.telemetryHub == nil {
				return next(ctx)
			}
			if ctx == nil {
				return next(ctx)
			}

			name := strings.TrimSpace(ctx.ToolName)
			params := ctx.Params
			if params == nil {
				params = map[string]any{}
				ctx.Params = params
			}
			if strings.TrimSpace(ctx.CallID) == "" {
				ctx.CallID = toolCallIDFromParams(params)
			}
			if ctx.StartTime.IsZero() {
				ctx.StartTime = time.Now()
			}

			rich := touch.ExtractFromArgs(name, params)
			r.publishToolEvent(telemetry.EventToolStarted, ctx.CallID, name, rich, ctx.StartTime, nil, nil, ctx.Attempt, ctx.Metadata)

			execFn := func(p map[string]any) (*builtin.Result, error) {
				ctx.Params = p
				return next(ctx)
			}

			var (
				res *builtin.Result
				err error
			)
			if name == "run_shell" {
				res, err = r.executeWithShellTelemetry(execFn, params)
			} else {
				res, err = execFn(params)
			}

			r.publishToolEvent(eventTypeForResult(res, err), ctx.CallID, name, rich, time.Now(), res, err, ctx.Attempt, ctx.Metadata)
			return res, err
		}
	}
}
