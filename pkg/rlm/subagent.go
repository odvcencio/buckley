package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/coordination/security"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/toolrunner"
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
	maxIterations int
	allowedTools  map[string]struct{}

	client     *model.Manager
	registry   *tool.Registry
	scratchpad ScratchpadWriter
	conflicts  *ConflictDetector
	approver   *security.ToolApprover
}

// SubAgentInstanceConfig configures a sub-agent execution.
type SubAgentInstanceConfig struct {
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
func NewSubAgent(cfg SubAgentInstanceConfig, deps SubAgentDeps) (*SubAgent, error) {
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

	result := &SubAgentResult{
		AgentID:   a.id,
		ModelUsed: a.model,
	}

	runner, err := toolrunner.New(toolrunner.Config{
		Models:               a.client,
		Registry:             allowedRegistry,
		DefaultMaxIterations: a.maxIterations,
		ToolExecutor: func(ctx context.Context, call model.ToolCall, args map[string]any, tools map[string]tool.Tool) (toolrunner.ToolExecutionResult, error) {
			return a.executeToolCall(ctx, call, args, tools, allowedSet)
		},
	})
	if err != nil {
		result.Duration = time.Since(start)
		return result, err
	}

	execResult, execErr := runner.Run(ctx, toolrunner.Request{
		Messages: []model.Message{
			{Role: "system", Content: a.systemPrompt},
			{Role: "user", Content: task},
		},
		SelectionPrompt: task,
		AllowedTools:    allowedToolNames(allowedSet),
		MaxIterations:   a.maxIterations,
		Model:           a.model,
	})
	if execResult != nil {
		result.TokensUsed = execResult.Usage.TotalTokens
		result.Summary = strings.TrimSpace(execResult.Content)
		result.ToolCalls = append(result.ToolCalls, toSubAgentToolCalls(execResult.ToolCalls)...)
	}
	if execErr != nil {
		result.Duration = time.Since(start)
		return result, execErr
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

	// Default: sub-agents get all tools from registry
	// The approver can restrict this if configured
	for _, t := range a.registry.List() {
		allowed[t.Name()] = struct{}{}
	}

	// If approver is set, use it to filter (but don't require it)
	if a.approver != nil {
		allowedTools := a.approver.GetAllowedToolsForAgent(ctx)
		// Only restrict if approver explicitly returns a non-empty list
		if len(allowedTools) > 0 {
			if _, hasWildcard := func() (struct{}, bool) {
				for _, name := range allowedTools {
					if name == "*" {
						return struct{}{}, true
					}
				}
				return struct{}{}, false
			}(); !hasWildcard {
				// Approver returned specific tools, use those
				allowed = map[string]struct{}{}
				for _, name := range allowedTools {
					allowed[name] = struct{}{}
				}
			}
			// If wildcard, keep the full registry (already set above)
		}
		// If empty list, keep full registry access (don't restrict)
	}

	if len(a.allowedTools) > 0 {
		allowed = intersectAllowed(allowed, a.allowedTools)
	}

	if len(allowed) == 0 {
		return tool.NewEmptyRegistry(), allowed
	}

	return a.registry, allowed
}

func (a *SubAgent) executeToolCall(ctx context.Context, call model.ToolCall, args map[string]any, tools map[string]tool.Tool, allowed map[string]struct{}) (toolrunner.ToolExecutionResult, error) {
	name := strings.TrimSpace(call.Function.Name)
	if name == "" {
		return toolrunner.ToolExecutionResult{}, fmt.Errorf("tool name missing")
	}
	if len(allowed) == 0 {
		return toolrunner.ToolExecutionResult{}, fmt.Errorf("no tools allowed")
	}
	if _, ok := allowed[name]; !ok {
		return toolrunner.ToolExecutionResult{}, fmt.Errorf("tool not allowed: %s", name)
	}
	if a.approver != nil {
		if err := a.approver.CheckToolAccess(ctx, name); err != nil {
			return toolrunner.ToolExecutionResult{}, err
		}
	}
	if _, ok := tools[name]; !ok {
		errMsg := fmt.Sprintf("tool not found: %s", name)
		return toolrunner.ToolExecutionResult{
			Result:  errMsg,
			Error:   errMsg,
			Success: false,
		}, nil
	}

	release := a.acquireLock(name, args)
	res, err := a.registry.ExecuteWithContext(ctx, name, args)
	if release != nil {
		release()
	}

	if err != nil {
		errMsg := fmt.Sprintf("execution error: %v", err)
		return toolrunner.ToolExecutionResult{
			Result:  errMsg,
			Error:   errMsg,
			Success: false,
		}, nil
	}

	if res == nil {
		return toolrunner.ToolExecutionResult{}, nil
	}

	return toolrunner.ToolExecutionResult{
		Result:  formatToolResult(res),
		Error:   res.Error,
		Success: res.Success,
	}, nil
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

func allowedToolNames(allowed map[string]struct{}) []string {
	if len(allowed) == 0 {
		return nil
	}
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	return names
}

func toSubAgentToolCalls(calls []toolrunner.ToolCallRecord) []SubAgentToolCall {
	out := make([]SubAgentToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, SubAgentToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
			Result:    call.Result,
			Success:   call.Success,
			Duration:  time.Duration(call.Duration) * time.Millisecond,
		})
	}
	return out
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
