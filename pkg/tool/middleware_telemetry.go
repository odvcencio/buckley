package tool

import (
	"fmt"
	"math"
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

			rich := r.extractTelemetryFieldsSafely(ctx)
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

			bestEffortTelemetry(func() {
				if res != nil && res.Success {
					if res.DiffPreview != nil {
						rich = touch.WithDiff(rich, res.DiffPreview.UnifiedDiff)
						if res.DiffPreview.DiffTruncated {
							rich.AddedLines = int32(min(res.DiffPreview.LinesAdded, math.MaxInt32))
							rich.RemovedLines = int32(min(res.DiffPreview.LinesRemoved, math.MaxInt32))
						}
						rich.FilePath = res.DiffPreview.FilePath
					} else if path, ok := res.Data["path"].(string); ok {
						rich.FilePath = path
					}
				}
			})
			completionMetadata := telemetryArgumentsSafely(ctx.Metadata, ctx.Params)
			r.publishToolEventBestEffort(eventTypeForResult(res, err), ctx.CallID, name, rich, time.Now(), res, err, ctx.Attempt, completionMetadata)
			return res, err
		}
	}
}

func (r *Registry) extractTelemetryFieldsSafely(ctx *ExecutionContext) (rich touch.RichFields) {
	defer func() {
		if recover() != nil {
			rich = touch.RichFields{}
		}
	}()
	options := touch.ExtractOptions{}
	if resolver, ok := ctx.Tool.(interface{ ResolvePath(string) (string, error) }); ok {
		options.ResolvePath = resolver.ResolvePath
	}
	if budget, ok := ctx.Tool.(interface{ FileMutationLimit() int64 }); ok {
		options.MaxFileBytes = budget.FileMutationLimit()
	}
	_, options.SkipFileDiff = r.checkBuiltinPermissionDefaults(ctx)
	if ctx.ToolName == "edit_file" {
		options.SkipFileDiff = true
	}
	return touch.ExtractFromArgsWithOptions(ctx.ToolName, ctx.Params, options)
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
