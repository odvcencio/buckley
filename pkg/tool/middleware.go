package tool

import (
	"context"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ExecutionContext carries request metadata through the middleware chain.
type ExecutionContext struct {
	Context   context.Context
	ToolName  string
	Tool      Tool
	SessionID string
	CallID    string
	Params    map[string]any
	StartTime time.Time
	Attempt   int
	Metadata  map[string]any
}

// Executor is the function signature for tool execution.
type Executor func(ctx *ExecutionContext) (*builtin.Result, error)

// Middleware wraps an Executor with additional behavior.
type Middleware func(next Executor) Executor

// ContextTool is an optional interface for tools that accept contexts.
type ContextTool interface {
	ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error)
}

// Chain composes middlewares in order (first middleware is outermost).
func Chain(middlewares ...Middleware) Middleware {
	return func(final Executor) Executor {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
