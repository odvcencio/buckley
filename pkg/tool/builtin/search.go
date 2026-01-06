package builtin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// SearchTextTool searches for text using ripgrep (rg) with sensible defaults
type SearchTextTool struct{ workDirAware }

func (t *SearchTextTool) Name() string {
	return "search_text"
}

func (t *SearchTextTool) Description() string {
	return "**SEARCH CODEBASE** when user asks 'where is', 'find all', 'search for', 'grep for', 'locate'. Use this BEFORE reading files when you don't know exact locations. Supports regex patterns and glob filtering. Essential for discovering function definitions, usage patterns, imports, string literals, TODOs, or any code pattern across the project. Always prefer this over guessing file locations."
}

func (t *SearchTextTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"query": {
				Type:        "string",
				Description: "Search query (regular expression by default)",
			},
			"path": {
				Type:        "string",
				Description: "Directory or file to search (defaults to current directory)",
			},
			"case_sensitive": {
				Type:        "boolean",
				Description: "Whether the search is case sensitive (default true)",
				Default:     true,
			},
			"context_before": {
				Type:        "integer",
				Description: "Lines of context to include before each match",
				Default:     0,
			},
			"context_after": {
				Type:        "integer",
				Description: "Lines of context to include after each match",
				Default:     0,
			},
			"glob": {
				Type:        "string",
				Description: "Glob pattern to include (repeatable)",
			},
		},
		Required: []string{"query"},
	}
}

func (t *SearchTextTool) Execute(params map[string]any) (*Result, error) {
	query, ok := params["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return &Result{
			Success: false,
			Error:   "query parameter must be a non-empty string",
		}, nil
	}

	searchPath := "."
	if p, ok := params["path"].(string); ok && strings.TrimSpace(p) != "" {
		searchPath = p
	}
	effectiveSearchPath := searchPath
	workDir := strings.TrimSpace(t.workDir)
	if workDir != "" {
		if _, rel, err := resolveRelPath(workDir, searchPath); err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		} else if strings.TrimSpace(rel) != "" {
			effectiveSearchPath = rel
		}
	}

	caseSensitive := true
	if v, ok := params["case_sensitive"]; ok {
		caseSensitive = parseBool(v, true)
	}

	contextBefore := parseInt(params["context_before"], 0)
	contextAfter := parseInt(params["context_after"], 0)

	globs := extractGlobParams(params["glob"])

	useRG := toolExists("rg")
	var cmd *exec.Cmd
	var toolName string

	ctx, cancel := t.execContext()
	defer cancel()

	if useRG {
		args := []string{"--line-number", "--column", "--no-heading", "--color", "never"}
		if !caseSensitive {
			args = append(args, "-i")
		}
		if contextBefore > 0 {
			args = append(args, fmt.Sprintf("-B%d", contextBefore))
		}
		if contextAfter > 0 {
			args = append(args, fmt.Sprintf("-A%d", contextAfter))
		}
		for _, g := range globs {
			args = append(args, "--glob", g)
		}
		args = append(args, query, effectiveSearchPath)
		cmd = exec.CommandContext(ctx, "rg", args...)
		toolName = "rg"
	} else {
		args := []string{"-n", "-r"}
		if !caseSensitive {
			args = append(args, "-i")
		}
		args = append(args, query, effectiveSearchPath)
		cmd = exec.CommandContext(ctx, "grep", args...)
		toolName = "grep"
	}
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "search command timed out",
		}, nil
	}
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("search failed: %v (%s)", err, strings.TrimSpace(stderr.String())),
			}, nil
		}
	}

	output := stdout.String()
	if exitCode == 1 && strings.TrimSpace(output) == "" {
		return &Result{
			Success: true,
			Data: map[string]any{
				"matches": []map[string]any{},
				"count":   0,
				"tool":    toolName,
			},
		}, nil
	}
	if exitCode != 0 {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("search command failed: %s", strings.TrimSpace(stderr.String())),
		}, nil
	}

	matchLines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	matches := make([]map[string]any, 0, len(matchLines))

	for _, line := range matchLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := parseSearchLine(line)
		if match != nil {
			matches = append(matches, match)
		}
	}

	const maxDisplayMatches = 50
	shouldAbridge := len(matches) > maxDisplayMatches

	result := &Result{
		Success: true,
		Data: map[string]any{
			"matches": matches,
			"count":   len(matches),
			"tool":    toolName,
		},
		ShouldAbridge: shouldAbridge,
	}

	// Limit matches shown in conversation
	if shouldAbridge {
		displayMatches := matches[:maxDisplayMatches]
		result.DisplayData = map[string]any{
			"matches": displayMatches,
			"count":   len(matches),
			"tool":    toolName,
			"summary": fmt.Sprintf("Found %d matches (showing first %d)", len(matches), maxDisplayMatches),
		}
	}

	return result, nil
}

func parseSearchLine(line string) map[string]any {
	parts := strings.SplitN(line, ":", 4)
	if len(parts) < 2 {
		return nil
	}

	path := strings.TrimSpace(parts[0])
	lineNum := 0
	column := 0
	content := ""

	if len(parts) >= 3 {
		lineNum = parseInt(parts[1], 0)
		column = parseInt(parts[2], 0)
		if len(parts) == 4 {
			content = parts[3]
		}
	} else if len(parts) == 2 {
		lineNum = parseInt(parts[1], 0)
	}

	return map[string]any{
		"path":    path,
		"line":    lineNum,
		"column":  column,
		"match":   content,
		"context": "",
	}
}

// SearchReplaceTool performs search and replace within a file
type SearchReplaceTool struct{ workDirAware }

func (t *SearchReplaceTool) Name() string {
	return "search_replace"
}

func (t *SearchReplaceTool) Description() string {
	return "Search and replace text in a file with literal or regex patterns. Supports case sensitivity control and replacement limits. Shows modification summary (replacements count, line changes) instead of full file content. Use this to update code patterns, fix typos, or refactor naming."
}

func (t *SearchReplaceTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "File to modify",
			},
			"search": {
				Type:        "string",
				Description: "Search pattern (interpreted as literal unless use_regex=true)",
			},
			"replace": {
				Type:        "string",
				Description: "Replacement text",
			},
			"use_regex": {
				Type:        "boolean",
				Description: "Treat search as regular expression",
				Default:     false,
			},
			"case_sensitive": {
				Type:        "boolean",
				Description: "Whether the search is case sensitive (default true)",
				Default:     true,
			},
			"max_replacements": {
				Type:        "integer",
				Description: "Maximum number of replacements (<=0 for unlimited)",
				Default:     0,
			},
		},
		Required: []string{"path", "search", "replace"},
	}
}

func (t *SearchReplaceTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}

	search, ok := params["search"].(string)
	if !ok || search == "" {
		return &Result{
			Success: false,
			Error:   "search parameter must be a non-empty string",
		}, nil
	}

	replace, ok := params["replace"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "replace parameter must be a string",
		}, nil
	}

	useRegex := parseBool(params["use_regex"], false)
	caseSensitive := parseBool(params["case_sensitive"], true)
	maxReplacements := parseInt(params["max_replacements"], 0)

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	pattern := search
	if !useRegex {
		pattern = regexp.QuoteMeta(search)
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("invalid pattern: %v", err),
		}, nil
	}

	original := string(content)
	var replaced string
	var replacements int

	if maxReplacements > 0 {
		replaced, replacements = replaceLimited(re, original, replace, maxReplacements)
	} else {
		replaced = re.ReplaceAllString(original, replace)
		replacements = countMatches(re, original)
	}

	if replacements == 0 {
		return &Result{
			Success: true,
			Data: map[string]any{
				"path":         absPath,
				"replacements": 0,
			},
		}, nil
	}

	if err := os.WriteFile(absPath, []byte(replaced), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	// Calculate line changes
	oldLines := strings.Split(original, "\n")
	newLines := strings.Split(replaced, "\n")

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":           absPath,
			"replacements":   replacements,
			"original":       original,
			"replaced":       replaced,
			"original_lines": len(oldLines),
			"new_lines":      len(newLines),
		},
		ShouldAbridge: true,
	}

	// Show compact summary in conversation
	summary := fmt.Sprintf("✎ Modified %s: %d replacement(s), %d→%d lines",
		filepath.Base(absPath), replacements, len(oldLines), len(newLines))

	result.DisplayData = map[string]any{
		"path":         absPath,
		"replacements": replacements,
		"summary":      summary,
		"old_lines":    len(oldLines),
		"new_lines":    len(newLines),
	}

	return result, nil
}

func replaceLimited(re *regexp.Regexp, input, replacement string, limit int) (string, int) {
	matches := re.FindAllStringIndex(input, -1)
	if len(matches) == 0 {
		return input, 0
	}

	var builder strings.Builder
	lastIndex := 0
	replacements := 0

	for _, loc := range matches {
		if replacements >= limit {
			break
		}
		start, end := loc[0], loc[1]
		builder.WriteString(input[lastIndex:start])
		builder.WriteString(replacement)
		lastIndex = end
		replacements++
	}

	builder.WriteString(input[lastIndex:])
	return builder.String(), replacements
}

func countMatches(re *regexp.Regexp, input string) int {
	return len(re.FindAllStringIndex(input, -1))
}

func parseBool(value any, defaultVal bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		default:
			return defaultVal
		}
	default:
		return defaultVal
	}
}

func parseInt(value any, defaultVal int) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return defaultVal
		}
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return defaultVal
		}
		return i
	default:
		return defaultVal
	}
}

func extractGlobParams(value any) []string {
	globs := []string{}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			globs = append(globs, v)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				globs = append(globs, s)
			}
		}
	}
	return globs
}

func toolExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
