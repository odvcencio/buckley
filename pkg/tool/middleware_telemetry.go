package tool

import (
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/touch"
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
				ctx.CallID = toolCallIDSafely(params)
			}
			if ctx.StartTime.IsZero() {
				ctx.StartTime = time.Now()
			}

			rich := extractTelemetryFieldsSafely(name, params)
			metadata := telemetryArgumentsSafely(ctx.Metadata, params)
			r.publishToolEventBestEffort(telemetry.EventToolStarted, ctx.CallID, name, rich, ctx.StartTime, nil, nil, ctx.Attempt, metadata)
			defer func() {
				if recovered := recover(); recovered != nil {
					panicErr := fmt.Errorf("tool %s panicked", name)
					panicResult := &builtin.Result{Success: false, Error: panicErr.Error()}
					completionMetadata := telemetryArgumentsSafely(ctx.Metadata, ctx.Params)
					r.publishToolEventBestEffort(telemetry.EventToolFailed, ctx.CallID, name, rich, time.Now(), panicResult, panicErr, ctx.Attempt, completionMetadata)
					// Preserve the registry's outer PanicRecovery behavior and
					// return semantics after publishing the terminal event.
					panic(recovered)
				}
			}()

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

			completionMetadata := telemetryArgumentsSafely(ctx.Metadata, ctx.Params)
			r.publishToolEventBestEffort(eventTypeForResult(res, err), ctx.CallID, name, rich, time.Now(), res, err, ctx.Attempt, completionMetadata)
			return res, err
		}
	}
}

func extractTelemetryFieldsSafely(name string, params map[string]any) (rich touch.RichFields) {
	defer func() {
		if recover() != nil {
			rich = touch.RichFields{}
		}
	}()
	return touch.ExtractFromArgs(name, params)
}

func telemetryArgumentsSafely(metadata map[string]any, params map[string]any) (out map[string]any) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	return withTelemetryArguments(metadata, params)
}

func toolCallIDSafely(params map[string]any) (callID string) {
	defer func() {
		if recover() != nil {
			callID = toolCallIDFromParams(nil)
		}
	}()
	return toolCallIDFromParams(params)
}

func bestEffortTelemetry(fn func()) {
	defer func() { _ = recover() }()
	fn()
}
