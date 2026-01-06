package oneshot

import (
	"context"
	"fmt"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/rlm"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// RLMRunner executes oneshot tasks using the full RLM tool ecosystem.
// This provides access to all built-in tools (read, write, bash, glob, grep, etc.)
// instead of limited custom tool definitions.
type RLMRunner struct {
	models   *model.Manager
	registry *tool.Registry
	ledger   *transparency.CostLedger
	modelID  string
}

// RLMRunnerConfig configures the RLM runner.
type RLMRunnerConfig struct {
	Models   *model.Manager
	Registry *tool.Registry
	Ledger   *transparency.CostLedger
	ModelID  string
}

// NewRLMRunner creates an RLM-based runner.
func NewRLMRunner(cfg RLMRunnerConfig) *RLMRunner {
	return &RLMRunner{
		models:   cfg.Models,
		registry: cfg.Registry,
		ledger:   cfg.Ledger,
		modelID:  cfg.ModelID,
	}
}

// RLMResult contains the result of an RLM task execution.
type RLMResult struct {
	// Response is the final text response
	Response string

	// ToolCalls lists all tools that were called
	ToolCalls []rlm.SubAgentToolCall

	// TokensUsed is the total token consumption
	TokensUsed int

	// Duration is how long execution took
	Duration time.Duration

	// Trace contains transparency data
	Trace *transparency.Trace
}

// Run executes a task with full RLM tool access.
// The systemPrompt sets the agent's role/behavior.
// The task is the user's request to execute.
// allowedTools can restrict which tools are available (nil = all tools).
func (r *RLMRunner) Run(ctx context.Context, systemPrompt, task string, allowedTools []string) (*RLMResult, error) {
	if r.models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if r.modelID == "" {
		return nil, fmt.Errorf("model ID required")
	}

	start := time.Now()
	traceID := fmt.Sprintf("rlm-%d", time.Now().UnixNano())

	// Determine model for sub-agent
	modelToUse := r.modelID

	// Create sub-agent configuration
	agentCfg := rlm.SubAgentConfig{
		ID:            fmt.Sprintf("oneshot-%d", time.Now().UnixNano()),
		Model:         modelToUse,
		SystemPrompt:  systemPrompt,
		MaxIterations: 25, // Allow more iterations for complex tasks
		AllowedTools:  allowedTools,
	}

	// Create sub-agent with full tool access
	agent, err := rlm.NewSubAgent(agentCfg, rlm.SubAgentDeps{
		Models:   r.models,
		Registry: r.registry,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// Execute task
	agentResult, err := agent.Execute(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("execute task: %w", err)
	}

	duration := time.Since(start)

	// Build result
	result := &RLMResult{
		Response:   agentResult.Summary,
		ToolCalls:  agentResult.ToolCalls,
		TokensUsed: agentResult.TokensUsed,
		Duration:   duration,
	}

	// Build trace for transparency
	builder := transparency.NewTraceBuilder(traceID, modelToUse, "openrouter")
	tokens := transparency.TokenUsage{
		Input:  agentResult.TokensUsed / 2, // Rough split
		Output: agentResult.TokensUsed / 2,
	}

	// Extract tool names for trace
	var toolNames []string
	for _, tc := range agentResult.ToolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	if len(toolNames) > 0 {
		builder.WithRequest(&transparency.RequestTrace{
			Tools: toolNames,
		})
	}

	builder.WithContent(agentResult.Summary)

	// Calculate cost (rough estimate)
	pricing := transparency.ModelPricing{
		InputPerMillion:  3.0,
		OutputPerMillion: 15.0,
	}
	if r.models != nil {
		if info, err := r.models.GetModelInfo(modelToUse); err == nil {
			pricing.InputPerMillion = info.Pricing.Prompt
			pricing.OutputPerMillion = info.Pricing.Completion
		}
	}
	cost := pricing.Calculate(tokens)

	result.Trace = builder.Complete(tokens, cost)

	// Record in ledger
	if r.ledger != nil {
		r.ledger.Record(transparency.CostEntry{
			Model:        modelToUse,
			Tokens:       tokens,
			Cost:         cost,
			Latency:      duration,
			InvocationID: traceID,
		})
	}

	return result, nil
}

// RunWithAudit executes a task and includes context audit in the trace.
func (r *RLMRunner) RunWithAudit(ctx context.Context, systemPrompt, task string, allowedTools []string, audit *transparency.ContextAudit) (*RLMResult, error) {
	result, err := r.Run(ctx, systemPrompt, task, allowedTools)
	if err != nil {
		return nil, err
	}

	// Attach audit to trace if available
	if result.Trace != nil && audit != nil {
		// Note: Would need to extend TraceBuilder to support this
		// For now, audit is tracked separately
	}

	return result, nil
}
