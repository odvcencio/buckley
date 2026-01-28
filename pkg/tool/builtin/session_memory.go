package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/odvcencio/buckley/pkg/ralph"
)

// SessionMemoryTool exposes Ralph session memory queries to the agent.
type SessionMemoryTool struct {
	Store     *ralph.MemoryStore
	SessionID string
}

func (t *SessionMemoryTool) Name() string {
	return "session_memory"
}

func (t *SessionMemoryTool) Description() string {
	return "Query Ralph session memory across raw turns, structured events, and summaries. Use for recalling past attempts, errors, tool calls, or decisions in the current session."
}

func (t *SessionMemoryTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: search, list_summaries, get_turn",
			},
			"tier": {
				Type:        "string",
				Description: "Memory tier for search: raw, events, summary",
				Default:     "summary",
			},
			"query": {
				Type:        "string",
				Description: "Search query for search action",
			},
			"limit": {
				Type:        "string",
				Description: "Maximum results to return (default: 5)",
			},
			"since": {
				Type:        "string",
				Description: "Minimum iteration number for list_summaries or event filters",
			},
			"until": {
				Type:        "string",
				Description: "Maximum iteration number for event filters",
			},
			"iteration": {
				Type:        "string",
				Description: "Iteration number for get_turn",
			},
			"event_types": {
				Type:        "string",
				Description: "Comma-separated event types for event search",
			},
			"tools": {
				Type:        "string",
				Description: "Comma-separated tool names for event search",
			},
			"file_paths": {
				Type:        "string",
				Description: "Comma-separated file path globs for event search",
			},
			"has_error": {
				Type:        "string",
				Description: "Filter events by error presence: true or false",
			},
		},
		Required: []string{"action"},
	}
}

func (t *SessionMemoryTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *SessionMemoryTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	if t.Store == nil {
		return &Result{Success: false, Error: "session memory store not configured"}, nil
	}
	if strings.TrimSpace(t.SessionID) == "" {
		return &Result{Success: false, Error: "session id not configured"}, nil
	}

	action := strings.ToLower(stringParam(params, "action"))
	switch action {
	case "search":
		return t.handleSearch(ctx, params)
	case "list_summaries":
		return t.handleListSummaries(ctx, params)
	case "get_turn":
		return t.handleGetTurn(ctx, params)
	default:
		return &Result{Success: false, Error: fmt.Sprintf("unknown action: %s", action)}, nil
	}
}

func (t *SessionMemoryTool) handleSearch(ctx context.Context, params map[string]any) (*Result, error) {
	tier := strings.ToLower(stringParam(params, "tier"))
	if tier == "" {
		tier = "summary"
	}
	query := strings.TrimSpace(stringParam(params, "query"))
	if query == "" {
		return &Result{Success: false, Error: "query parameter is required"}, nil
	}
	limit := intParam(params, "limit", 5)
	if ctx == nil {
		ctx = context.Background()
	}

	switch tier {
	case "raw":
		turns, err := t.Store.SearchTurns(ctx, t.SessionID, query, limit)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		items := make([]map[string]any, 0, len(turns))
		for _, turn := range turns {
			items = append(items, map[string]any{
				"iteration": turn.Iteration,
				"backend":   turn.Backend,
				"model":     turn.Model,
				"prompt":    truncateString(turn.Prompt, 300),
				"response":  truncateString(turn.Response, 500),
				"error":     turn.Error,
			})
		}
		return &Result{Success: true, Data: map[string]any{
			"tier":    "raw",
			"query":   query,
			"count":   len(items),
			"results": items,
		}}, nil

	case "events":
		eq := ralph.EventQuery{
			SessionID:  t.SessionID,
			EventTypes: splitCSV(stringParam(params, "event_types")),
			Tools:      splitCSV(stringParam(params, "tools")),
			FilePaths:  splitCSV(stringParam(params, "file_paths")),
			Since:      intParam(params, "since", 0),
			Until:      intParam(params, "until", 0),
			Query:      query,
			Limit:      limit,
		}
		if hasError, ok := boolParam(params, "has_error"); ok {
			eq.HasError = &hasError
		}
		events, err := t.Store.SearchEvents(ctx, eq)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		items := make([]map[string]any, 0, len(events))
		for _, evt := range events {
			items = append(items, map[string]any{
				"iteration": evt.Iteration,
				"type":      evt.EventType,
				"tool":      evt.Tool,
				"file":      evt.FilePath,
				"has_error": evt.HasError,
				"data":      evt.Data,
			})
		}
		return &Result{Success: true, Data: map[string]any{
			"tier":    "events",
			"query":   query,
			"count":   len(items),
			"results": items,
		}}, nil

	case "summary":
		summaries, err := t.Store.SearchSummaries(ctx, t.SessionID, query, limit)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		items := make([]map[string]any, 0, len(summaries))
		for _, summary := range summaries {
			items = append(items, map[string]any{
				"start_iteration": summary.StartIteration,
				"end_iteration":   summary.EndIteration,
				"summary":         truncateString(summary.Summary, 500),
				"key_decisions":   summary.KeyDecisions,
				"errors":          summary.ErrorPatterns,
			})
		}
		return &Result{Success: true, Data: map[string]any{
			"tier":    "summary",
			"query":   query,
			"count":   len(items),
			"results": items,
		}}, nil
	default:
		return &Result{Success: false, Error: fmt.Sprintf("unknown tier: %s", tier)}, nil
	}
}

func (t *SessionMemoryTool) handleListSummaries(ctx context.Context, params map[string]any) (*Result, error) {
	since := intParam(params, "since", 0)
	limit := intParam(params, "limit", 10)
	if ctx == nil {
		ctx = context.Background()
	}

	summaries, err := t.Store.ListSummaries(ctx, t.SessionID, since, limit)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	items := make([]map[string]any, 0, len(summaries))
	for _, summary := range summaries {
		items = append(items, map[string]any{
			"start_iteration": summary.StartIteration,
			"end_iteration":   summary.EndIteration,
			"summary":         truncateString(summary.Summary, 500),
			"key_decisions":   summary.KeyDecisions,
			"errors":          summary.ErrorPatterns,
		})
	}
	return &Result{Success: true, Data: map[string]any{
		"count":   len(items),
		"results": items,
	}}, nil
}

func (t *SessionMemoryTool) handleGetTurn(ctx context.Context, params map[string]any) (*Result, error) {
	iteration := intParam(params, "iteration", 0)
	if iteration <= 0 {
		return &Result{Success: false, Error: "iteration parameter is required"}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	turns, err := t.Store.GetTurnsByIteration(ctx, t.SessionID, iteration)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	items := make([]map[string]any, 0, len(turns))
	for _, turn := range turns {
		items = append(items, map[string]any{
			"iteration": turn.Iteration,
			"backend":   turn.Backend,
			"model":     turn.Model,
			"prompt":    truncateString(turn.Prompt, 500),
			"response":  truncateString(turn.Response, 1000),
			"error":     turn.Error,
		})
	}
	return &Result{Success: true, Data: map[string]any{
		"count":   len(items),
		"results": items,
	}}, nil
}

func stringParam(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	value, ok := params[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func intParam(params map[string]any, key string, fallback int) int {
	if params == nil {
		return fallback
	}
	value, ok := params[key]
	if !ok || value == nil {
		return fallback
	}
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	}
	return fallback
}

func boolParam(params map[string]any, key string) (bool, bool) {
	if params == nil {
		return false, false
	}
	value, ok := params[key]
	if !ok || value == nil {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(v))
		if trimmed == "true" || trimmed == "1" || trimmed == "yes" {
			return true, true
		}
		if trimmed == "false" || trimmed == "0" || trimmed == "no" {
			return false, true
		}
	}
	return false, false
}

func splitCSV(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func truncateString(value string, max int) string {
	if max <= 0 {
		return value
	}
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
