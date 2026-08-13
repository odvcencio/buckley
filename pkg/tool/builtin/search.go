package builtin

import (
	"bufio"
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
	return "Search text with regex, globs, context, hidden repository paths, and bounded pages. Use next_offset until absent to exhaust a large result set; .git is excluded."
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
			"offset": {
				Type:        "integer",
				Description: "Zero-based record offset for paging large result sets (default 0)",
				Default:     0,
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum match/context records returned per page (default 50, max 500)",
				Default:     50,
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
	absSearchPath, effectiveSearchPath, pathErr := resolveRelPath(t.workDir, searchPath)
	if pathErr != nil {
		return &Result{Success: false, Error: pathErr.Error()}, nil
	}
	if _, statErr := os.Stat(absSearchPath); statErr != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to inspect search path: %v", statErr)}, nil
	}
	workDir := strings.TrimSpace(t.workDir)
	if strings.TrimSpace(effectiveSearchPath) == "" {
		effectiveSearchPath = "."
	}
	if effectiveSearchPath == "-" {
		effectiveSearchPath = "./-"
	}

	caseSensitive := true
	if v, ok := params["case_sensitive"]; ok {
		caseSensitive = parseBool(v, true)
	}

	contextBefore := parseInt(params["context_before"], 0)
	contextAfter := parseInt(params["context_after"], 0)
	offset := parseInt(params["offset"], 0)
	limit := parseInt(params["limit"], 50)
	if offset < 0 {
		return &Result{Success: false, Error: "offset must not be negative"}, nil
	}
	if limit <= 0 || limit > 500 {
		return &Result{Success: false, Error: "limit must be between 1 and 500"}, nil
	}

	globs := extractGlobParams(params["glob"])

	useRG := toolExists("rg")
	var cmd *exec.Cmd
	var toolName string

	ctx, cancel := t.execContext()
	defer cancel()

	if useRG {
		args := []string{
			"--no-config", "--no-follow", "--hidden", "--glob", "!.git/**", "--with-filename", "--line-number", "--column", "--no-heading", "--color", "never",
			"--field-match-separator=\x1e", "--field-context-separator=\x1f", "--no-context-separator",
		}
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
		args = append(args, "--", query, effectiveSearchPath)
		cmd = exec.CommandContext(ctx, "rg", args...)
		toolName = "rg"
	} else {
		args := []string{"-n", "-r", "-H", "-Z", "--exclude-dir=.git", "--binary-files=without-match"}
		if !caseSensitive {
			args = append(args, "-i")
		}
		if contextBefore > 0 {
			args = append(args, fmt.Sprintf("-B%d", contextBefore))
		}
		if contextAfter > 0 {
			args = append(args, fmt.Sprintf("-A%d", contextAfter))
		}
		for _, glob := range globs {
			args = append(args, "--include="+glob)
		}
		args = append(args, "--", query, effectiveSearchPath)
		cmd = exec.CommandContext(ctx, "grep", args...)
		toolName = "grep"
	}
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("search output: %v", err)}, nil
	}
	stderr := newCappedSearchBuffer(64 << 10)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("search failed: %v", err)}, nil
	}

	matches := make([]map[string]any, 0, limit)
	matchCount := 0
	recordCount := 0
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := parseSearchLine(line)
		if match == nil {
			continue
		}
		if recordCount >= offset && len(matches) < limit {
			matches = append(matches, match)
		}
		recordCount++
		if match["kind"] == "match" {
			matchCount++
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "search command timed out",
		}, nil
	}
	if scanErr != nil {
		return &Result{Success: false, Error: fmt.Sprintf("search output could not be parsed safely: %v", scanErr)}, nil
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

	if exitCode == 1 && recordCount == 0 {
		return &Result{
			Success: true,
			Data: map[string]any{
				"matches": []map[string]any{},
				"count":   0,
				"records": 0,
				"offset":  0,
				"limit":   limit,
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

	pageStart := offset
	pageEnd := pageStart + len(matches)
	data := map[string]any{
		"matches": matches,
		"count":   matchCount,
		"records": recordCount,
		"tool":    toolName,
		"offset":  pageStart,
		"limit":   limit,
		"summary": pagedSearchSummary(matchCount, recordCount, pageStart, pageEnd),
	}
	if offset+len(matches) < recordCount {
		data["next_offset"] = offset + len(matches)
	}
	return &Result{Success: true, Data: data}, nil
}

func pagedSearchSummary(matches, records, start, end int) string {
	if end <= start {
		return fmt.Sprintf("Found %d matches in %d records (no records at offset %d)", matches, records, start)
	}
	return fmt.Sprintf("Found %d matches in %d records (showing records %d-%d including context)", matches, records, start+1, end)
}

type cappedSearchBuffer struct {
	remaining int
	buf       bytes.Buffer
}

func newCappedSearchBuffer(limit int) *cappedSearchBuffer {
	return &cappedSearchBuffer{remaining: max(limit, 0)}
}

func (b *cappedSearchBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if b != nil && b.remaining > 0 {
		keep := min(len(p), b.remaining)
		_, _ = b.buf.Write(p[:keep])
		b.remaining -= keep
	}
	return written, nil
}

func (b *cappedSearchBuffer) String() string {
	if b == nil {
		return ""
	}
	return b.buf.String()
}

func parseSearchLine(line string) map[string]any {
	if strings.Contains(line, "\x00") {
		parts := strings.SplitN(line, "\x00", 2)
		if len(parts) != 2 {
			return nil
		}
		rest := parts[1]
		separator := strings.IndexByte(rest, ':')
		kind := "match"
		if contextSeparator := strings.IndexByte(rest, '-'); separator < 0 || (contextSeparator >= 0 && contextSeparator < separator) {
			separator = contextSeparator
			kind = "context"
		}
		if separator <= 0 {
			return nil
		}
		content := rest[separator+1:]
		match := content
		contextText := ""
		if kind == "context" {
			match = ""
			contextText = content
		}
		return map[string]any{
			"path": strings.TrimSpace(parts[0]), "line": parseInt(rest[:separator], 0),
			"column": 0, "match": match, "context": contextText, "kind": kind,
		}
	}
	if strings.Contains(line, "\x1f") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			return nil
		}
		return map[string]any{
			"path":    strings.TrimSpace(parts[0]),
			"line":    parseInt(parts[1], 0),
			"column":  0,
			"match":   "",
			"context": parts[2],
			"kind":    "context",
		}
	}
	if strings.Contains(line, "\x1e") {
		parts := strings.SplitN(line, "\x1e", 4)
		if len(parts) != 4 {
			return nil
		}
		return map[string]any{
			"path":    strings.TrimSpace(parts[0]),
			"line":    parseInt(parts[1], 0),
			"column":  parseInt(parts[2], 0),
			"match":   parts[3],
			"context": "",
			"kind":    "match",
		}
	}
	// grep fallback output remains colon-delimited. Ripgrep always uses the
	// unambiguous record separators above, including for colon-bearing paths.
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
		"kind":    "match",
	}
}

// SearchReplaceTool performs search and replace within a file
type SearchReplaceTool struct{ workDirAware }

func (t *SearchReplaceTool) Name() string {
	return "search_replace"
}

func (t *SearchReplaceTool) Description() string {
	return "Search and replace text in a file with literal or regex patterns. Shows a modification summary, not full content."
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

	info, err := os.Stat(absPath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to stat file: %v", err)}, nil
	}
	if err := os.WriteFile(absPath, []byte(replaced), info.Mode().Perm()); err != nil {
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
