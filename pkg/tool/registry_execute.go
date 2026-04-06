package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/containerexec"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// Execute executes a tool by name using a background context.
func (r *Registry) Execute(name string, params map[string]any) (*builtin.Result, error) {
	return r.ExecuteWithContext(context.Background(), name, params)
}

// ExecuteWithContext executes a tool by name using the provided context.
func (r *Registry) ExecuteWithContext(ctx context.Context, name string, params map[string]any) (*builtin.Result, error) {
	if name == "" {
		return nil, fmt.Errorf("tool name cannot be empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	execCtx := &ExecutionContext{
		Context:   ctx,
		ToolName:  name,
		Tool:      t,
		SessionID: r.telemetrySession,
		CallID:    toolCallIDFromParams(params),
		Params:    params,
		StartTime: time.Now(),
		Attempt:   1,
		Metadata:  make(map[string]any),
	}
	exec := r.executorForCall()
	if exec == nil {
		return nil, fmt.Errorf("tool executor not initialized")
	}
	return exec(execCtx)
}

func (r *Registry) executorForCall() Executor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	exec := r.executor
	r.mu.RUnlock()
	if exec != nil {
		return exec
	}
	r.rebuildExecutor()
	r.mu.RLock()
	exec = r.executor
	r.mu.RUnlock()
	return exec
}

func (r *Registry) rebuildExecutor() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuildExecutorLocked()
}

func (r *Registry) rebuildExecutorLocked() {
	base := r.baseExecutor()
	middlewares := make([]Middleware, 0, len(r.middlewares)+4)
	middlewares = append(middlewares, PanicRecovery(), r.telemetryMiddleware(), Hooks(r.hooks), r.approvalMiddleware())
	middlewares = append(middlewares, r.middlewares...)
	r.executor = Chain(middlewares...)(base)
}

func (r *Registry) baseExecutor() Executor {
	return func(ctx *ExecutionContext) (*builtin.Result, error) {
		if ctx == nil {
			return nil, fmt.Errorf("execution context required")
		}
		name := strings.TrimSpace(ctx.ToolName)
		if name == "" {
			return nil, fmt.Errorf("tool name cannot be empty")
		}
		t := ctx.Tool
		if t == nil {
			var ok bool
			t, ok = r.Get(name)
			if !ok {
				return nil, fmt.Errorf("tool not found: %s", name)
			}
			ctx.Tool = t
		}

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
		return r.executeTool(ctx, t, params)
	}
}

func (r *Registry) executeTool(ctx *ExecutionContext, tool Tool, params map[string]any) (*builtin.Result, error) {
	if ctx != nil && ctx.Context != nil {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
	}
	if r.containerExecute && r.containerCompose != "" {
		service := containerexec.GetServiceForTool(strings.TrimSpace(ctx.ToolName))
		runner := containerexec.NewContainerRunner(r.containerCompose, service, r.containerWorkDir, tool)
		return runner.Execute(params)
	}
	if tool == nil {
		return nil, fmt.Errorf("tool required")
	}
	if ctxTool, ok := tool.(ContextTool); ok {
		execCtx := ctx.Context
		if execCtx == nil {
			execCtx = context.Background()
		}
		return ctxTool.ExecuteWithContext(execCtx, params)
	}
	return tool.Execute(params)
}

// EnableContainers configures the registry to run tools inside containers.
func (r *Registry) EnableContainers(composePath, workDir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setContainerContextLocked(composePath, workDir)
	r.containerExecute = true
}

// DisableContainers disables container execution
func (r *Registry) DisableContainers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.containerExecute = false
	r.containerCompose = ""
	r.containerWorkDir = ""
}

// Close releases resources held by the registry, including sandbox containers.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	sb := r.sandbox
	r.sandbox = nil
	r.mu.Unlock()
	if sb != nil {
		return sb.Close()
	}
	return nil
}
