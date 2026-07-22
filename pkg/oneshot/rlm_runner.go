package oneshot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/transparency"
)

// RLMRunner executes oneshot tasks using the full RLM tool ecosystem.
// This provides access to all built-in tools (read, write, bash, glob, grep, etc.)
// instead of limited custom tool definitions.
type RLMRunner struct {
	models    *model.Manager
	registry  *tool.Registry
	ledger    *transparency.CostLedger
	modelID   string
	reasoning string
}

// RLMRunnerConfig configures the RLM runner.
type RLMRunnerConfig struct {
	Models          *model.Manager
	Registry        *tool.Registry
	Ledger          *transparency.CostLedger
	ModelID         string
	ReasoningEffort string
}

// NewRLMRunner creates an RLM-based runner.
func NewRLMRunner(cfg RLMRunnerConfig) *RLMRunner {
	return &RLMRunner{
		models:    cfg.Models,
		registry:  cfg.Registry,
		ledger:    cfg.Ledger,
		modelID:   cfg.ModelID,
		reasoning: normalizeRLMReasoningEffort(cfg.ReasoningEffort),
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

	// InputTokens and OutputTokens preserve the provider-reported split used
	// for trace and cost attribution.
	InputTokens  int
	OutputTokens int

	// Duration is how long execution took
	Duration time.Duration

	// Trace contains transparency data
	Trace *transparency.Trace

	// ProviderID identifies whether verification came from a native Codex
	// workspace or an API model using explicit snapshot tools.
	ProviderID string

	// ExecutionEvidence contains native provider command events. API providers
	// instead contribute explicit ToolCalls from the constrained verification
	// tool.
	ExecutionEvidence []model.CommandExecutionEvidence
}

// Run executes a task with full RLM tool access.
// The systemPrompt sets the agent's role/behavior.
// The task is the user's request to execute.
// allowedTools can restrict which tools are available (nil = all tools).
func (r *RLMRunner) Run(ctx context.Context, systemPrompt, task string, allowedTools []string, opts RLMExecutionOpts) (*RLMResult, error) {
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
	providerID := r.models.ProviderIDForModel(modelToUse)
	agentRegistry := r.registry
	snapshotWorkDir := ""
	cleanupSnapshot := func() {}
	if opts.ReviewSnapshot != nil {
		if providerID == "codex" || strings.HasPrefix(modelToUse, "codex/") {
			// Codex uses its native shell in a separately materialized workspace;
			// never expose the live API-tool registry alongside it.
			agentRegistry = tool.NewEmptyRegistry()
		} else {
			var err error
			snapshotWorkDir, cleanupSnapshot, err = model.PrepareReviewWorkspace(ctx, opts.ReviewSnapshot)
			if err != nil {
				return nil, fmt.Errorf("materialize API review snapshot: %w", err)
			}
			snapshotRoot, rootErr := model.ReviewWorkspaceRepositoryRoot(ctx, snapshotWorkDir)
			if rootErr != nil {
				cleanupSnapshot()
				return nil, fmt.Errorf("resolve API review snapshot root: %w", rootErr)
			}
			agentRegistry, err = newReviewSnapshotRegistry(snapshotRoot, allowedTools, r.models.ReviewSandboxCommand())
			if err != nil {
				cleanupSnapshot()
				return nil, err
			}
		}
	}
	defer cleanupSnapshot()

	// Create sub-agent configuration
	maxIterations := opts.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 25
	}
	agentCfg := rlm.SubAgentInstanceConfig{
		ID:             fmt.Sprintf("oneshot-%d", time.Now().UnixNano()),
		Model:          modelToUse,
		Reasoning:      r.reasoning,
		SystemPrompt:   systemPrompt,
		MaxIterations:  maxIterations,
		AllowedTools:   allowedTools,
		ReviewSnapshot: opts.ReviewSnapshot,
	}

	// Create sub-agent with full tool access
	agent, err := rlm.NewSubAgent(agentCfg, rlm.SubAgentDeps{
		Models:   r.models,
		Registry: agentRegistry,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// Execute task
	agentResult, err := agent.Execute(ctx, task)
	if opts.ReviewSnapshot != nil && snapshotWorkDir != "" {
		verifyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		verifyErr := model.VerifyReviewWorkspace(verifyCtx, snapshotWorkDir, opts.ReviewSnapshot)
		cancel()
		if verifyErr != nil {
			return nil, fmt.Errorf("API review changed the captured source snapshot: %w", verifyErr)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("execute task: %w", err)
	}

	duration := time.Since(start)

	// Build result
	result := &RLMResult{
		Response:          agentResult.Summary,
		ToolCalls:         agentResult.ToolCalls,
		TokensUsed:        agentResult.TokensUsed,
		InputTokens:       agentResult.InputTokens,
		OutputTokens:      agentResult.OutputTokens,
		Duration:          duration,
		ProviderID:        providerID,
		ExecutionEvidence: append([]model.CommandExecutionEvidence(nil), agentResult.ExecutionEvidence...),
	}

	// Build trace for transparency
	if providerID == "" {
		providerID = "unknown"
		result.ProviderID = providerID
	}
	builder := transparency.NewTraceBuilder(traceID, modelToUse, providerID)
	tokens := transparency.TokenUsage{
		Input:  agentResult.InputTokens,
		Output: agentResult.OutputTokens,
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

func newReviewSnapshotRegistry(root string, allowedTools []string, codexCommand ...string) (*tool.Registry, error) {
	allowed := make(map[string]struct{}, len(allowedTools))
	for _, name := range allowedTools {
		name = strings.TrimSpace(name)
		switch name {
		case "read_file", "find_files", "search_text", "run_verification":
			allowed[name] = struct{}{}
		case "":
			continue
		default:
			return nil, fmt.Errorf("review snapshot tool %q is not in the exact snapshot review tool set", name)
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("review snapshot execution requires an explicit snapshot review tool set")
	}
	registry := tool.NewRegistry(tool.WithBuiltinFilter(func(candidate tool.Tool) bool {
		_, ok := allowed[candidate.Name()]
		return ok
	}))
	if _, enabled := allowed["run_verification"]; enabled {
		verification, err := builtin.NewRunVerificationTool(root, codexCommand...)
		if err != nil {
			return nil, fmt.Errorf("create sealed review verification tool: %w", err)
		}
		registry.Register(verification)
		registry.SetToolKind(verification.Name(), "execute")
	}
	registry.SetWorkDir(root)
	return registry, nil
}

func normalizeRLMReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

// RunWithAudit executes a task and includes context audit in the trace.
func (r *RLMRunner) RunWithAudit(ctx context.Context, systemPrompt, task string, allowedTools []string, audit *transparency.ContextAudit) (*RLMResult, error) {
	result, err := r.Run(ctx, systemPrompt, task, allowedTools, RLMExecutionOpts{})
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
