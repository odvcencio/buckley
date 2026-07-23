package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/coordination/security"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

const (
	defaultSubAgentMaxIterations = 25
	defaultFinalSynthesisLead    = 90 * time.Second

	defaultSubAgentPrompt = `You are a Buckley sub-agent executing a specific task delegated by the coordinator.

## Your Mission
Complete the assigned task using available tools, then provide a concise summary of what you accomplished.

## Execution Guidelines

1. **Read Before Writing**: Always read files before modifying them
2. **Verify Changes**: After edits, confirm the change was applied correctly
3. **Handle Errors**: If a tool fails, try an alternative approach or report the issue
4. **Stay Focused**: Only do what's asked - don't expand scope
5. **Be Efficient**: Use the minimum number of tool calls needed

## Tool Usage Patterns

**File Operations**:
- Read the file first to understand context
- Make targeted edits rather than full rewrites
- Verify changes compiled/work if possible

**Shell Commands**:
- Prefer specific commands over exploratory ones
- Capture and report relevant output
- Handle non-zero exit codes gracefully

**Search/Analysis**:
- Start with narrow searches, broaden if needed
- Report findings even if partial

## Summary Format

End your response with a clear summary:
- What you did (actions taken)
- What you found (for analysis tasks)
- What changed (for modification tasks)
- Any issues encountered

Keep summaries under 200 words - the coordinator only sees this summary, not your full output.`
)

// SubAgent executes delegated tasks with tool access.
type SubAgent struct {
	id             string
	model          string
	systemPrompt   string
	reasoning      string
	maxIterations  int
	maxCostUSD     float64
	adaptive       bool
	synthesisLead  time.Duration
	allowedTools   map[string]struct{}
	readOnly       bool
	reviewSnapshot *model.ReviewSnapshot
	toolTier       string

	client     *model.Manager
	registry   *tool.Registry
	scratchpad ScratchpadWriter
	conflicts  *ConflictDetector
	approver   *security.ToolApprover
	engine     *rules.Engine
}

// SubAgentConfig configures a sub-agent execution.
type SubAgentConfig struct {
	ID             string
	Model          string
	Reasoning      string
	SystemPrompt   string
	MaxIterations  int
	MaxCostUSD     float64
	Adaptive       bool
	SynthesisLead  time.Duration
	AllowedTools   []string
	ReviewSnapshot *model.ReviewSnapshot
	ToolTier       string // role_permissions tier for runtime validation
}

// SubAgentInstanceConfig preserves the merged oneshot runner API.
type SubAgentInstanceConfig = SubAgentConfig

// SubAgentDeps provides shared dependencies.
type SubAgentDeps struct {
	Models     *model.Manager
	Registry   *tool.Registry
	Scratchpad ScratchpadWriter
	Conflicts  *ConflictDetector
	Approver   *security.ToolApprover
	Engine     *rules.Engine
}

// SubAgentResult captures the outcome of a sub-agent task.
type SubAgentResult struct {
	AgentID           string
	ModelUsed         string
	Summary           string
	RawKey            string
	Raw               []byte
	TokensUsed        int
	InputTokens       int
	OutputTokens      int
	Duration          time.Duration
	ToolCalls         []SubAgentToolCall
	ExecutionEvidence []model.CommandExecutionEvidence
}

// SubAgentToolCall records a tool invocation.
type SubAgentToolCall struct {
	ID        string
	Name      string
	Arguments string
	Result    string
	Data      map[string]any
	Success   bool
	Duration  time.Duration
}

// NewSubAgent creates a sub-agent with dependencies.
func NewSubAgent(cfg SubAgentConfig, deps SubAgentDeps) (*SubAgent, error) {
	if strings.TrimSpace(cfg.ID) == "" {
		return nil, fmt.Errorf("sub-agent ID required")
	}
	if deps.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if deps.Registry == nil {
		return nil, fmt.Errorf("tool registry required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("model required")
	}

	prompt := strings.TrimSpace(cfg.SystemPrompt)
	if prompt == "" {
		prompt = defaultSubAgentPrompt
	}

	maxIterations := cfg.MaxIterations
	if maxIterations <= 0 && !cfg.Adaptive {
		maxIterations = defaultSubAgentMaxIterations
	}
	synthesisLead := cfg.SynthesisLead
	if cfg.Adaptive && synthesisLead <= 0 {
		synthesisLead = defaultFinalSynthesisLead
	}

	allowedTools := make(map[string]struct{})
	for _, name := range cfg.AllowedTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		allowedTools[name] = struct{}{}
	}

	return &SubAgent{
		id:             cfg.ID,
		model:          cfg.Model,
		systemPrompt:   prompt,
		reasoning:      normalizeSubAgentReasoning(cfg.Reasoning),
		maxIterations:  maxIterations,
		maxCostUSD:     cfg.MaxCostUSD,
		adaptive:       cfg.Adaptive,
		synthesisLead:  synthesisLead,
		allowedTools:   allowedTools,
		readOnly:       isReadOnlyToolSet(cfg.AllowedTools) || cfg.ReviewSnapshot != nil,
		reviewSnapshot: cfg.ReviewSnapshot,
		toolTier:       cfg.ToolTier,
		client:         deps.Models,
		registry:       deps.Registry,
		scratchpad:     deps.Scratchpad,
		conflicts:      deps.Conflicts,
		approver:       deps.Approver,
		engine:         deps.Engine,
	}, nil
}

func normalizeSubAgentReasoning(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

// Execute runs the task to completion and returns a summary for the coordinator.
func (a *SubAgent) Execute(ctx context.Context, task string) (*SubAgentResult, error) {
	start := time.Now()
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("task required")
	}

	allowedRegistry, allowedSet := a.allowedRegistry(ctx)
	toolDefs := buildToolDefinitions(allowedRegistry, allowedSet)

	messages := []model.Message{
		{Role: "system", Content: a.systemPrompt},
		{Role: "user", Content: task},
	}

	result := &SubAgentResult{
		AgentID:   a.id,
		ModelUsed: a.model,
	}
	contextWindow, _ := a.client.GetContextLength(a.model)
	maxIterations := a.maxIterations
	if maxIterations <= 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline && a.maxCostUSD <= 0 {
			maxIterations = defaultSubAgentMaxIterations
		}
	}

	for i := 0; maxIterations <= 0 || i < maxIterations; i++ {
		req := model.ChatRequest{
			Model:     a.model,
			Tools:     toolDefs,
			SessionID: "rlm-subagent-" + a.id,
			ToolChoice: func() string {
				if len(toolDefs) == 0 {
					return "none"
				}
				return "auto"
			}(),
		}
		requestMessages := messages
		if a.shouldSynthesize(ctx, i, maxIterations, start) {
			req.Tools = nil
			req.ToolChoice = "none"
			requestMessages = finalSynthesisMessages(messages)
		}
		applyExecutionPolicy(&req, a.readOnly, a.reviewSnapshot)
		if a.reasoning != "" {
			req.Reasoning = &model.ReasoningConfig{Effort: a.reasoning}
		}
		req.Messages = conversation.CompactModelMessagesForRequest(requestMessages, req, contextWindow)
		if err := a.applyCostBudget(&req, result); err != nil {
			finalizeSubAgentResult(result, start)
			return result, err
		}
		resp, err := a.client.ChatCompletion(ctx, req)
		if err != nil {
			finalizeSubAgentResult(result, start)
			return result, err
		}
		result.InputTokens += resp.Usage.PromptTokens
		result.OutputTokens += resp.Usage.CompletionTokens
		result.ExecutionEvidence = append(result.ExecutionEvidence, resp.ExecutionEvidence...)
		turnTokens := resp.Usage.TotalTokens
		if turnTokens == 0 {
			turnTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		}
		result.TokensUsed += turnTokens

		if len(resp.Choices) == 0 {
			finalizeSubAgentResult(result, start)
			return result, fmt.Errorf("no response from model")
		}

		choice := resp.Choices[0]

		if len(choice.Message.ToolCalls) > 0 {
			toolResults, err := a.executeTools(ctx, choice.Message.ToolCalls, allowedRegistry, allowedSet, result)
			if err != nil {
				finalizeSubAgentResult(result, start)
				return result, err
			}

			messages = append(messages, model.Message{
				Role:      "assistant",
				Content:   choice.Message.Content,
				ToolCalls: choice.Message.ToolCalls,
			})
			for _, tr := range toolResults {
				messages = append(messages, model.Message{
					Role:       "tool",
					ToolCallID: tr.ID,
					Name:       tr.Name,
					Content:    tr.Result,
				})
			}
			continue
		}

		content, err := model.ExtractTextContent(choice.Message.Content)
		if err != nil {
			content = fmt.Sprintf("%v", choice.Message.Content)
		}
		result.Summary = strings.TrimSpace(content)
		break
	}

	finalizeSubAgentResult(result, start)
	if a.scratchpad != nil {
		key, err := a.scratchpad.Write(ctx, WriteRequest{
			Type:      EntryTypeAnalysis,
			Raw:       result.Raw,
			Summary:   result.Summary,
			Metadata:  map[string]any{"model": a.model, "agent_id": a.id},
			CreatedBy: a.id,
			CreatedAt: time.Now(),
		})
		if err == nil {
			result.RawKey = key
		}
	}

	return result, nil
}

func (a *SubAgent) shouldSynthesize(ctx context.Context, iteration, maxIterations int, startedAt time.Time) bool {
	if maxIterations > 0 && iteration == maxIterations-1 {
		return true
	}
	if !a.adaptive || a.synthesisLead <= 0 {
		return false
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return false
	}
	lead := a.synthesisLead
	if proportionalLead := deadline.Sub(startedAt) / 3; proportionalLead < lead {
		lead = proportionalLead
	}
	return time.Until(deadline) <= lead
}

func finalSynthesisMessages(messages []model.Message) []model.Message {
	final := append([]model.Message(nil), messages...)
	return append(final, model.Message{
		Role: "user",
		Content: "FINAL SYNTHESIS: Tool use is complete. Return the complete final answer now. " +
			"Do not request another tool call, omit required sections, or respond with progress commentary.",
	})
}

const (
	defaultBudgetedCompletionTokens = 8192
	minimumBudgetedCompletionTokens = 256
)

func (a *SubAgent) applyCostBudget(req *model.ChatRequest, result *SubAgentResult) error {
	if a.maxCostUSD <= 0 || req == nil {
		return nil
	}
	pricing, err := a.client.GetPricing(a.model)
	if err != nil {
		return fmt.Errorf("resolve model pricing for cost budget: %w", err)
	}
	spent, err := a.client.CalculateCostFromTokens(a.model, result.InputTokens, result.OutputTokens)
	if err != nil {
		return fmt.Errorf("calculate consumed review budget: %w", err)
	}
	estimate := model.EstimateRequestTokens(*req)
	maxOutputTokens, err := budgetedMaxOutputTokens(*pricing, estimate.Total, spent, a.maxCostUSD)
	if err != nil {
		return err
	}
	req.MaxTokens = maxOutputTokens
	return nil
}

func budgetedMaxOutputTokens(pricing model.ModelPricing, estimatedInputTokens int, spentUSD, maxCostUSD float64) (int, error) {
	remaining := maxCostUSD - spentUSD
	estimatedInputCost := float64(estimatedInputTokens) * pricing.Prompt / 1_000_000
	// Leave room for token-estimation and provider-accounting variance.
	availableOutputUSD := (remaining - estimatedInputCost) * 0.98
	if availableOutputUSD <= 0 {
		return 0, fmt.Errorf("review cost budget exhausted before model call: $%.4f spent, $%.4f limit", spentUSD, maxCostUSD)
	}

	maxOutputTokens := defaultBudgetedCompletionTokens
	if pricing.Completion > 0 {
		maxOutputTokens = min(maxOutputTokens, int(availableOutputUSD*1_000_000/pricing.Completion))
	}
	if maxOutputTokens < minimumBudgetedCompletionTokens {
		return 0, fmt.Errorf("review cost budget cannot fund a useful model response: %d output tokens affordable", maxOutputTokens)
	}
	return maxOutputTokens, nil
}

func finalizeSubAgentResult(result *SubAgentResult, start time.Time) {
	if result == nil {
		return
	}
	if strings.TrimSpace(result.Summary) == "" {
		result.Summary = summarizeToolCalls(result.ToolCalls)
	}
	result.Raw = marshalSubAgentRaw(result)
	result.Duration = time.Since(start)
}

func isReadOnlyToolSet(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		switch strings.TrimSpace(name) {
		case "read_file", "find_files", "search_text":
			// These built-ins do not execute arbitrary code or modify files.
		default:
			return false
		}
	}
	return true
}

func applyExecutionPolicy(req *model.ChatRequest, readOnly bool, snapshot *model.ReviewSnapshot) {
	if req == nil || (!readOnly && snapshot == nil) {
		return
	}
	if readOnly || snapshot != nil {
		if req.Metadata == nil {
			req.Metadata = make(map[string]string, 2)
		}
		req.Metadata[model.RequestMetadataReadOnly] = "true"
	}
	if snapshot != nil {
		req.ReviewSnapshot = snapshot
		req.Metadata[model.RequestMetadataReviewSnapshot] = snapshot.ID()
	}
}

func (a *SubAgent) allowedRegistry(ctx context.Context) (*tool.Registry, map[string]struct{}) {
	allowed := map[string]struct{}{}
	if a.registry == nil {
		return tool.NewEmptyRegistry(), allowed
	}
	if a.approver == nil {
		for _, t := range a.registry.List() {
			allowed[t.Name()] = struct{}{}
		}
	} else {
		allowedTools := a.approver.GetAllowedToolsForAgent(ctx)
		if len(allowedTools) == 0 {
			return tool.NewEmptyRegistry(), allowed
		}
		for _, name := range allowedTools {
			allowed[name] = struct{}{}
		}
		if _, ok := allowed["*"]; ok {
			allowed = map[string]struct{}{}
			for _, t := range a.registry.List() {
				allowed[t.Name()] = struct{}{}
			}
		}
	}

	if len(a.allowedTools) > 0 {
		allowed = intersectAllowed(allowed, a.allowedTools)
	}

	if len(allowed) == 0 {
		return tool.NewEmptyRegistry(), allowed
	}

	return a.registry, allowed
}

func buildToolDefinitions(registry *tool.Registry, allowed map[string]struct{}) []map[string]any {
	if registry == nil {
		return nil
	}
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	return registry.ToOpenAIFunctionsFiltered(names)
}

func (a *SubAgent) executeTools(ctx context.Context, calls []model.ToolCall, registry *tool.Registry, allowed map[string]struct{}, result *SubAgentResult) ([]SubAgentToolCall, error) {
	toolResults := make([]SubAgentToolCall, 0, len(calls))

	for _, call := range calls {
		name := call.Function.Name
		if name == "" {
			return nil, fmt.Errorf("tool name missing")
		}
		if len(allowed) == 0 {
			return nil, fmt.Errorf("no tools allowed")
		}
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("tool not allowed: %s", name)
		}
		if a.approver != nil {
			if err := a.approver.CheckToolAccess(ctx, name); err != nil {
				return nil, err
			}
		}

		// Runtime guard: validate tool call against role_permissions rules.
		// Defense in depth -- tool list is filtered at spawn time, but this
		// validates at execution time (e.g., for kill-switch overrides).
		if a.engine != nil && a.toolTier != "" {
			if err := a.checkRolePermission(name); err != nil {
				return nil, err
			}
		}

		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			toolResults = append(toolResults, SubAgentToolCall{
				ID:        call.ID,
				Name:      name,
				Arguments: call.Function.Arguments,
				Result:    fmt.Sprintf("invalid arguments: %v", err),
				Success:   false,
			})
			continue
		}

		if args == nil {
			args = map[string]any{}
		}
		if call.ID != "" {
			args[tool.ToolCallIDParam] = call.ID
		}

		release := a.acquireLock(name, args)
		start := time.Now()
		res, err := registry.ExecuteWithContext(ctx, name, args)
		if release != nil {
			release()
		}

		toolCall := SubAgentToolCall{
			ID:        call.ID,
			Name:      name,
			Arguments: call.Function.Arguments,
			Duration:  time.Since(start),
		}

		if err != nil {
			toolCall.Result = fmt.Sprintf("execution error: %v", err)
			toolCall.Success = false
		} else {
			toolCall.Success = res != nil && res.Success
			if res != nil {
				toolCall.Data = cloneToolResultData(res.Data)
			}
			toolCall.Result = formatToolResult(res)
		}
		toolResults = append(toolResults, toolCall)
		result.ToolCalls = append(result.ToolCalls, toolCall)
	}

	return toolResults, nil
}

// checkRolePermission validates a tool call against role_permissions arbiter rules.
func (a *SubAgent) checkRolePermission(toolName string) error {
	matched, err := rules.Eval(a.engine, "role_permissions", rules.RolePermissionFacts{
		Role: "subagent",
		Tier: a.toolTier,
	})
	if err != nil || len(matched) == 0 {
		return nil // fail open if rules unavailable
	}
	params := matched[0].Params

	// Check explicit deny list.
	if denied, ok := params["denied"].([]any); ok {
		for _, d := range denied {
			if s, ok := d.(string); ok && s == toolName {
				return fmt.Errorf("tool %q denied by role_permissions rule for tier %q", toolName, a.toolTier)
			}
		}
	}

	// Check write capability.
	if canWrite, ok := params["can_write"].(bool); ok && !canWrite {
		if isWriteTool(toolName) {
			return fmt.Errorf("tool %q denied: write not permitted for tier %q", toolName, a.toolTier)
		}
	}

	// Check shell capability.
	if canShell, ok := params["can_shell"].(bool); ok && !canShell {
		if toolName == "shell" || toolName == "bash" {
			return fmt.Errorf("tool %q denied: shell not permitted for tier %q", toolName, a.toolTier)
		}
	}

	return nil
}

// isWriteTool returns true if the tool is a write-capable tool.
func isWriteTool(name string) bool {
	switch name {
	case "write_file", "patch_file", "edit_file", "insert_text", "delete_lines",
		"search_replace", "rename_symbol", "extract_function", "mark_resolved":
		return true
	default:
		return false
	}
}

func (a *SubAgent) acquireLock(name string, args map[string]any) func() {
	if a.conflicts == nil {
		return nil
	}
	path := extractPathArg(args)
	if path == "" {
		return nil
	}
	mode := toolLockMode(name)
	if mode == "" {
		return nil
	}

	switch mode {
	case "read":
		if err := a.conflicts.AcquireRead(a.id, path); err != nil {
			return nil
		}
		return func() { a.conflicts.ReleaseRead(a.id, path) }
	case "write":
		if err := a.conflicts.AcquireWrite(a.id, path); err != nil {
			return nil
		}
		return func() { a.conflicts.ReleaseWrite(a.id, path) }
	}
	return nil
}

func extractPathArg(args map[string]any) string {
	if args == nil {
		return ""
	}
	if value, ok := args["path"].(string); ok {
		return strings.TrimSpace(value)
	}
	if value, ok := args["file"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func toolLockMode(name string) string {
	switch name {
	case "read_file", "list_directory", "find_files", "file_exists", "get_file_info", "search_text":
		return "read"
	case "write_file", "patch_file", "edit_file", "insert_text", "delete_lines", "search_replace", "rename_symbol", "extract_function", "mark_resolved":
		return "write"
	default:
		return ""
	}
}

func formatToolResult(res *builtin.Result) string {
	if res == nil {
		return ""
	}
	result, err := tool.ToModelOutput(res)
	if err != nil {
		return fmt.Sprintf("{\"success\":%t}", res.Success)
	}
	return result
}

func cloneToolResultData(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func summarizeToolCalls(calls []SubAgentToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Name != "" {
			names = append(names, call.Name)
		}
	}
	return fmt.Sprintf("Executed %d tool calls: %s", len(calls), strings.Join(names, ", "))
}

func marshalSubAgentRaw(result *SubAgentResult) []byte {
	if result == nil {
		return nil
	}
	payload := map[string]any{
		"summary":            result.Summary,
		"tool_calls":         result.ToolCalls,
		"execution_evidence": result.ExecutionEvidence,
		"tokens_used":        result.TokensUsed,
		"input_tokens":       result.InputTokens,
		"output_tokens":      result.OutputTokens,
		"model":              result.ModelUsed,
		"agent_id":           result.AgentID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return encoded
}

func intersectAllowed(base, allowed map[string]struct{}) map[string]struct{} {
	if len(base) == 0 || len(allowed) == 0 {
		return map[string]struct{}{}
	}
	out := make(map[string]struct{})
	for name := range allowed {
		if _, ok := base[name]; ok {
			out[name] = struct{}{}
		}
	}
	return out
}
