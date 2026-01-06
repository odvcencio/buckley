package rlm

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot/commit"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// CommitRunner executes commit generation with an RLM-style retry loop.
type CommitRunner struct {
	invoker       Invoker
	ledger        *transparency.CostLedger
	maxIterations int
}

// CommitRunnerConfig configures the commit runner.
type CommitRunnerConfig struct {
	Invoker       Invoker
	Ledger        *transparency.CostLedger
	MaxIterations int
}

// NewCommitRunner creates a commit runner.
func NewCommitRunner(cfg CommitRunnerConfig) *CommitRunner {
	return &CommitRunner{
		invoker:       cfg.Invoker,
		ledger:        cfg.Ledger,
		maxIterations: cfg.MaxIterations,
	}
}

// Run executes the commit generation flow with validation retries.
func (r *CommitRunner) Run(ctx context.Context, opts commit.ContextOptions) (*commit.RunResult, error) {
	result := &commit.RunResult{}

	commitCtx, audit, err := commit.AssembleContext(opts)
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}
	result.ContextAudit = audit
	result.Warnings = commitCtx.Warnings

	basePrompt := commit.BuildPrompt(commitCtx)
	systemPrompt := commit.SystemPrompt()
	maxIterations := clampIterations(r.maxIterations)
	prompt := basePrompt

	for attempt := 0; attempt < maxIterations; attempt++ {
		invokeResult, trace, err := r.invoker.Invoke(ctx, systemPrompt, prompt, commit.GenerateCommitTool, audit)
		result.Trace = trace
		if err != nil {
			result.Error = err
			return result, nil
		}

		if !invokeResult.HasToolCall() {
			result.Error = &transparency.NoToolCallError{
				Expected: commit.GenerateCommitTool.Name,
				Got:      "text response",
			}
			prompt = appendGuidance(basePrompt, "IMPORTANT: You MUST call the "+commit.GenerateCommitTool.Name+" tool. Do not reply with plain text.")
			continue
		}

		var parsed commit.CommitResult
		if err := invokeResult.ToolCall.Unmarshal(&parsed); err != nil {
			result.Error = fmt.Errorf("unmarshal tool call: %w", err)
			prompt = appendGuidance(basePrompt, "The tool call arguments must be valid JSON that matches the schema for "+commit.GenerateCommitTool.Name+".")
			continue
		}
		if err := parsed.Validate(); err != nil {
			result.Error = fmt.Errorf("invalid commit: %w", err)
			prompt = appendGuidance(basePrompt, "The generated commit message failed validation: "+strings.TrimSpace(err.Error())+". Fix and call "+commit.GenerateCommitTool.Name+" again.")
			continue
		}

		result.Commit = &parsed
		result.Error = nil
		return result, nil
	}

	return result, nil
}

// RunSimple is a convenience method for simple usage.
func (r *CommitRunner) RunSimple(ctx context.Context) (*commit.CommitResult, error) {
	result, err := r.Run(ctx, commit.DefaultContextOptions())
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return result.Commit, nil
}
