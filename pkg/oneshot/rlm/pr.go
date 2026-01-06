package rlm

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot/pr"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// PRRunner executes PR generation with an RLM-style retry loop.
type PRRunner struct {
	invoker       Invoker
	ledger        *transparency.CostLedger
	maxIterations int
}

// PRRunnerConfig configures the PR runner.
type PRRunnerConfig struct {
	Invoker       Invoker
	Ledger        *transparency.CostLedger
	MaxIterations int
}

// NewPRRunner creates a PR runner.
func NewPRRunner(cfg PRRunnerConfig) *PRRunner {
	return &PRRunner{
		invoker:       cfg.Invoker,
		ledger:        cfg.Ledger,
		maxIterations: cfg.MaxIterations,
	}
}

// Run executes the PR generation flow with validation retries.
func (r *PRRunner) Run(ctx context.Context, opts pr.ContextOptions) (*pr.RunResult, error) {
	result := &pr.RunResult{}

	prCtx, audit, err := pr.AssembleContext(opts)
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}
	result.Context = prCtx
	result.ContextAudit = audit

	basePrompt := pr.BuildPrompt(prCtx)
	systemPrompt := pr.SystemPrompt()
	maxIterations := clampIterations(r.maxIterations)
	prompt := basePrompt

	for attempt := 0; attempt < maxIterations; attempt++ {
		invokeResult, trace, err := r.invoker.Invoke(ctx, systemPrompt, prompt, pr.GeneratePRTool, audit)
		result.Trace = trace
		if err != nil {
			result.Error = err
			return result, nil
		}

		if !invokeResult.HasToolCall() {
			result.Error = &transparency.NoToolCallError{
				Expected: pr.GeneratePRTool.Name,
				Got:      "text response",
			}
			prompt = appendGuidance(basePrompt, "IMPORTANT: You MUST call the "+pr.GeneratePRTool.Name+" tool. Do not reply with plain text.")
			continue
		}

		var parsed pr.PRResult
		if err := invokeResult.ToolCall.Unmarshal(&parsed); err != nil {
			result.Error = fmt.Errorf("unmarshal tool call: %w", err)
			prompt = appendGuidance(basePrompt, "The tool call arguments must be valid JSON that matches the schema for "+pr.GeneratePRTool.Name+".")
			continue
		}
		if err := parsed.Validate(); err != nil {
			result.Error = fmt.Errorf("invalid PR: %w", err)
			prompt = appendGuidance(basePrompt, "The generated PR failed validation: "+strings.TrimSpace(err.Error())+". Fix and call "+pr.GeneratePRTool.Name+" again.")
			continue
		}

		result.PR = &parsed
		result.Error = nil
		return result, nil
	}

	return result, nil
}

// RunSimple is a convenience method for simple usage.
func (r *PRRunner) RunSimple(ctx context.Context) (*pr.PRResult, error) {
	result, err := r.Run(ctx, pr.DefaultContextOptions())
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return result.PR, nil
}
