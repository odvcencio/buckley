package rlm

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// DelegateTool dispatches a single task to a sub-agent.
type DelegateTool struct {
	dispatcher  *BatchDispatcher
	ctxProvider func() context.Context
}

// NewDelegateTool constructs a delegate tool.
func NewDelegateTool(dispatcher *BatchDispatcher, ctxProvider func() context.Context) *DelegateTool {
	return &DelegateTool{dispatcher: dispatcher, ctxProvider: ctxProvider}
}

func (t *DelegateTool) Name() string {
	return "delegate"
}

func (t *DelegateTool) Description() string {
	return "Delegate a task to a sub-agent and return a summary with scratchpad references."
}

func (t *DelegateTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"task": {
				Type:        "string",
				Description: "Task description for the sub-agent",
			},
			"weight": {
				Type:        "string",
				Description: "Task weight tier (trivial, light, medium, heavy, reasoning)",
			},
			"tools": {
				Type:        "array",
				Description: "Optional list of allowed tools",
			},
			"system_prompt": {
				Type:        "string",
				Description: "Optional system prompt for the sub-agent",
			},
			"max_iterations": {
				Type:        "integer",
				Description: "Optional max iterations for the sub-agent",
			},
		},
		Required: []string{"task"},
	}
}

func (t *DelegateTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.dispatcher == nil {
		return &builtin.Result{Success: false, Error: "delegate dispatcher unavailable"}, nil
	}
	task, ok := params["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return &builtin.Result{Success: false, Error: "task must be a non-empty string"}, nil
	}

	weight := parseWeight(params["weight"])
	tools := parseStringSlice(params["tools"])
	systemPrompt, _ := params["system_prompt"].(string)
	maxIterations := parseInt(params["max_iterations"], 0)

	ctx := context.Background()
	if t.ctxProvider != nil {
		ctx = t.ctxProvider()
	}

	results, err := t.dispatcher.Execute(ctx, BatchRequest{
		Tasks: []SubTask{{
			Prompt:        task,
			Weight:        weight,
			AllowedTools:  tools,
			SystemPrompt:  systemPrompt,
			MaxIterations: maxIterations,
		}},
		Parallel: false,
	})
	if err != nil {
		return &builtin.Result{Success: false, Error: err.Error()}, nil
	}
	if len(results) == 0 {
		return &builtin.Result{Success: false, Error: "no result returned"}, nil
	}
	res := results[0]
	return &builtin.Result{
		Success: res.Error == "",
		Data: map[string]any{
			"summary":        res.Summary,
			"scratchpad_key": res.RawKey,
			"agent_id":       res.AgentID,
			"model":          res.ModelUsed,
			"error":          res.Error,
		},
		Error: res.Error,
	}, nil
}

// DelegateBatchTool dispatches multiple tasks at once.
type DelegateBatchTool struct {
	dispatcher  *BatchDispatcher
	ctxProvider func() context.Context
}

// NewDelegateBatchTool constructs a delegate_batch tool.
func NewDelegateBatchTool(dispatcher *BatchDispatcher, ctxProvider func() context.Context) *DelegateBatchTool {
	return &DelegateBatchTool{dispatcher: dispatcher, ctxProvider: ctxProvider}
}

func (t *DelegateBatchTool) Name() string {
	return "delegate_batch"
}

func (t *DelegateBatchTool) Description() string {
	return "Delegate multiple tasks to sub-agents, optionally in parallel."
}

func (t *DelegateBatchTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"tasks": {
				Type:        "array",
				Description: "List of tasks with task, weight, tools, system_prompt",
			},
			"parallel": {
				Type:        "boolean",
				Description: "Run tasks in parallel",
				Default:     true,
			},
		},
		Required: []string{"tasks"},
	}
}

func (t *DelegateBatchTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.dispatcher == nil {
		return &builtin.Result{Success: false, Error: "delegate batch dispatcher unavailable"}, nil
	}
	tasksParam, ok := params["tasks"]
	if !ok {
		return &builtin.Result{Success: false, Error: "tasks required"}, nil
	}

	tasks, err := parseSubTasks(tasksParam)
	if err != nil {
		return &builtin.Result{Success: false, Error: err.Error()}, nil
	}
	parallel := parseBool(params["parallel"], true)

	ctx := context.Background()
	if t.ctxProvider != nil {
		ctx = t.ctxProvider()
	}

	results, execErr := t.dispatcher.Execute(ctx, BatchRequest{Tasks: tasks, Parallel: parallel})
	data := make([]map[string]any, 0, len(results))
	for _, res := range results {
		data = append(data, map[string]any{
			"task_id":        res.TaskID,
			"agent_id":       res.AgentID,
			"summary":        res.Summary,
			"scratchpad_key": res.RawKey,
			"model":          res.ModelUsed,
			"error":          res.Error,
		})
	}

	result := &builtin.Result{
		Success: execErr == nil,
		Data: map[string]any{
			"results": data,
		},
	}
	if execErr != nil {
		result.Error = execErr.Error()
	}
	return result, nil
}

// InspectTool returns scratchpad summaries for the coordinator.
type InspectTool struct {
	scratchpad  *Scratchpad
	ctxProvider func() context.Context
}

// NewInspectTool constructs an inspect tool.
func NewInspectTool(scratchpad *Scratchpad, ctxProvider func() context.Context) *InspectTool {
	return &InspectTool{scratchpad: scratchpad, ctxProvider: ctxProvider}
}

func (t *InspectTool) Name() string {
	return "inspect"
}

func (t *InspectTool) Description() string {
	return "Inspect a scratchpad entry summary by key."
}

func (t *InspectTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"key": {
				Type:        "string",
				Description: "Scratchpad entry key",
			},
		},
		Required: []string{"key"},
	}
}

func (t *InspectTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.scratchpad == nil {
		return &builtin.Result{Success: false, Error: "scratchpad unavailable"}, nil
	}
	key, ok := params["key"].(string)
	if !ok || strings.TrimSpace(key) == "" {
		return &builtin.Result{Success: false, Error: "key must be a non-empty string"}, nil
	}

	ctx := context.Background()
	if t.ctxProvider != nil {
		ctx = t.ctxProvider()
	}

	entry, err := t.scratchpad.Inspect(ctx, key)
	if err != nil {
		return &builtin.Result{Success: false, Error: err.Error()}, nil
	}
	if entry == nil {
		return &builtin.Result{Success: false, Error: "scratchpad entry not found"}, nil
	}

	return &builtin.Result{
		Success: true,
		Data: map[string]any{
			"summary":    entry.Summary,
			"metadata":   entry.Metadata,
			"created_by": entry.CreatedBy,
			"created_at": entry.CreatedAt,
		},
	}, nil
}

// SetAnswerTool updates the runtime answer state.
type SetAnswerTool struct {
	answer *Answer
}

// NewSetAnswerTool constructs a set_answer tool.
func NewSetAnswerTool(answer *Answer) *SetAnswerTool {
	return &SetAnswerTool{answer: answer}
}

func (t *SetAnswerTool) Name() string {
	return "set_answer"
}

func (t *SetAnswerTool) Description() string {
	return "Set the coordinator answer content and readiness state."
}

func (t *SetAnswerTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"content": {
				Type:        "string",
				Description: "Answer content",
			},
			"ready": {
				Type:        "boolean",
				Description: "Whether the answer is ready",
			},
			"confidence": {
				Type:        "number",
				Description: "Confidence between 0 and 1",
			},
			"artifacts": {
				Type:        "array",
				Description: "Optional artifact references",
			},
			"next_steps": {
				Type:        "array",
				Description: "Optional next steps",
			},
		},
		Required: []string{"content", "ready"},
	}
}

func (t *SetAnswerTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.answer == nil {
		return &builtin.Result{Success: false, Error: "answer state unavailable"}, nil
	}
	content, ok := params["content"].(string)
	if !ok {
		return &builtin.Result{Success: false, Error: "content must be a string"}, nil
	}
	ready := parseBool(params["ready"], false)
	confidence := parseFloat(params["confidence"], t.answer.Confidence)

	t.answer.Content = strings.TrimSpace(content)
	t.answer.Ready = ready
	t.answer.Confidence = confidence
	t.answer.Artifacts = parseStringSlice(params["artifacts"])
	t.answer.NextSteps = parseStringSlice(params["next_steps"])
	t.answer.Normalize()

	return &builtin.Result{Success: true}, nil
}

func parseSubTasks(input any) ([]SubTask, error) {
	items, ok := input.([]any)
	if !ok {
		return nil, fmt.Errorf("tasks must be a list")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("tasks list is empty")
	}

	tasks := make([]SubTask, 0, len(items))
	for _, item := range items {
		payload, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("task entry must be an object")
		}
		prompt, _ := payload["task"].(string)
		if strings.TrimSpace(prompt) == "" {
			return nil, fmt.Errorf("task prompt required")
		}
		weight := parseWeight(payload["weight"])
		if strings.TrimSpace(string(weight)) == "" {
			weight = WeightMedium
		}
		tools := parseStringSlice(payload["tools"])
		systemPrompt, _ := payload["system_prompt"].(string)
		maxIterations := parseInt(payload["max_iterations"], 0)
		id, _ := payload["id"].(string)
		tasks = append(tasks, SubTask{
			ID:            id,
			Prompt:        prompt,
			Weight:        weight,
			AllowedTools:  tools,
			SystemPrompt:  systemPrompt,
			MaxIterations: maxIterations,
		})
	}
	return tasks, nil
}

func parseWeight(input any) Weight {
	if input == nil {
		return Weight("")
	}
	if value, ok := input.(string); ok {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" {
			return Weight(value)
		}
	}
	return Weight("")
}

func parseStringSlice(input any) []string {
	if input == nil {
		return nil
	}
	items, ok := input.([]any)
	if !ok {
		if value, ok := input.([]string); ok {
			return value
		}
		if value, ok := input.(string); ok {
			value = strings.TrimSpace(value)
			if value == "" {
				return nil
			}
			return []string{value}
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				out = append(out, value)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseBool(input any, fallback bool) bool {
	switch v := input.(type) {
	case bool:
		return v
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		if s == "true" || s == "1" || s == "yes" {
			return true
		}
		if s == "false" || s == "0" || s == "no" {
			return false
		}
	}
	return fallback
}

func parseInt(input any, fallback int) int {
	switch v := input.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func parseFloat(input any, fallback float64) float64 {
	switch v := input.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(v, "%f", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func toolRegistryDefinitions(registry *tool.Registry) []map[string]any {
	if registry == nil {
		return nil
	}
	list := registry.List()
	defs := make([]map[string]any, 0, len(list))
	for _, t := range list {
		defs = append(defs, tool.ToOpenAIFunction(t))
	}
	return defs
}
