package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/draco/buckley/pkg/coordination/security"
	"github.com/draco/buckley/pkg/model"
	"github.com/draco/buckley/pkg/tool"
	"github.com/draco/buckley/pkg/tool/builtin"
)

const (
	defaultSubAgentMaxIterations = 25

	// defaultReasoningEffort is the reasoning effort requested for models that
	// advertise reasoning support (model.Manager.SupportsReasoning) but have no
	// caller-specified effort. Mirrors the "high" default already used for
	// review/planning requests in pkg/orchestrator (review_agent.go, planner.go).
	defaultReasoningEffort = "high"

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
	maxIterations int
	allowedTools  map[string]struct{}

	client     *model.Manager
	registry   *tool.Registry
	scratchpad ScratchpadWriter
	conflicts  *ConflictDetector
	approver   *security.ToolApprover
}

// SubAgentConfig configures a sub-agent execution.
type SubAgentConfig struct {
	ID            string
	Model         string
	SystemPrompt  string
	MaxIterations int
	AllowedTools  []string
}

// SubAgentDeps provides shared dependencies.
type SubAgentDeps struct {
	Models     *model.Manager
	Registry   *tool.Registry
	Scratchpad ScratchpadWriter
	Conflicts  *ConflictDetector
	Approver   *security.ToolApprover
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
		maxIterations: maxIterations,
		allowedTools:  allowedTools,
		client:        deps.Models,
		registry:      deps.Registry,
		scratchpad:    deps.Scratchpad,
		conflicts:     deps.Conflicts,
		approver:      deps.Approver,
	}, nil
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

	// fallbackRetried ensures we give a reasoning model at most one corrective
	// nudge when it emits a tool call as text that we can't parse cleanly,
	// instead of either silently surfacing the raw payload as the final
	// summary or looping forever.
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
		if a.client.SupportsReasoning(a.model) {
			req.Reasoning = &model.ReasoningConfig{Effort: defaultReasoningEffort}
		}

		resp, err := streamChatCompletion(ctx, a.client, req)
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
		toolCalls := choice.Message.ToolCalls
		fromFallbackParse := false

		if len(toolCalls) == 0 {
			if textContent, convErr := model.ExtractTextContent(choice.Message.Content); convErr == nil {
				parsed := parseTextToolCalls(textContent)
				switch {
				case parsed.Detected && len(parsed.Calls) > 0:
					// The model emitted a well-formed tool call as text (e.g. a
					// GLM/Qwen <tool_call>{...}</tool_call> or ```json payload)
					// instead of populating the structured tool_calls field.
					// Dispatch it through the normal tool-execution path exactly
					// as if the API had returned it structured.
					toolCalls = parsed.Calls
					fromFallbackParse = true
				case parsed.Detected:
					// Looks like a tool call but we couldn't parse it. Never let
					// this raw text leak out as the sub-agent's final answer.
					if fallbackRetried {
						result.Duration = time.Since(start)
						return result, fmt.Errorf("sub-agent %s: model emitted a tool call as text that could not be parsed: %s", a.id, parsed.Reason)
					}
					fallbackRetried = true
					messages = append(messages, model.Message{Role: "assistant", Content: choice.Message.Content})
					messages = append(messages, model.Message{
						Role: "user",
						Content: fmt.Sprintf("Your previous reply looked like a tool call but could not be parsed (%s). "+
							"Call the tool using the tool-calling interface, or reply with plain text only if no tool is needed.", parsed.Reason),
					})
					continue
				}
			}
		}

		if len(toolCalls) > 0 {
			toolResults, err := a.executeTools(ctx, toolCalls, allowedRegistry, allowedSet, result)
			if err != nil {
				if fromFallbackParse && !fallbackRetried {
					// A fallback-parsed call didn't dispatch (e.g. disallowed
					// tool, bad args). Give the model one chance to correct
					// itself instead of aborting the whole task immediately.
					fallbackRetried = true
					messages = append(messages, model.Message{Role: "assistant", Content: choice.Message.Content})
					messages = append(messages, model.Message{
						Role:    "user",
						Content: fmt.Sprintf("Your previous tool call could not be dispatched (%v). Call the tool using the tool-calling interface, or reply with plain text only if no tool is needed.", err),
					})
					continue
				}
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

// ---------------------------------------------------------------------------
// Fallback text tool-call parsing.
//
// Reasoning models (GLM-4.x, Qwen agentic checkpoints, etc.) routed through
// OpenRouter/vLLM don't always populate the OpenAI-standard structured
// `tool_calls` field. Instead they emit the call as text inside the message
// content, typically as a Hermes/GLM-style `<tool_call>{...}</tool_call>`
// block (sometimes with GLM's native `name\n<arg_key>..</arg_key>` body
// instead of JSON) or a ```json fenced `{"name":...,"arguments":...}` object.
// Left unhandled, that raw text gets treated as the model's final answer
// (see subagent.go Execute: the `len(choice.Message.ToolCalls) == 0` branch).
// parseTextToolCalls recognizes these encodings and converts them back into
// model.ToolCall values so callers can dispatch them exactly like a
// structured tool call.
// ---------------------------------------------------------------------------

var (
	textToolCallTagRe = regexp.MustCompile(`(?is)<tool_call>(.*?)</tool_call>`)
	textJSONFenceRe   = regexp.MustCompile("(?is)```json\\s*(.*?)```")
	textArgPairRe     = regexp.MustCompile(`(?is)<arg_key>(.*?)</arg_key>\s*<arg_value>(.*?)</arg_value>`)
)

// textToolCallParse is the outcome of scanning free-form model output for a
// tool call encoded as text.
type textToolCallParse struct {
	// Calls holds successfully parsed tool calls, if any.
	Calls []model.ToolCall
	// Detected is true when content contains a recognizable tool-call wrapper
	// (<tool_call> tags, or a JSON tool-call object) even if it could not be
	// fully parsed. Callers must not treat the raw text as a final answer
	// when Detected is true and Calls is empty.
	Detected bool
	// Reason explains why parsing failed when Detected is true and Calls is
	// empty.
	Reason string
}

// parseTextToolCalls scans content emitted in an assistant message body for a
// tool call written as text rather than delivered via the structured
// tool_calls field. It supports the encodings seen in practice from
// reasoning models on OpenRouter: Hermes/GLM-style
// `<tool_call>{json}</tool_call>` blocks (including GLM's native
// `<tool_call>name\n<arg_key>..</arg_key><arg_value>..</arg_value></tool_call>`
// form) and ```json fenced `{"name":...,"arguments":...}` objects.
func parseTextToolCalls(content string) textToolCallParse {
	content = strings.TrimSpace(content)
	if content == "" {
		return textToolCallParse{}
	}

	if tags := textToolCallTagRe.FindAllStringSubmatch(content, -1); len(tags) > 0 {
		out := textToolCallParse{Detected: true}
		for i, m := range tags {
			call, err := parseTaggedToolCall(strings.TrimSpace(m[1]), i)
			if err != nil {
				return textToolCallParse{Detected: true, Reason: err.Error()}
			}
			out.Calls = append(out.Calls, call)
		}
		return out
	}

	if fences := textJSONFenceRe.FindAllStringSubmatch(content, -1); len(fences) > 0 {
		out := textToolCallParse{}
		for i, m := range fences {
			call, recognized, err := parseJSONToolCall(strings.TrimSpace(m[1]), i)
			if !recognized {
				continue
			}
			out.Detected = true
			if err != nil {
				return textToolCallParse{Detected: true, Reason: err.Error()}
			}
			out.Calls = append(out.Calls, call)
		}
		if out.Detected {
			return out
		}
	}

	// A message that is nothing but a bare JSON tool-call object (no tags or
	// fence at all).
	if strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}") {
		call, recognized, err := parseJSONToolCall(content, 0)
		if recognized {
			if err != nil {
				return textToolCallParse{Detected: true, Reason: err.Error()}
			}
			return textToolCallParse{Detected: true, Calls: []model.ToolCall{call}}
		}
	}

	return textToolCallParse{}
}

// parseTaggedToolCall parses the inner text of a single
// <tool_call>...</tool_call> block, which is either a JSON object or GLM's
// native "name\n<arg_key>k</arg_key><arg_value>v</arg_value>..." form.
func parseTaggedToolCall(inner string, idx int) (model.ToolCall, error) {
	if inner == "" {
		return model.ToolCall{}, fmt.Errorf("tool_call #%d: empty payload", idx+1)
	}

	if strings.HasPrefix(inner, "{") {
		call, recognized, err := parseJSONToolCall(inner, idx)
		if recognized {
			if err != nil {
				return model.ToolCall{}, fmt.Errorf("tool_call #%d: %w", idx+1, err)
			}
			return call, nil
		}
	}

	if pairs := textArgPairRe.FindAllStringSubmatch(inner, -1); len(pairs) > 0 {
		keyIdx := strings.Index(inner, "<arg_key>")
		name := inner
		if keyIdx >= 0 {
			name = inner[:keyIdx]
		}
		if nl := strings.IndexAny(name, "\r\n"); nl >= 0 {
			name = name[:nl]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return model.ToolCall{}, fmt.Errorf("tool_call #%d: missing function name before <arg_key>", idx+1)
		}
		args := make(map[string]string, len(pairs))
		for _, p := range pairs {
			key := strings.TrimSpace(p[1])
			if key == "" {
				continue
			}
			args[key] = strings.TrimSpace(p[2])
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return model.ToolCall{}, fmt.Errorf("tool_call #%d: %w", idx+1, err)
		}
		return model.ToolCall{
			ID:       fmt.Sprintf("fallback-call-%d", idx+1),
			Type:     "function",
			Function: model.FunctionCall{Name: name, Arguments: string(encoded)},
		}, nil
	}

	return model.ToolCall{}, fmt.Errorf("tool_call #%d: unrecognized payload (not JSON, no <arg_key>/<arg_value> pairs)", idx+1)
}

// parseJSONToolCall attempts to interpret s as a
// {"name":...,"arguments":...} tool-call object. recognized is false when s
// isn't shaped like a tool call at all (e.g. missing a "name" field), so
// callers can skip it without treating the surrounding text as a
// detected-but-broken tool call. recognized is true with a non-nil err when s
// looks like it was meant to be a tool call but is malformed.
func parseJSONToolCall(s string, idx int) (call model.ToolCall, recognized bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '{' {
		return model.ToolCall{}, false, nil
	}

	var raw map[string]json.RawMessage
	if unmarshalErr := json.Unmarshal([]byte(s), &raw); unmarshalErr != nil {
		return model.ToolCall{}, true, fmt.Errorf("invalid JSON tool-call payload: %w", unmarshalErr)
	}

	nameRaw, hasName := raw["name"]
	if !hasName {
		return model.ToolCall{}, false, nil
	}
	argsRaw, hasArgs := raw["arguments"]
	if !hasArgs {
		argsRaw, hasArgs = raw["parameters"]
	}

	var name string
	if unmarshalErr := json.Unmarshal(nameRaw, &name); unmarshalErr != nil || strings.TrimSpace(name) == "" {
		return model.ToolCall{}, true, fmt.Errorf("tool-call payload has a non-string or empty \"name\" field")
	}

	argsJSON := "{}"
	if hasArgs {
		trimmed := strings.TrimSpace(string(argsRaw))
		if strings.HasPrefix(trimmed, `"`) {
			// arguments encoded as a JSON string containing JSON, e.g.
			// "arguments": "{\"path\":\"x\"}"
			var inner string
			if unmarshalErr := json.Unmarshal(argsRaw, &inner); unmarshalErr != nil {
				return model.ToolCall{}, true, fmt.Errorf("tool call %q has an unparsable arguments string: %w", name, unmarshalErr)
			}
			inner = strings.TrimSpace(inner)
			if inner == "" {
				inner = "{}"
			}
			if !json.Valid([]byte(inner)) {
				return model.ToolCall{}, true, fmt.Errorf("tool call %q arguments string is not valid JSON", name)
			}
			argsJSON = inner
		} else {
			if !json.Valid(argsRaw) {
				return model.ToolCall{}, true, fmt.Errorf("tool call %q has malformed arguments", name)
			}
			argsJSON = trimmed
		}
	}

	return model.ToolCall{
		ID:       fmt.Sprintf("fallback-call-%d", idx+1),
		Type:     "function",
		Function: model.FunctionCall{Name: name, Arguments: argsJSON},
	}, true, nil
}
