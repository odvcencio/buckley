package tool

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// PluginVetoGate is the pre-tool veto surface a plugin hook process
// exposes (pkg/tool/external.HookRunner is the concrete implementation
// wired in production; its Veto method satisfies this interface without
// either package importing the other). NewPluginVetoMiddleware depends
// only on this narrow interface so tests can supply a stub.
type PluginVetoGate interface {
	// Veto asks every plugin whose manifest hooks.pre_tool.tools glob
	// matches toolName to approve or deny the call. denied reports the
	// final decision; plugin names the plugin that denied (empty when
	// denied is false); reason is that plugin's explanation.
	Veto(ctx context.Context, toolName string, sanitizedArgs map[string]any) (denied bool, plugin, reason string)
}

// NewPluginVetoMiddleware evaluates every tool call against the process
// plugins registered as pre-tool veto gates (ADR 0002; see
// pkg/tool/external's hook contract). It sends each plugin the tool name
// and its already-redacted/bounded arguments (reusing pkg/telemetry's
// sanitizing layer) and denies the call if any matching plugin does.
//
// Composition order matters: this middleware must sit INSIDE (nested
// after) NewPermissionMiddleware in the chain passed to Chain(...), for
// example Chain(NewPermissionMiddleware(gate), NewPluginVetoMiddleware(veto)).
// A built-in policy deny returns from the outer permission middleware
// without ever calling next, so this middleware never runs for a call
// policy already denied -- a plugin can only narrow what policy allowed,
// never re-allow what policy denied.
func NewPluginVetoMiddleware(gate PluginVetoGate) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if gate == nil || ctx == nil {
				return next(ctx)
			}

			reqCtx := ctx.Context
			if reqCtx == nil {
				reqCtx = context.Background()
			}
			sanitizedArgs := sanitizeArgsForVeto(ctx.Params)

			denied, plugin, reason := gate.Veto(reqCtx, ctx.ToolName, sanitizedArgs)
			if denied {
				reason = strings.TrimSpace(reason)
				if reason == "" {
					reason = "no reason given"
				}
				return &builtin.Result{
					Success: false,
					Error:   fmt.Sprintf("denied by plugin %q: %s", plugin, reason),
				}, fmt.Errorf("tool %q denied by plugin %q", ctx.ToolName, plugin)
			}
			return next(ctx)
		}
	}
}

// sanitizeArgsForVeto redacts sensitive-looking fields and bounds string
// sizes in params before it ever leaves the process toward a plugin,
// reusing the same telemetry.NormalizeAndSanitize pass and
// stripToolCallID helper tool call telemetry already applies (see
// telemetry_detail.go's withTelemetryArguments).
func sanitizeArgsForVeto(params map[string]any) map[string]any {
	if len(params) == 0 {
		return map[string]any{}
	}
	clean := telemetry.NormalizeAndSanitize(stripToolCallID(params), telemetry.MaxArgumentBytes)
	if m, ok := clean.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
