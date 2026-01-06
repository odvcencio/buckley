package rlm

import (
	"context"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
	"github.com/odvcencio/buckley/pkg/transparency"
)

const defaultMaxIterations = 3

// Invoker issues model requests for a one-shot tool call.
type Invoker interface {
	Invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error)
}

// InvokeToolLoop retries tool calls with a guidance prompt when missing a tool invocation.
func InvokeToolLoop(ctx context.Context, invoker Invoker, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit, maxIterations int) (*oneshot.Result, *transparency.Trace, error) {
	maxIterations = clampIterations(maxIterations)
	basePrompt := userPrompt
	prompt := basePrompt
	var lastResult *oneshot.Result
	var lastTrace *transparency.Trace

	for attempt := 0; attempt < maxIterations; attempt++ {
		result, trace, err := invoker.Invoke(ctx, systemPrompt, prompt, tool, audit)
		lastResult = result
		lastTrace = trace
		if err != nil {
			return result, trace, err
		}
		if result != nil && result.ToolCall != nil {
			return result, trace, nil
		}
		prompt = appendGuidance(basePrompt, "IMPORTANT: You MUST call the "+tool.Name+" tool. Do not reply with plain text.")
	}

	return lastResult, lastTrace, nil
}

func appendGuidance(basePrompt, guidance string) string {
	guidance = strings.TrimSpace(guidance)
	if guidance == "" {
		return basePrompt
	}
	return basePrompt + "\n\n" + guidance
}

func clampIterations(maxIterations int) int {
	if maxIterations <= 0 {
		return defaultMaxIterations
	}
	if maxIterations > 10 {
		return 10
	}
	return maxIterations
}
