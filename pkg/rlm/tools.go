package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// DelegateTool dispatches a single task to a sub-agent.
type DelegateTool struct {
	dispatcher  *Dispatcher
	ctxProvider func() context.Context
}

// NewDelegateTool constructs a delegate tool.
func NewDelegateTool(dispatcher *Dispatcher, ctxProvider func() context.Context) *DelegateTool {
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
			"tools": {
				Type:        "array",
				Description: "Optional list of allowed tools (nil = all tools)",
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
	data := map[string]any{
		"summary":        res.Summary,
		"scratchpad_key": res.RawKey,
		"agent_id":       res.AgentID,
		"model":          res.ModelUsed,
		"tokens_used":    res.TokensUsed,
		"duration_ms":    res.Duration.Milliseconds(),
		"error":          res.Error,
	}
	// Include tool call summary for transparency
	if len(res.ToolCalls) > 0 {
		data["tool_calls_count"] = len(res.ToolCalls)
	}
	return &builtin.Result{
		Success: res.Error == "",
		Data:    data,
		Error:   res.Error,
	}, nil
}

// DelegateBatchTool dispatches multiple tasks at once.
type DelegateBatchTool struct {
	dispatcher  *Dispatcher
	ctxProvider func() context.Context
}

// NewDelegateBatchTool constructs a delegate_batch tool.
func NewDelegateBatchTool(dispatcher *Dispatcher, ctxProvider func() context.Context) *DelegateBatchTool {
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
				Description: "List of tasks with task, tools, system_prompt",
			},
			"parallel": {
				Type:        "boolean",
				Description: "Run tasks in parallel (default true)",
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
		item := map[string]any{
			"task_id":        res.TaskID,
			"agent_id":       res.AgentID,
			"summary":        res.Summary,
			"scratchpad_key": res.RawKey,
			"model":          res.ModelUsed,
			"tokens_used":    res.TokensUsed,
			"duration_ms":    res.Duration.Milliseconds(),
			"error":          res.Error,
		}
		if len(res.ToolCalls) > 0 {
			item["tool_calls_count"] = len(res.ToolCalls)
		}
		data = append(data, item)
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

// InspectRawTool returns full scratchpad entry content including raw data.
type InspectRawTool struct {
	scratchpad  *Scratchpad
	ctxProvider func() context.Context
}

// NewInspectRawTool constructs an inspect_raw tool.
func NewInspectRawTool(scratchpad *Scratchpad, ctxProvider func() context.Context) *InspectRawTool {
	return &InspectRawTool{scratchpad: scratchpad, ctxProvider: ctxProvider}
}

func (t *InspectRawTool) Name() string {
	return "inspect_raw"
}

func (t *InspectRawTool) Description() string {
	return "Inspect a scratchpad entry with full raw content. Use when summary is insufficient."
}

func (t *InspectRawTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"key": {
				Type:        "string",
				Description: "Scratchpad entry key",
			},
			"max_length": {
				Type:        "integer",
				Description: "Max raw content length to return (default 10000)",
			},
		},
		Required: []string{"key"},
	}
}

func (t *InspectRawTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.scratchpad == nil {
		return &builtin.Result{Success: false, Error: "scratchpad unavailable"}, nil
	}
	key, ok := params["key"].(string)
	if !ok || strings.TrimSpace(key) == "" {
		return &builtin.Result{Success: false, Error: "key must be a non-empty string"}, nil
	}

	maxLength := parseInt(params["max_length"], 10000)
	if maxLength <= 0 {
		maxLength = 10000
	}

	ctx := context.Background()
	if t.ctxProvider != nil {
		ctx = t.ctxProvider()
	}

	entry, err := t.scratchpad.InspectRaw(ctx, key)
	if err != nil {
		return &builtin.Result{Success: false, Error: err.Error()}, nil
	}
	if entry == nil {
		return &builtin.Result{Success: false, Error: "scratchpad entry not found"}, nil
	}

	rawContent := string(entry.Raw)
	truncated := false
	if len(rawContent) > maxLength {
		rawContent = rawContent[:maxLength]
		truncated = true
	}

	return &builtin.Result{
		Success: true,
		Data: map[string]any{
			"key":        entry.Key,
			"type":       string(entry.Type),
			"raw":        rawContent,
			"truncated":  truncated,
			"total_size": len(entry.Raw),
			"summary":    entry.Summary,
			"metadata":   entry.Metadata,
			"created_by": entry.CreatedBy,
			"created_at": entry.CreatedAt,
		},
	}, nil
}

// ListScratchpadTool lists recent scratchpad entries.
type ListScratchpadTool struct {
	scratchpad  *Scratchpad
	ctxProvider func() context.Context
}

// NewListScratchpadTool constructs a list_scratchpad tool.
func NewListScratchpadTool(scratchpad *Scratchpad, ctxProvider func() context.Context) *ListScratchpadTool {
	return &ListScratchpadTool{scratchpad: scratchpad, ctxProvider: ctxProvider}
}

func (t *ListScratchpadTool) Name() string {
	return "list_scratchpad"
}

func (t *ListScratchpadTool) Description() string {
	return "List recent scratchpad entries. Use to see what data is available without searching."
}

func (t *ListScratchpadTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"limit": {
				Type:        "integer",
				Description: "Max entries to return (default 20)",
			},
			"type": {
				Type:        "string",
				Description: "Optional filter by entry type: file, command, analysis, decision, artifact, strategy",
			},
		},
	}
}

func (t *ListScratchpadTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.scratchpad == nil {
		return &builtin.Result{Success: false, Error: "scratchpad unavailable"}, nil
	}

	limit := parseInt(params["limit"], 20)
	if limit <= 0 {
		limit = 20
	}
	typeFilter, _ := params["type"].(string)

	ctx := context.Background()
	if t.ctxProvider != nil {
		ctx = t.ctxProvider()
	}

	var filtered []EntrySummary
	if strings.TrimSpace(typeFilter) != "" {
		entries, err := t.scratchpad.ListSummariesByType(ctx, EntryType(typeFilter), limit)
		if err != nil {
			return &builtin.Result{Success: false, Error: err.Error()}, nil
		}
		filtered = entries
	} else {
		entries, err := t.scratchpad.ListSummaries(ctx, limit)
		if err != nil {
			return &builtin.Result{Success: false, Error: err.Error()}, nil
		}
		filtered = entries
	}

	data := make([]map[string]any, 0, len(filtered))
	for _, entry := range filtered {
		data = append(data, map[string]any{
			"key":        entry.Key,
			"type":       string(entry.Type),
			"summary":    entry.Summary,
			"created_by": entry.CreatedBy,
			"created_at": entry.CreatedAt,
		})
	}

	return &builtin.Result{
		Success: true,
		Data: map[string]any{
			"entries": data,
			"count":   len(data),
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
		tools := parseStringSlice(payload["tools"])
		systemPrompt, _ := payload["system_prompt"].(string)
		maxIterations := parseInt(payload["max_iterations"], 0)
		id, _ := payload["id"].(string)
		tasks = append(tasks, SubTask{
			ID:            id,
			Prompt:        prompt,
			AllowedTools:  tools,
			SystemPrompt:  systemPrompt,
			MaxIterations: maxIterations,
		})
	}
	return tasks, nil
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

// SearchScratchpadTool provides semantic search over scratchpad entries.
type SearchScratchpadTool struct {
	rag         *ScratchpadRAG
	ctxProvider func() context.Context
}

// NewSearchScratchpadTool constructs a search_scratchpad tool.
func NewSearchScratchpadTool(rag *ScratchpadRAG, ctxProvider func() context.Context) *SearchScratchpadTool {
	return &SearchScratchpadTool{rag: rag, ctxProvider: ctxProvider}
}

func (t *SearchScratchpadTool) Name() string {
	return "search_scratchpad"
}

func (t *SearchScratchpadTool) Description() string {
	return "Semantically search the scratchpad for relevant past work. Returns entries most similar to your query."
}

func (t *SearchScratchpadTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"query": {
				Type:        "string",
				Description: "Search query describing what you're looking for",
			},
			"type": {
				Type:        "string",
				Description: "Optional filter by entry type: file, command, analysis, decision, artifact, strategy",
			},
			"limit": {
				Type:        "integer",
				Description: "Max results to return (default 5)",
			},
		},
		Required: []string{"query"},
	}
}

func (t *SearchScratchpadTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.rag == nil {
		return &builtin.Result{Success: false, Error: "scratchpad search unavailable"}, nil
	}

	query, _ := params["query"].(string)
	if strings.TrimSpace(query) == "" {
		return &builtin.Result{Success: false, Error: "query required"}, nil
	}

	limit := parseInt(params["limit"], 5)
	entryType, _ := params["type"].(string)

	ctx := context.Background()
	if t.ctxProvider != nil {
		ctx = t.ctxProvider()
	}

	var results []RAGSearchResult
	var err error

	if strings.TrimSpace(entryType) != "" {
		results, err = t.rag.SearchByType(ctx, query, EntryType(entryType), limit)
	} else {
		results, err = t.rag.Search(ctx, query, limit)
	}

	if err != nil {
		return &builtin.Result{Success: false, Error: err.Error()}, nil
	}

	// Format results for coordinator
	data := make([]map[string]any, 0, len(results))
	for _, r := range results {
		data = append(data, map[string]any{
			"key":        r.Entry.Key,
			"type":       string(r.Entry.Type),
			"summary":    r.Entry.Summary,
			"created_by": r.Entry.CreatedBy,
			"similarity": r.Similarity,
			"rank":       r.Rank,
		})
	}

	return &builtin.Result{
		Success: true,
		Data: map[string]any{
			"results": data,
			"query":   query,
			"count":   len(results),
		},
	}, nil
}

// RecordStrategyTool persists strategic decisions for context learning.
type RecordStrategyTool struct {
	scratchpad  ScratchpadWriter
	ctxProvider func() context.Context
}

// NewRecordStrategyTool constructs a record_strategy tool.
func NewRecordStrategyTool(scratchpad ScratchpadWriter, ctxProvider func() context.Context) *RecordStrategyTool {
	return &RecordStrategyTool{scratchpad: scratchpad, ctxProvider: ctxProvider}
}

func (t *RecordStrategyTool) Name() string {
	return "record_strategy"
}

func (t *RecordStrategyTool) Description() string {
	return "Record a strategic decision or approach for future reference. Use this to persist decomposition strategies, tier selection rationale, or lessons learned."
}

func (t *RecordStrategyTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"category": {
				Type:        "string",
				Description: "Strategy category: decomposition, tier_selection, retry_approach, or lesson_learned",
			},
			"summary": {
				Type:        "string",
				Description: "Brief summary of the strategy (shown in context)",
			},
			"details": {
				Type:        "string",
				Description: "Full details of the strategic decision",
			},
			"rationale": {
				Type:        "string",
				Description: "Why this strategy was chosen",
			},
		},
		Required: []string{"category", "summary"},
	}
}

// Valid strategy categories
var validStrategyCategories = map[string]bool{
	"decomposition":  true,
	"approach":       true,
	"retry_approach": true,
	"lesson_learned": true,
	"architecture":   true,
	"optimization":   true,
	"error_handling": true,
	"decision":       true,
}

func (t *RecordStrategyTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.scratchpad == nil {
		return &builtin.Result{Success: false, Error: "scratchpad unavailable"}, nil
	}

	category, _ := params["category"].(string)
	summary, _ := params["summary"].(string)
	details, _ := params["details"].(string)
	rationale, _ := params["rationale"].(string)

	category = strings.TrimSpace(strings.ToLower(category))
	if category == "" {
		return &builtin.Result{Success: false, Error: "category required"}, nil
	}
	// Validate category
	if !validStrategyCategories[category] {
		validList := make([]string, 0, len(validStrategyCategories))
		for k := range validStrategyCategories {
			validList = append(validList, k)
		}
		return &builtin.Result{
			Success: false,
			Error:   fmt.Sprintf("invalid category %q; valid categories: %s", category, strings.Join(validList, ", ")),
		}, nil
	}
	if strings.TrimSpace(summary) == "" {
		return &builtin.Result{Success: false, Error: "summary required"}, nil
	}

	ctx := context.Background()
	if t.ctxProvider != nil {
		ctx = t.ctxProvider()
	}

	// Build structured raw content
	raw := map[string]string{
		"category":  category,
		"summary":   summary,
		"details":   details,
		"rationale": rationale,
	}
	rawBytes, _ := json.Marshal(raw)

	key, err := t.scratchpad.Write(ctx, WriteRequest{
		Type:      EntryTypeStrategy,
		Raw:       rawBytes,
		Summary:   fmt.Sprintf("[%s] %s", category, summary),
		Metadata:  map[string]any{"category": category},
		CreatedBy: "coordinator",
	})
	if err != nil {
		return &builtin.Result{Success: false, Error: err.Error()}, nil
	}

	return &builtin.Result{
		Success: true,
		Data: map[string]any{
			"key":      key,
			"category": category,
			"summary":  summary,
		},
	}, nil
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
