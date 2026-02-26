package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// sandboxedTools are tool names that should be routed through the Docker sandbox.
var sandboxedTools = map[string]bool{
	"run_shell": true,
	"run_tests": true,
}

// DockerSandboxMiddleware intercepts shell-executing tools and routes them
// through the provided SandboxExecutor. Read-only tools pass through untouched.
func DockerSandboxMiddleware(sandbox SandboxExecutor, kindLookup func(string) string) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if sandbox == nil || ctx == nil {
				return next(ctx)
			}

			toolName := strings.TrimSpace(ctx.ToolName)
			if !sandboxedTools[toolName] {
				return next(ctx)
			}

			command := extractCommand(toolName, ctx.Params)
			if command == "" {
				return next(ctx)
			}

			execCtx := ctx.Context
			if execCtx == nil {
				execCtx = context.Background()
			}

			req := SandboxRequest{
				Command:  command,
				ToolName: toolName,
			}
			if workDir, ok := ctx.Params["working_directory"].(string); ok && workDir != "" {
				req.WorkDir = workDir
			}
			if timeout, ok := ctx.Params["timeout"].(float64); ok && timeout > 0 {
				req.Timeout = time.Duration(timeout) * time.Second
			}

			result, err := sandbox.Execute(execCtx, req)
			if err != nil {
				return &builtin.Result{
					Success: false,
					Error:   fmt.Sprintf("docker sandbox: %v", err),
				}, nil
			}

			return sandboxResultToBuiltin(result), nil
		}
	}
}

func extractCommand(toolName string, params map[string]any) string {
	if params == nil {
		return ""
	}
	switch toolName {
	case "run_shell":
		if cmd, ok := params["command"].(string); ok {
			return strings.TrimSpace(cmd)
		}
	case "run_tests":
		if cmd, ok := params["command"].(string); ok {
			return strings.TrimSpace(cmd)
		}
	}
	return ""
}

func sandboxResultToBuiltin(r *SandboxResult) *builtin.Result {
	if r == nil {
		return &builtin.Result{Success: false, Error: "nil sandbox result"}
	}

	data := map[string]any{
		"exit_code": r.ExitCode,
		"stdout":    r.Stdout,
		"stderr":    r.Stderr,
	}
	if r.Killed {
		data["killed"] = true
	}

	success := r.ExitCode == 0
	result := &builtin.Result{
		Success: success,
		Data:    data,
	}
	if !success {
		result.Error = fmt.Sprintf("command exited with code %d", r.ExitCode)
	}
	return result
}
