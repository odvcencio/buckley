package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

func (r *Runner) availableTools(allowed []string) []tool.Tool {
	allTools := r.config.Registry.List()
	if len(allowed) == 0 {
		return allTools
	}

	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}

	var filtered []tool.Tool
	for _, t := range allTools {
		if allowedSet[t.Name()] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// selectTools performs phase 1: ask model which tools it needs.
func (r *Runner) selectTools(ctx context.Context, req Request, tools []tool.Tool) ([]tool.Tool, error) {
	selectionContext := strings.TrimSpace(req.SelectionPrompt)
	if selectionContext == "" {
		selectionContext = lastUserMessage(req.Messages)
	}

	// Check cache first (cache key is hashed internally)
	if r.selectionCache != nil {
		if cachedNames, ok := r.selectionCache.get(selectionContext); ok {
			toolMap := make(map[string]tool.Tool, len(tools))
			for _, t := range tools {
				toolMap[t.Name()] = t
			}
			var selected []tool.Tool
			for _, name := range cachedNames {
				if t, ok := toolMap[name]; ok {
					selected = append(selected, t)
				}
			}
			if len(selected) > 0 || len(cachedNames) == 0 {
				return selected, nil
			}
			// Cache miss - cached tools no longer available
		}
	}

	var catalog strings.Builder
	catalog.WriteString("Available tools:\n")
	for _, t := range tools {
		desc := t.Description()
		if idx := strings.Index(desc, "."); idx > 0 {
			desc = desc[:idx+1]
		}
		catalog.WriteString(fmt.Sprintf("- %s: %s\n", t.Name(), desc))
	}

	selectionPrompt := fmt.Sprintf(`Given this user request:
%s

And these available tools:
%s

Which tools (if any) would you need to complete this request?
Return a JSON array of tool names, e.g., ["read_file", "write_file", "run_shell"]
If the request asks about the repo/codebase or to validate claims, include search_text and read_file.
If the request needs git history/status/diff/blame or merge info, include git_status/git_log/git_diff/git_blame (and list_merge_conflicts if relevant).
If no tools are needed, return [].
Only include tools you will actually use.`, selectionContext, catalog.String())

	messages := []model.Message{
		{Role: "user", Content: selectionPrompt},
	}

	resp, err := r.config.Models.ChatCompletion(ctx, model.ChatRequest{
		Model:      r.requestModel(req),
		Messages:   messages,
		MaxTokens:  500,
		ToolChoice: "none",
	})
	if err != nil {
		return nil, fmt.Errorf("tool selection: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from model")
	}

	content, _ := model.ExtractTextContent(resp.Choices[0].Message.Content)
	selectedNames := r.parseToolNames(content)

	// Cache the result
	if r.selectionCache != nil {
		r.selectionCache.set(selectionContext, selectedNames)
	}

	toolMap := make(map[string]tool.Tool, len(tools))
	for _, t := range tools {
		toolMap[t.Name()] = t
	}

	var selected []tool.Tool
	for _, name := range selectedNames {
		if t, ok := toolMap[name]; ok {
			selected = append(selected, t)
		}
	}

	if shouldIncludeGitTools(selectionContext) {
		selected = ensureToolSelection(selected, toolMap, []string{
			"git_status",
			"git_log",
			"git_diff",
			"git_blame",
			"list_merge_conflicts",
		})
	}

	if len(selectedNames) > 0 && len(selected) == 0 {
		return tools, nil
	}

	return selected, nil
}

func shouldIncludeGitTools(selectionContext string) bool {
	if strings.TrimSpace(selectionContext) == "" {
		return false
	}
	lower := strings.ToLower(selectionContext)
	keywords := []string{
		"git",
		"repo",
		"repository",
		"branch",
		"commit",
		"diff",
		"status",
		"log",
		"blame",
		"merge",
	}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func ensureToolSelection(selected []tool.Tool, toolMap map[string]tool.Tool, names []string) []tool.Tool {
	if len(names) == 0 || len(toolMap) == 0 {
		return selected
	}
	seen := make(map[string]struct{}, len(selected))
	for _, t := range selected {
		seen[t.Name()] = struct{}{}
	}
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		if t, ok := toolMap[name]; ok {
			selected = append(selected, t)
			seen[name] = struct{}{}
		}
	}
	return selected
}

func (r *Runner) parseToolNames(content string) []string {
	content = strings.TrimSpace(content)

	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start >= 0 && end > start {
		content = content[start : end+1]
	}

	var names []string
	if err := json.Unmarshal([]byte(content), &names); err != nil {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			line = strings.Trim(line, "-•*\"'`,[]")
			if line != "" && !strings.Contains(line, " ") {
				names = append(names, line)
			}
		}
	}

	return names
}

func lastUserMessage(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		return messageContentToString(messages[i].Content)
	}
	return ""
}

func messageContentToString(content any) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", content)
}
