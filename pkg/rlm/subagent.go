package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/coordination/security"
	"m31labs.dev/buckley/pkg/jsonrepair"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

const (
	defaultSubAgentMaxIterations = 25

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
	id            string
	model         string
	systemPrompt  string
	reasoning     string
	maxIterations int
	allowedTools  map[string]struct{}
	toolTier      string

	client     *model.Manager
	registry   *tool.Registry
	scratchpad ScratchpadWriter
	conflicts  *ConflictDetector
	approver   *security.ToolApprover
	engine     *rules.Engine
}

// SubAgentConfig configures a sub-agent execution.
type SubAgentConfig struct {
	ID            string
	Model         string
	Reasoning     string
	SystemPrompt  string
	MaxIterations int
	AllowedTools  []string
	ToolTier      string // role_permissions tier for runtime validation
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
	AgentID    string
	ModelUsed  string
	Summary    string
	RawKey     string
	Raw        []byte
	TokensUsed int
	Duration   time.Duration
	ToolCalls  []SubAgentToolCall
}

// SubAgentToolCall records a tool invocation.
type SubAgentToolCall struct {
	ID        string
	Name      string
	Arguments string
	Result    string
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
	if maxIterations <= 0 {
		maxIterations = defaultSubAgentMaxIterations
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
		id:            cfg.ID,
		model:         cfg.Model,
		systemPrompt:  prompt,
		reasoning:     normalizeSubAgentReasoning(cfg.Reasoning),
		maxIterations: maxIterations,
		allowedTools:  allowedTools,
		toolTier:      cfg.ToolTier,
		client:        deps.Models,
		registry:      deps.Registry,
		scratchpad:    deps.Scratchpad,
		conflicts:     deps.Conflicts,
		approver:      deps.Approver,
		engine:        deps.Engine,
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
	toolDefs := buildToolDefinitions(allowedRegistry)

	messages := []model.Message{
		{Role: "system", Content: a.systemPrompt},
		{Role: "user", Content: task},
	}

	result := &SubAgentResult{
		AgentID:   a.id,
		ModelUsed: a.model,
	}

	// fallbackRetried ensures we give a reasoning model at most one
	// corrective nudge when it emits a tool call as text we can't parse
	// (see model.ParseTextToolCalls), instead of either silently surfacing
	// the raw payload as the final summary or looping forever.
	fallbackRetried := false

	for i := 0; i < a.maxIterations; i++ {
		req := model.ChatRequest{
			Model:    a.model,
			Messages: messages,
			Tools:    toolDefs,
			ToolChoice: func() string {
				if len(toolDefs) == 0 {
					return "none"
				}
				return "auto"
			}(),
		}
		if a.reasoning != "" {
			req.Reasoning = &model.ReasoningConfig{Effort: a.reasoning}
		}
		resp, err := a.client.ChatCompletion(ctx, req)
		if err != nil {
			result.Duration = time.Since(start)
			return result, err
		}
		result.TokensUsed += resp.Usage.TotalTokens

		if len(resp.Choices) == 0 {
			result.Duration = time.Since(start)
			return result, fmt.Errorf("no response from model")
		}

		choice := resp.Choices[0]

		// Check for tool calls, falling back to parsing a textual tool-call
		// payload (GLM/Qwen `<tool_call>{...}</tool_call>` or ```json
		// fenced) when the model didn't populate the structured tool_calls
		// field. Without this fallback, that raw text becomes
		// result.Summary below -- the exact bug behind `buckley review`
		// leaking a raw tool-call payload as its verdict.
		toolCalls, unparsable, reason := resolveSubAgentToolCalls(choice.Message)
		if unparsable {
			// Looks like a tool call but we couldn't parse it. Never let
			// this raw text leak out as the sub-agent's final answer.
			if fallbackRetried {
				result.Duration = time.Since(start)
				return result, fmt.Errorf("sub-agent %s: model emitted a tool call as text that could not be parsed: %s", a.id, reason)
			}
			fallbackRetried = true
			messages = append(messages, model.Message{Role: "assistant", Content: choice.Message.Content})
			messages = append(messages, model.Message{
				Role: "user",
				Content: fmt.Sprintf("Your previous reply looked like a tool call but could not be parsed (%s). "+
					"Call the tool using the tool-calling interface, or reply with plain text only if no tool is needed.", reason),
			})
			continue
		}

		if len(toolCalls) > 0 {
			toolResults, err := a.executeTools(ctx, toolCalls, allowedRegistry, allowedSet, result)
			if err != nil {
				result.Duration = time.Since(start)
				return result, err
			}

			messages = append(messages, model.Message{
				Role:      "assistant",
				Content:   choice.Message.Content,
				ToolCalls: toolCalls,
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

	if result.Summary == "" {
		result.Summary = summarizeToolCalls(result.ToolCalls)
	}

	raw := marshalSubAgentRaw(result)
	result.Raw = raw
	if a.scratchpad != nil {
		key, err := a.scratchpad.Write(ctx, WriteRequest{
			Type:      EntryTypeAnalysis,
			Raw:       raw,
			Summary:   result.Summary,
			Metadata:  map[string]any{"model": a.model, "agent_id": a.id},
			CreatedBy: a.id,
			CreatedAt: time.Now(),
		})
		if err == nil {
			result.RawKey = key
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

// resolveSubAgentToolCalls extracts tool calls from msg, falling back to
// model.ParseTextToolCalls when the model didn't populate the structured
// tool_calls field. This handles reasoning models (GLM-5.x, Qwen agentic
// checkpoints, etc.) routed through OpenRouter/vLLM that emit a tool call as
// text inside the message content -- a Hermes/GLM-style
// `<tool_call>{...}</tool_call>` block (sometimes with GLM's native
// `name\n<arg_key>..</arg_key>` body instead of JSON) or a ```json fenced
// `{"name":...,"arguments":...}` object -- instead of the structured
// tool_calls field.
//
// unparsable is true when a tool-call-shaped payload was found in the text
// but could not be cleanly parsed -- callers must not treat msg.Content as
// a final answer (result.Summary) in that case, only retry or error.
func resolveSubAgentToolCalls(msg model.Message) (calls []model.ToolCall, unparsable bool, reason string) {
	if len(msg.ToolCalls) > 0 {
		return msg.ToolCalls, false, ""
	}
	textContent, err := model.ExtractTextContent(msg.Content)
	if err != nil {
		return nil, false, ""
	}
	parsed := model.ParseTextToolCalls(textContent)
	if !parsed.Detected {
		return nil, false, ""
	}
	if len(parsed.Calls) > 0 {
		return parsed.Calls, false, ""
	}
	return nil, true, parsed.Reason
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

func buildToolDefinitions(registry *tool.Registry) []map[string]any {
	if registry == nil {
		return nil
	}
	tools := registry.List()
	defs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, tool.ToOpenAIFunction(t))
	}
	return defs
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
		// jsonrepair.TryUnmarshal guards against reasoning models (GLM-5.x
		// in particular) that populate the *structured* tool_calls field
		// with argument JSON containing common quirks -- most notably stray
		// whitespace inside numeric literals -- which would otherwise fail
		// here with e.g. "invalid character ' ' in numeric literal".
		// model.ParseTextToolCalls already repairs this for the text
		// fallback path above; this covers the same quirk arriving via a
		// structured tool call that never went through that parser.
		if err := jsonrepair.TryUnmarshal([]byte(call.Function.Arguments), &args); err != nil {
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
		res, err := registry.Execute(name, args)
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
	// Use tool.ToJSON which applies TOON encoding for compact token-efficient results
	result, err := tool.ToJSON(res)
	if err != nil {
		return fmt.Sprintf("{\"success\":%t}", res.Success)
	}
	return result
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
		"summary":     result.Summary,
		"tool_calls":  result.ToolCalls,
		"tokens_used": result.TokensUsed,
		"model":       result.ModelUsed,
		"agent_id":    result.AgentID,
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
