package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/transparency"
)

// AgentRunner executes oneshot tasks with one multi-turn tool-using agent.
// It deliberately does not claim RLM semantics: the coordinator, parallel
// subagents, shared scratchpad, and confidence loop live in pkg/rlm.Runtime.
type AgentRunner struct {
	models    *model.Manager
	registry  *tool.Registry
	ledger    *transparency.CostLedger
	modelID   string
	reasoning string
}

// AgentRunnerConfig configures the tool agent runner.
type AgentRunnerConfig struct {
	Models          *model.Manager
	Registry        *tool.Registry
	Ledger          *transparency.CostLedger
	ModelID         string
	ReasoningEffort string
}

// NewAgentRunner creates a tool agent runner.
func NewAgentRunner(cfg AgentRunnerConfig) *AgentRunner {
	return &AgentRunner{
		models:    cfg.Models,
		registry:  cfg.Registry,
		ledger:    cfg.Ledger,
		modelID:   cfg.ModelID,
		reasoning: normalizeAgentReasoningEffort(cfg.ReasoningEffort),
	}
}

// AgentResult contains the result of one tool agent execution.
type AgentResult struct {
	// Response is the final text response
	Response string

	// Incomplete reports that execution ended before a final model response.
	Incomplete bool

	// FinishReason records why the provider stopped the final model turn.
	FinishReason string

	// ToolCalls lists all tools that were called
	ToolCalls []AgentToolCall

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

// AgentToolCall is the provider-neutral record of one tool invocation.
// The oneshot API owns this shape so callers do not depend on pkg/rlm merely
// because AgentRunner reuses that runtime's subagent implementation.
type AgentToolCall struct {
	ID        string
	Name      string
	Arguments string
	Result    string
	Data      map[string]any
	Success   bool
	Duration  time.Duration
}

// Run executes a task with multi-turn tool access.
// The systemPrompt sets the agent's role/behavior.
// The task is the user's request to execute.
// allowedTools can restrict which tools are available (nil = all tools).
func (r *AgentRunner) Run(ctx context.Context, systemPrompt, task string, allowedTools []string, opts AgentExecutionOpts) (*AgentResult, error) {
	if r.models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if r.modelID == "" && strings.TrimSpace(opts.ModelID) == "" {
		return nil, fmt.Errorf("model ID required")
	}

	start := time.Now()
	traceID := fmt.Sprintf("agent-%d", time.Now().UnixNano())

	// Determine model for sub-agent
	modelToUse := strings.TrimSpace(opts.ModelID)
	if modelToUse == "" {
		modelToUse = r.modelID
	}
	providerID := r.models.ProviderIDForModel(modelToUse)
	agentRegistry := r.registry
	snapshotWorkDir := ""
	cleanupSnapshot := func() {}
	closeAgentRegistry := false
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
			agentRegistry, err = newReviewSnapshotRegistryWithLimits(
				snapshotRoot,
				allowedTools,
				opts.VerificationTimeout,
				r.models.ReviewSandboxCommand(),
			)
			if err != nil {
				cleanupSnapshot()
				return nil, err
			}
			closeAgentRegistry = true
		}
	}
	defer cleanupSnapshot()
	if closeAgentRegistry {
		defer func() { _ = agentRegistry.Close() }()
	}

	// Create sub-agent configuration
	reasoningEffort := r.reasoning
	if override := normalizeAgentReasoningEffort(opts.ReasoningEffort); override != "" {
		reasoningEffort = override
	}
	maxCostUSD := effectiveAgentMaxCostUSD(providerID, opts.MaxCostUSD)
	requestedOutputTokens := opts.MaxOutputTokens
	if requestedOutputTokens <= 0 {
		requestedOutputTokens = reviewAgentOutputTokenLimit(opts.ReasoningMaxTokens)
	}
	maxOutputTokens := effectiveAgentOutputTokenLimit(r.models, modelToUse, requestedOutputTokens)
	agentCfg := rlm.SubAgentConfig{
		ID:                   fmt.Sprintf("oneshot-%d", time.Now().UnixNano()),
		SessionID:            traceID,
		Model:                modelToUse,
		Reasoning:            reasoningEffort,
		ReasoningMaxTokens:   opts.ReasoningMaxTokens,
		MaxOutputTokens:      maxOutputTokens,
		SystemPrompt:         systemPrompt,
		MaxIterations:        opts.MaxIterations,
		MaxToolCalls:         opts.MaxToolCalls,
		MaxVerificationCalls: opts.MaxVerificationCalls,
		MaxCostUSD:           maxCostUSD,
		Adaptive:             opts.MaxIterations <= 0 || opts.SynthesisLead > 0,
		ExplorationTimeout:   opts.ExplorationTimeout,
		SynthesisLead:        opts.SynthesisLead,
		AllowedTools:         allowedTools,
		ReviewSnapshot:       opts.ReviewSnapshot,
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
	agentResult, executionErr := agent.Execute(ctx, task)
	if opts.ReviewSnapshot != nil && snapshotWorkDir != "" {
		verifyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		verifyErr := model.VerifyReviewWorkspace(verifyCtx, snapshotWorkDir, opts.ReviewSnapshot)
		cancel()
		if verifyErr != nil {
			return nil, fmt.Errorf("API review changed the captured source snapshot: %w", verifyErr)
		}
	}
	if agentResult == nil {
		if executionErr != nil {
			return nil, fmt.Errorf("execute task: %w", executionErr)
		}
		return nil, fmt.Errorf("execute task returned no result")
	}

	duration := time.Since(start)
	response := agentResult.Summary
	if executionErr != nil {
		response = formatIncompleteAgentResponse(agentResult, executionErr)
	}

	// Build result
	result := &AgentResult{
		Response:          response,
		Incomplete:        executionErr != nil,
		FinishReason:      agentResult.FinishReason,
		ToolCalls:         convertAgentToolCalls(agentResult.ToolCalls),
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
	builder.WithRequest(&transparency.RequestTrace{
		Tools:              toolNames,
		MaxTokens:          maxOutputTokens,
		ReasoningMaxTokens: opts.ReasoningMaxTokens,
	})

	builder.WithContent(response)
	builder.WithResponse(&transparency.ResponseTrace{
		FinishReason: agentResult.FinishReason,
	})

	// Calculate API cost only when the provider publishes token pricing.
	// Native Codex runs through the user's CLI subscription.
	cost := 0.0
	if providerID != "codex" {
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
		cost = effectiveAgentInvocationCost(providerID, pricing, tokens)
	}

	result.Trace = builder.Complete(tokens, cost)
	result.Trace.Duration = duration

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

	if executionErr != nil {
		return result, fmt.Errorf("execute task: %w", executionErr)
	}
	return result, nil
}

// CollectAgentEvidence executes a host-owned evidence plan against the same
// immutable snapshot boundary used by the review agent. The model never has to
// remember to request these calls, and later validation repairs can reuse the
// results without rerunning them.
func (r *AgentRunner) CollectAgentEvidence(ctx context.Context, requests []AgentEvidenceRequest, opts AgentExecutionOpts) ([]AgentToolCall, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	if opts.ReviewSnapshot == nil {
		return nil, fmt.Errorf("agent evidence collection requires an immutable review snapshot")
	}

	workDir, cleanup, err := model.PrepareReviewWorkspace(ctx, opts.ReviewSnapshot)
	if err != nil {
		return nil, fmt.Errorf("materialize agent evidence snapshot: %w", err)
	}
	defer cleanup()
	root, err := model.ReviewWorkspaceRepositoryRoot(ctx, workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve agent evidence snapshot root: %w", err)
	}

	allowedTools := make([]string, 0, len(requests))
	seenTools := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		name := strings.TrimSpace(request.Tool)
		if name == "" {
			return nil, fmt.Errorf("agent evidence request tool is required")
		}
		if _, seen := seenTools[name]; seen {
			continue
		}
		seenTools[name] = struct{}{}
		allowedTools = append(allowedTools, name)
	}

	codexCommand := ""
	if r != nil && r.models != nil {
		codexCommand = r.models.ReviewSandboxCommand()
	}
	registry, err := newReviewSnapshotRegistryWithLimits(root, allowedTools, opts.VerificationTimeout, codexCommand)
	if err != nil {
		return nil, fmt.Errorf("create agent evidence registry: %w", err)
	}
	defer func() { _ = registry.Close() }()

	calls := make([]AgentToolCall, len(requests))
	jobs := make(chan int)
	workers := min(len(requests), 4)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for index := range jobs {
				calls[index] = collectAgentEvidenceRequest(ctx, registry, index, requests[index])
			}
		}()
	}
	for index := range requests {
		jobs <- index
	}
	close(jobs)
	wg.Wait()

	verifyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	verifyErr := model.VerifyReviewWorkspace(verifyCtx, workDir, opts.ReviewSnapshot)
	cancel()
	if verifyErr != nil {
		return nil, fmt.Errorf("agent evidence collection changed the captured source snapshot: %w", verifyErr)
	}
	return calls, nil
}

func collectAgentEvidenceRequest(ctx context.Context, registry *tool.Registry, index int, request AgentEvidenceRequest) AgentToolCall {
	callID := fmt.Sprintf("host-evidence-%d", index+1)
	parameters := make(map[string]any, len(request.Parameters)+1)
	for key, value := range request.Parameters {
		parameters[key] = value
	}
	arguments, _ := json.Marshal(request.Parameters)
	parameters[tool.ToolCallIDParam] = callID

	started := time.Now()
	result, err := registry.ExecuteWithContext(ctx, strings.TrimSpace(request.Tool), parameters)
	call := AgentToolCall{
		ID:        callID,
		Name:      strings.TrimSpace(request.Tool),
		Arguments: string(arguments),
		Duration:  time.Since(started),
	}
	if err != nil {
		call.Result = fmt.Sprintf("execution error: %v", err)
		return call
	}
	if result == nil {
		call.Result = "execution returned no result"
		return call
	}
	call.Success = result.Success
	call.Data = make(map[string]any, len(result.Data))
	for key, value := range result.Data {
		call.Data[key] = value
	}
	encoded, encodeErr := tool.ToModelOutput(result)
	if encodeErr != nil {
		call.Result = fmt.Sprintf("encode evidence result: %v", encodeErr)
		return call
	}
	call.Result = encoded
	return call
}

func convertAgentToolCalls(calls []rlm.SubAgentToolCall) []AgentToolCall {
	if len(calls) == 0 {
		return nil
	}
	converted := make([]AgentToolCall, len(calls))
	for i, call := range calls {
		converted[i] = AgentToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
			Result:    call.Result,
			Data:      call.Data,
			Success:   call.Success,
			Duration:  call.Duration,
		}
	}
	return converted
}

func reviewAgentOutputTokenLimit(reasoningMaxTokens int) int {
	if reasoningMaxTokens <= 0 {
		return 0
	}
	// OpenRouter counts reasoning against the completion limit. Keep the
	// existing final-answer allowance in addition to the thinking budget.
	return reasoningMaxTokens + 4096
}

// effectiveAgentOutputTokenLimit applies an explicit task budget and then caps
// it with the provider's advertised completion ceiling when available. A
// missing catalog entry is deliberately non-fatal: providers that do not
// publish a limit retain the caller's requested budget.
func effectiveAgentOutputTokenLimit(models *model.Manager, modelID string, configured int) int {
	if configured <= 0 {
		return configured
	}
	if models == nil {
		return configured
	}
	info, err := models.GetModelInfo(modelID)
	if err != nil || info == nil || info.MaxCompletionTokens <= 0 {
		return configured
	}
	return clampAgentOutputTokenLimit(configured, info.MaxCompletionTokens)
}

func clampAgentOutputTokenLimit(configured, providerMax int) int {
	if configured <= 0 || providerMax <= 0 {
		return configured
	}
	return min(configured, providerMax)
}

func effectiveAgentMaxCostUSD(providerID string, configured float64) float64 {
	if providerID == "codex" {
		// Native Codex execution has no per-token API price that Buckley can
		// enforce. Keep its turn, tool, and elapsed-time budgets authoritative.
		return 0
	}
	return configured
}

func effectiveAgentInvocationCost(providerID string, pricing transparency.ModelPricing, tokens transparency.TokenUsage) float64 {
	if providerID == "codex" {
		return 0
	}
	return pricing.Calculate(tokens)
}

func formatIncompleteAgentResponse(result *rlm.SubAgentResult, cause error) string {
	var b strings.Builder
	b.WriteString("> [!WARNING]\n")
	b.WriteString("> **Incomplete agent result — salvaged from completed work.**\n")
	b.WriteString("> The run ended before validation completed. This artifact is not a completed or validated result.\n\n")
	b.WriteString("## Interruption\n\n")
	b.WriteString(salvageText(cause.Error(), 2000))
	b.WriteString("\n")

	if summary := strings.TrimSpace(result.Summary); summary != "" {
		b.WriteString("\n## Partial Model Output\n\n")
		b.WriteString(salvageText(summary, 8000))
		b.WriteString("\n")
	}

	if len(result.ToolCalls) > 0 {
		b.WriteString("\n## Completed Evidence\n")
		start := max(0, len(result.ToolCalls)-12)
		for _, call := range result.ToolCalls[start:] {
			status := "failed"
			if call.Success {
				status = "completed"
			}
			b.WriteString("\n- `")
			b.WriteString(salvageText(call.Name, 200))
			b.WriteString("` — ")
			b.WriteString(status)
			if arguments := strings.TrimSpace(call.Arguments); arguments != "" {
				b.WriteString("\n  - Arguments: `")
				b.WriteString(salvageText(arguments, 800))
				b.WriteString("`")
			}
			if output := strings.TrimSpace(call.Result); output != "" {
				b.WriteString("\n  - Result:\n\n    ```text\n    ")
				output = strings.ReplaceAll(salvageText(output, 4000), "\n", "\n    ")
				b.WriteString(output)
				b.WriteString("\n    ```\n")
			}
		}
	}

	b.WriteString("\n## Accounting\n\n")
	fmt.Fprintf(&b, "- Completed model tokens: %d input, %d output, %d total\n", result.InputTokens, result.OutputTokens, result.TokensUsed)
	fmt.Fprintf(&b, "- Completed tool calls retained: %d\n", len(result.ToolCalls))
	return strings.TrimSpace(b.String()) + "\n"
}

func salvageText(value string, limit int) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "```", "` ` `")
	runes := []rune(value)
	if limit > 0 && len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func newReviewSnapshotRegistry(root string, allowedTools []string, codexCommand ...string) (*tool.Registry, error) {
	return newReviewSnapshotRegistryWithLimits(root, allowedTools, 0, codexCommand...)
}

func newReviewSnapshotRegistryWithLimits(root string, allowedTools []string, verificationTimeout time.Duration, codexCommand ...string) (*tool.Registry, error) {
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
		if verificationTimeout > 0 {
			verification.SetTimeoutLimit(verificationTimeout)
		}
		registry.Register(verification)
		registry.SetToolKind(verification.Name(), "execute")
	}
	registry.SetWorkDir(root)
	return registry, nil
}

func normalizeAgentReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

// RunWithAudit executes a task and includes context audit in the trace.
func (r *AgentRunner) RunWithAudit(ctx context.Context, systemPrompt, task string, allowedTools []string, audit *transparency.ContextAudit) (*AgentResult, error) {
	result, err := r.Run(ctx, systemPrompt, task, allowedTools, AgentExecutionOpts{})
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
