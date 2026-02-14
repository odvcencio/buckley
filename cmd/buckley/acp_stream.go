package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/acp"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func sendACPToolCallStart(stream acp.StreamFunc, call model.ToolCall, params map[string]any) {
	if stream == nil {
		return
	}
	update := acp.SessionUpdate{
		SessionUpdate: acp.SessionUpdateToolCall,
		ToolCallID:    call.ID,
		Title:         toolCallTitle(call.Function.Name, params),
		Kind:          toolCallKind(call.Function.Name),
		Status:        acp.ToolCallStatusInProgress,
		RawInput:      params,
		Locations:     toolCallLocations(params),
	}
	_ = stream(update)
}

func sendACPToolCallUpdate(stream acp.StreamFunc, call model.ToolCall, params map[string]any, status, text string, rawOutput any, result *builtin.Result) {
	if stream == nil {
		return
	}
	update := acp.SessionUpdate{
		SessionUpdate: acp.SessionUpdateToolCallUpdate,
		ToolCallID:    call.ID,
		Status:        status,
		RawOutput:     rawOutput,
	}
	contents := buildToolCallContents(text, result)
	if len(contents) > 0 {
		update.Content = contents
	}
	_ = stream(update)
}

func sendACPPhaseUpdate(stream acp.StreamFunc, last, message string) string {
	message = strings.TrimSpace(message)
	if stream == nil || message == "" || message == last {
		return last
	}
	_ = stream(acp.NewAgentThoughtChunk(message))
	return message
}

func buildToolCallContents(text string, result *builtin.Result) []acp.ToolCallContent {
	const maxText = 8000
	var contents []acp.ToolCallContent
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		contents = append(contents, acp.ToolCallContent{
			Type:    "content",
			Content: &acp.ContentBlock{Type: "text", Text: truncateWithLimit(trimmed, maxText)},
		})
	}

	if result != nil {
		contents = append(contents, toolCallOutputContents(result, maxText)...)
		if result.DiffPreview != nil {
			diff := result.DiffPreview
			path := strings.TrimSpace(diff.FilePath)
			if path != "" {
				if abs, err := filepath.Abs(path); err == nil {
					path = abs
				}
			}
			oldText := strings.TrimSpace(diff.OldContent)
			newText := strings.TrimSpace(diff.NewContent)
			var oldPtr *string
			var newPtr *string
			if oldText != "" {
				oldText = truncateWithLimit(oldText, maxText)
				oldPtr = &oldText
			}
			if newText != "" {
				newText = truncateWithLimit(newText, maxText)
				newPtr = &newText
			}
			if preview := strings.TrimSpace(diff.Preview); preview != "" {
				contents = append(contents, acp.ToolCallContent{
					Type:    "content",
					Content: &acp.ContentBlock{Type: "text", Text: truncateWithLimit("[DIFF]\n"+preview, maxText)},
				})
			}
			contents = append(contents, acp.ToolCallContent{
				Type:    "diff",
				Path:    path,
				OldText: oldPtr,
				NewText: newPtr,
			})
		}
	}

	return contents
}

func toolCallOutputContents(result *builtin.Result, maxText int) []acp.ToolCallContent {
	if result == nil || len(result.Data) == 0 {
		return nil
	}

	var contents []acp.ToolCallContent
	for _, key := range []string{"stdout", "stderr", "output"} {
		val, ok := result.Data[key]
		if !ok {
			continue
		}
		if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
			label := strings.ToUpper(key)
			text := fmt.Sprintf("[%s]\n%s", label, truncateWithLimit(s, maxText))
			contents = append(contents, acp.ToolCallContent{
				Type:    "content",
				Content: &acp.ContentBlock{Type: "text", Text: text},
			})
		}
	}
	return contents
}

func toolCallKind(name string) string {
	switch name {
	case "read_file", "list_directory", "find_files", "file_exists",
		"git_status", "git_diff", "git_log", "git_blame", "list_merge_conflicts":
		return acp.ToolKindRead
	case "search_text", "search_replace", "find_symbol", "find_references", "analyze_complexity", "find_duplicates":
		return acp.ToolKindSearch
	case "write_file", "edit_file", "insert_text", "delete_lines", "patch_file", "rename_symbol", "extract_function", "mark_resolved":
		return acp.ToolKindEdit
	case "shell_command", "run_tests", "terminal_editor":
		return acp.ToolKindExecute
	case "headless_browse":
		return acp.ToolKindFetch
	default:
		return acp.ToolKindOther
	}
}

func toolCallTitle(name string, params map[string]any) string {
	switch name {
	case "read_file":
		if path := toolCallParamString(params, "path"); path != "" {
			return "Read " + path
		}
	case "write_file":
		if path := toolCallParamString(params, "path"); path != "" {
			return "Write " + path
		}
	case "edit_file", "insert_text", "delete_lines", "patch_file":
		if path := toolCallParamString(params, "path"); path != "" {
			return "Edit " + path
		}
	case "search_text":
		if query := toolCallParamString(params, "query"); query != "" {
			return "Search: " + truncate(query, 80)
		}
	case "search_replace":
		if query := toolCallParamString(params, "query"); query != "" {
			return "Search/replace: " + truncate(query, 80)
		}
	case "shell_command":
		if cmd := toolCallParamString(params, "command"); cmd != "" {
			return "Run: " + truncate(cmd, 80)
		}
	case "run_tests":
		if target := toolCallParamString(params, "target"); target != "" {
			return "Run tests: " + truncate(target, 80)
		}
	}
	return name
}

func toolCallLocations(params map[string]any) []acp.ToolCallLocation {
	path := toolCallParamString(params, "path")
	if path == "" {
		return nil
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return []acp.ToolCallLocation{{Path: path}}
}

func toolCallParamString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	if val, ok := params[key]; ok {
		if s, ok := val.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func toolCallRawOutput(result *builtin.Result, err error) any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if result == nil {
		return nil
	}
	payload := map[string]any{
		"success": result.Success,
	}
	if result.Error != "" {
		payload["error"] = result.Error
	}
	if len(result.Data) > 0 {
		payload["data"] = result.Data
	}
	if len(result.DisplayData) > 0 {
		payload["display"] = result.DisplayData
	}
	return payload
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func truncateWithLimit(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
