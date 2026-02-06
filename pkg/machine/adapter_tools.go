package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

const toolCallIDParam = "__buckley_tool_call_id"

// ToolRegistry is the minimal interface for tool execution.
// *tool.Registry satisfies this interface.
type ToolRegistry interface {
	ExecuteWithContext(ctx context.Context, name string, params map[string]any) (*builtin.Result, error)
}

// RegistryToolExecutor adapts a ToolRegistry to the ToolBatchExecutor interface.
// It executes tool calls concurrently with a configurable parallelism limit.
type RegistryToolExecutor struct {
	Registry    ToolRegistry
	MaxParallel int
}

func (e *RegistryToolExecutor) maxParallel() int {
	if e.MaxParallel <= 0 {
		return 5
	}
	return e.MaxParallel
}

// Execute runs all tool calls concurrently and collects results.
func (e *RegistryToolExecutor) Execute(ctx context.Context, calls []ToolCallRequest) ToolsCompleted {
	if len(calls) == 0 {
		return ToolsCompleted{}
	}

	results := make([]ToolCallResult, len(calls))
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.maxParallel())

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c ToolCallRequest) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[idx] = ToolCallResult{
						ID:   c.ID,
						Name: c.Name,
						Err:  fmt.Errorf("panic in tool %s: %v", c.Name, r),
					}
				}
			}()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = ToolCallResult{
					ID:   c.ID,
					Name: c.Name,
					Err:  ctx.Err(),
				}
				return
			}

			params := make(map[string]any, len(c.Params)+1)
			maps.Copy(params, c.Params)
			params[toolCallIDParam] = c.ID

			res, err := e.Registry.ExecuteWithContext(ctx, c.Name, params)
			results[idx] = buildToolResult(c, res, err)
		}(i, call)
	}
	wg.Wait()

	return ToolsCompleted{Results: results}
}

func buildToolResult(call ToolCallRequest, res *builtin.Result, err error) ToolCallResult {
	r := ToolCallResult{
		ID:   call.ID,
		Name: call.Name,
	}
	if err != nil {
		r.Err = err
		r.Result = err.Error()
		return r
	}
	if res == nil {
		r.Success = true
		return r
	}
	r.Success = res.Success
	if !res.Success {
		r.Result = res.Error
		return r
	}
	if len(res.Data) > 0 {
		if data, jsonErr := json.Marshal(res.Data); jsonErr == nil {
			r.Result = string(data)
		}
	}
	return r
}
