package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadFileTool reads a file from disk
type ReadFileTool struct{ workDirAware }

func (t *ReadFileTool) Name() string {
	return "read_file"
}

func (t *ReadFileTool) Description() string {
	return "Read file contents in bounded, 1-indexed line pages. Pages default to 100 lines; explicit ranges are capped at 500 lines and return next_start_line when more remains."
}

func (t *ReadFileTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to read",
			},
			"start_line": {
				Type:        "number",
				Description: "First line to return (1-indexed, defaults to 1)",
			},
			"end_line": {
				Type:        "number",
				Description: "Last line to return (1-indexed, inclusive; defaults to start_line + 99; explicit ranges are capped at 500 lines)",
			},
		},
		Required: []string{"path"},
	}
}

func (t *ReadFileTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "path parameter must be a string",
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	if t.maxFileSizeBytes > 0 {
		if info, err := os.Stat(absPath); err == nil && info.Size() > t.maxFileSizeBytes {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("file too large: %d bytes (max %d)", info.Size(), t.maxFileSizeBytes),
			}, nil
		}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	contentStr := string(content)
	lines := fileLines(contentStr)
	startLine, endLine, explicitPage, err := readFilePage(params, len(lines))
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	pageLines := lines[startLine-1 : endLine]
	pageContent := strings.Join(pageLines, "\n")
	hasMore := endLine < len(lines)
	page := map[string]any{
		"start_line":  startLine,
		"end_line":    endLine,
		"total_lines": len(lines),
		"has_more":    hasMore,
	}
	if hasMore {
		page["next_start_line"] = endLine + 1
	}
	shouldAbridge := explicitPage || hasMore

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":    absPath,
			"content": contentStr, // Full content remains available to non-model callers.
			"size":    len(content),
			"page":    page,
		},
		ShouldAbridge: shouldAbridge,
	}

	if shouldAbridge {
		preview := fmt.Sprintf("Read %s (lines %d-%d of %d, %d bytes)", filepath.Base(absPath), startLine, endLine, len(lines), len(content))
		if hasMore {
			preview += fmt.Sprintf("; continue with start_line=%d", endLine+1)
		}
		result.DisplayData = map[string]any{
			"path":    absPath,
			"content": pageContent,
			"size":    len(content),
			"page":    page,
			"preview": preview,
		}
	}

	return result, nil
}

const (
	readFileDefaultPageLines = 100
	readFileMaxPageLines     = 500
)

func fileLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func readFilePage(params map[string]any, totalLines int) (startLine, endLine int, explicitPage bool, err error) {
	startLine = 1
	if value, ok := params["start_line"]; ok {
		explicitPage = true
		startLine, err = readFileLineNumber("start_line", value)
		if err != nil {
			return 0, 0, false, err
		}
	}

	endLine = startLine + readFileDefaultPageLines - 1
	if value, ok := params["end_line"]; ok {
		explicitPage = true
		endLine, err = readFileLineNumber("end_line", value)
		if err != nil {
			return 0, 0, false, err
		}
	}
	if endLine < startLine {
		return 0, 0, false, fmt.Errorf("end_line must be greater than or equal to start_line")
	}
	if endLine-startLine+1 > readFileMaxPageLines {
		endLine = startLine + readFileMaxPageLines - 1
	}
	if totalLines == 0 {
		if startLine != 1 {
			return 0, 0, false, fmt.Errorf("start_line %d exceeds empty file", startLine)
		}
		return 1, 0, explicitPage, nil
	}
	if startLine > totalLines {
		return 0, 0, false, fmt.Errorf("start_line %d exceeds file length of %d lines", startLine, totalLines)
	}
	if endLine > totalLines {
		endLine = totalLines
	}
	return startLine, endLine, explicitPage, nil
}

func readFileLineNumber(name string, value any) (int, error) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s parameter must be a positive integer", name)
		}
		number = float64(parsed)
	default:
		return 0, fmt.Errorf("%s parameter must be a positive integer", name)
	}
	if number < 1 || math.Trunc(number) != number || number > math.MaxInt {
		return 0, fmt.Errorf("%s parameter must be a positive integer", name)
	}
	return int(number), nil
}

// WriteFileTool writes content to a file
type WriteFileTool struct{ workDirAware }

func (t *WriteFileTool) Name() string {
	return "write_file"
}

func (t *WriteFileTool) Description() string {
	return "Write or create a file with the given content. Creates parent directories. Shows a compact summary, not the content."
}

func (t *WriteFileTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to write",
			},
			"content": {
				Type:        "string",
				Description: "Content to write to the file",
			},
		},
		Required: []string{"path", "content"},
	}
}

func (t *WriteFileTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "path parameter must be a string",
		}, nil
	}

	content, ok := params["content"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "content parameter must be a string",
		}, nil
	}

	if t.maxFileSizeBytes > 0 && int64(len(content)) > t.maxFileSizeBytes {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("content too large: %d bytes (max %d)", len(content), t.maxFileSizeBytes),
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	// Create parent directory if it doesn't exist
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to create directory: %v", err),
		}, nil
	}

	// Check existence independently of readability so an existing file that
	// is writable but not readable is still reported as modified. Treat any
	// stat error other than not-exist as existing/unknown; do not silently
	// label an uncertain target as newly created.
	fileExists := true
	if _, statErr := os.Stat(absPath); statErr != nil && os.IsNotExist(statErr) {
		fileExists = false
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	lines := strings.Split(content, "\n")
	isNew := !fileExists

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":    absPath,
			"size":    len(content),
			"content": content, // Full content
		},
		ShouldAbridge: true,
	}

	// Show compact summary in conversation
	summary := fmt.Sprintf("✓ Wrote %s (%d lines, %d bytes)", filepath.Base(absPath), len(lines), len(content))
	if isNew {
		summary = fmt.Sprintf("✓ Created %s (%d lines, %d bytes)", filepath.Base(absPath), len(lines), len(content))
	}

	result.DisplayData = map[string]any{
		"path":    absPath,
		"size":    len(content),
		"summary": summary,
		"lines":   len(lines),
		"is_new":  isNew,
	}

	attachPostEditDiagnostics(result, absPath)
	return result, nil
}

// ListDirectoryTool lists files in a directory
type ListDirectoryTool struct{ workDirAware }

func (t *ListDirectoryTool) Name() string {
	return "list_directory"
}

func (t *ListDirectoryTool) Description() string {
	return "List files and directories at a path. Returns name, type, and size for each entry."
}

func (t *ListDirectoryTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the directory to list",
				Default:     ".",
			},
		},
		Required: []string{},
	}
}

func (t *ListDirectoryTool) Execute(params map[string]any) (*Result, error) {
	path := "."
	if p, ok := params["path"].(string); ok {
		path = p
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read directory: %v", err),
		}, nil
	}

	files := []map[string]any{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, map[string]any{
			"name":   entry.Name(),
			"is_dir": entry.IsDir(),
			"size":   info.Size(),
		})
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"path":  absPath,
			"files": files,
			"count": len(files),
		},
	}, nil
}

// PatchFileTool applies a unified diff patch to the repository
type PatchFileTool struct{ workDirAware }

func (t *PatchFileTool) Name() string {
	return "apply_patch"
}

func (t *PatchFileTool) Description() string {
	return "Apply a unified diff patch to modify files, with configurable path stripping (-pN)."
}

func (t *PatchFileTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"patch": {
				Type:        "string",
				Description: "Unified diff/patch content to apply",
			},
			"strip": {
				Type:        "integer",
				Description: "Number of leading path components to strip when applying (patch -pN). Defaults to 0.",
				Default:     0,
			},
		},
		Required: []string{"patch"},
	}
}

func (t *PatchFileTool) Execute(params map[string]any) (*Result, error) {
	rawPatch, ok := params["patch"].(string)
	if !ok || strings.TrimSpace(rawPatch) == "" {
		return &Result{
			Success: false,
			Error:   "patch parameter must be a non-empty string",
		}, nil
	}

	const maxPatchBytes = 10 * 1024 * 1024 // 10MB
	if len(rawPatch) > maxPatchBytes {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("patch too large: %d bytes (max %d)", len(rawPatch), maxPatchBytes),
		}, nil
	}

	strip := 0
	if v, exists := params["strip"]; exists {
		var parsedStrip int
		var err error
		switch value := v.(type) {
		case float64:
			parsedStrip = int(value)
		case int:
			parsedStrip = value
		case string:
			if strings.TrimSpace(value) == "" {
				parsedStrip = 0
			} else {
				parsedStrip, err = strconv.Atoi(value)
				if err != nil {
					return &Result{
						Success: false,
						Error:   fmt.Sprintf("strip parameter must be an integer: %v", err),
					}, nil
				}
			}
		default:
			return &Result{
				Success: false,
				Error:   "strip parameter must be an integer",
			}, nil
		}

		if parsedStrip < 0 {
			return &Result{
				Success: false,
				Error:   "strip parameter cannot be negative",
			}, nil
		}
		strip = parsedStrip
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "patch", fmt.Sprintf("-p%d", strip), "-N", "-s")
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	cmd.Stdin = strings.NewReader(rawPatch)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "patch command timed out",
		}, nil
	}
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("patch command failed: %v\n%s", err, strings.TrimSpace(string(output))),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"strip":   strip,
			"message": strings.TrimSpace(string(output)),
		},
	}, nil
}

// FindFilesTool finds files matching a pattern
type FindFilesTool struct{ workDirAware }

func (t *FindFilesTool) Name() string {
	return "find_files"
}

func (t *FindFilesTool) Description() string {
	return "Find files recursively with repository-relative globs and bounded pages, skipping dependency and build-output directories. Use next_offset until absent to exhaust a large result set."
}

func (t *FindFilesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"pattern": {
				Type:        "string",
				Description: "Glob pattern to match files (e.g., '*.go', 'src/**/*.ts')",
			},
			"base_path": {
				Type:        "string",
				Description: "Base directory to search from (default: current directory)",
				Default:     ".",
			},
			"offset": {
				Type:        "integer",
				Description: "Zero-based match offset for paging large result sets (default 0)",
				Default:     0,
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum matches returned to the model per page (default 200, max 1000)",
				Default:     200,
			},
		},
		Required: []string{"pattern"},
	}
}

func (t *FindFilesTool) Execute(params map[string]any) (*Result, error) {
	pattern, ok := params["pattern"].(string)
	if !ok || pattern == "" {
		return &Result{
			Success: false,
			Error:   "pattern parameter must be a non-empty string",
		}, nil
	}

	basePath := "."
	if bp, ok := params["base_path"].(string); ok && bp != "" {
		basePath = bp
	}
	offset := parseInt(params["offset"], 0)
	limit := parseInt(params["limit"], 200)
	if offset < 0 {
		return &Result{Success: false, Error: "offset must not be negative"}, nil
	}
	if limit <= 0 || limit > 1000 {
		return &Result{Success: false, Error: "limit must be between 1 and 1000"}, nil
	}

	absBasePath, err := resolvePath(t.workDir, basePath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}
	baseInfo, err := os.Stat(absBasePath)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to inspect base path: %v", err)}, nil
	}
	if !baseInfo.IsDir() {
		return &Result{Success: false, Error: fmt.Sprintf("base path is not a directory: %s", basePath)}, nil
	}
	expandedPatterns, err := expandFindFilesPatterns(pattern)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("invalid glob pattern: %v", err)}, nil
	}
	patterns := compileFindFilesPatterns(expandedPatterns)

	matches := make([]string, 0, limit)
	matchCount := 0
	err = filepath.WalkDir(absBasePath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipFindFilesDir(path, absBasePath) {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(absBasePath, path)
		if err != nil {
			return nil
		}

		relPath = filepath.ToSlash(relPath)
		if matchesFindFilesPattern(patterns, relPath) {
			// WalkDir visits entries in lexical order, so retain only this page
			// instead of materializing and sorting the repository-wide corpus.
			if matchCount >= offset && len(matches) < limit {
				matches = append(matches, relPath)
			}
			matchCount++
		}

		return nil
	})

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to search files: %v", err),
		}, nil
	}
	pageStart := offset
	pageEnd := pageStart + len(matches)
	data := map[string]any{
		"pattern": pattern,
		"matches": matches,
		"count":   matchCount,
		"offset":  pageStart,
		"limit":   limit,
		"summary": pagedFindFilesSummary(pattern, matchCount, pageStart, pageEnd),
	}
	if offset+len(matches) < matchCount {
		data["next_offset"] = offset + len(matches)
	}
	return &Result{Success: true, Data: data}, nil
}

func pagedFindFilesSummary(pattern string, count, start, end int) string {
	if end <= start {
		return fmt.Sprintf("Found %d files matching %q (no records at offset %d)", count, pattern, start)
	}
	return fmt.Sprintf("Found %d files matching %q (showing %d-%d)", count, pattern, start+1, end)
}

const maxFindFilesPatternExpansions = 64

// expandFindFilesPatterns adds the small brace-alternation surface models
// commonly use for repository inventories (for example **/*.{go,rs,ts}).
// Expansion is bounded so a tool argument cannot create unbounded matching
// work. Each expanded pattern is validated before the filesystem walk.
func expandFindFilesPatterns(pattern string) ([]string, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" {
		return nil, fmt.Errorf("pattern is empty")
	}

	patterns := []string{pattern}
	for {
		expanded := false
		next := make([]string, 0, len(patterns))
		for _, candidate := range patterns {
			open := strings.IndexByte(candidate, '{')
			if open < 0 {
				next = append(next, candidate)
				continue
			}
			closeOffset := strings.IndexByte(candidate[open+1:], '}')
			if closeOffset < 0 {
				return nil, fmt.Errorf("unclosed brace")
			}
			closeIndex := open + 1 + closeOffset
			choices := strings.Split(candidate[open+1:closeIndex], ",")
			if len(choices) < 2 {
				return nil, fmt.Errorf("brace expression must contain alternatives")
			}
			for _, choice := range choices {
				choice = strings.TrimSpace(choice)
				if choice == "" {
					return nil, fmt.Errorf("brace expression contains an empty alternative")
				}
				next = append(next, candidate[:open]+choice+candidate[closeIndex+1:])
				if len(next) > maxFindFilesPatternExpansions {
					return nil, fmt.Errorf("brace expansion exceeds %d patterns", maxFindFilesPatternExpansions)
				}
			}
			expanded = true
		}
		patterns = next
		if !expanded {
			break
		}
	}

	for _, candidate := range patterns {
		segments := strings.Split(candidate, "/")
		for _, segment := range segments {
			if segment == "**" {
				continue
			}
			if _, err := pathpkg.Match(segment, "probe"); err != nil {
				return nil, err
			}
		}
	}
	return patterns, nil
}

type findFilesPattern struct {
	basename string
	segments []string
}

func compileFindFilesPatterns(patterns []string) []findFilesPattern {
	compiled := make([]findFilesPattern, 0, len(patterns))
	for _, pattern := range patterns {
		if !strings.Contains(pattern, "/") {
			compiled = append(compiled, findFilesPattern{basename: pattern})
			continue
		}
		compiled = append(compiled, findFilesPattern{segments: strings.Split(pattern, "/")})
	}
	return compiled
}

func matchesFindFilesPattern(patterns []findFilesPattern, relPath string) bool {
	relPath = filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	base := pathpkg.Base(relPath)
	var pathSegments []string
	for _, pattern := range patterns {
		if pattern.basename != "" {
			matched, _ := pathpkg.Match(pattern.basename, base)
			if matched {
				return true
			}
			continue
		}
		if pathSegments == nil {
			pathSegments = strings.Split(relPath, "/")
		}
		if matchFindFilesSegments(pattern.segments, pathSegments) {
			return true
		}
	}
	return false
}

func matchFindFilesSegments(pattern, name []string) bool {
	doubleStars := 0
	for _, segment := range pattern {
		if segment == "**" {
			doubleStars++
		}
	}
	if doubleStars <= 1 {
		return matchFindFilesSegmentsLinear(pattern, name)
	}

	type state struct{ pattern, name int }
	stateCapacity := (len(pattern) + 1) * (len(name) + 1)
	memo := make(map[state]bool, stateCapacity)
	seen := make(map[state]bool, stateCapacity)
	var match func(int, int) bool
	match = func(patternIndex, nameIndex int) bool {
		key := state{pattern: patternIndex, name: nameIndex}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true

		matched := false
		switch {
		case patternIndex == len(pattern):
			matched = nameIndex == len(name)
		case pattern[patternIndex] == "**":
			matched = match(patternIndex+1, nameIndex) ||
				(nameIndex < len(name) && match(patternIndex, nameIndex+1))
		case nameIndex < len(name):
			segmentMatched, err := pathpkg.Match(pattern[patternIndex], name[nameIndex])
			matched = err == nil && segmentMatched && match(patternIndex+1, nameIndex+1)
		}
		memo[key] = matched
		return matched
	}
	return match(0, 0)
}

// A single ** has no overlapping wildcard states, which makes this common
// repository pattern linear without allocating a memo table for every file.
func matchFindFilesSegmentsLinear(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for offset := 0; offset <= len(name); offset++ {
			if matchFindFilesSegmentsLinear(pattern[1:], name[offset:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	matched, err := pathpkg.Match(pattern[0], name[0])
	return err == nil && matched && matchFindFilesSegmentsLinear(pattern[1:], name[1:])
}

func shouldSkipFindFilesDir(path, base string) bool {
	name := filepath.Base(path)
	if path == base {
		return false
	}
	switch name {
	case ".git", ".hg", ".svn",
		"node_modules", "bower_components",
		"target", ".next", ".nuxt", ".turbo", ".cache",
		"dist", "coverage",
		".venv", "venv", "__pycache__":
		return true
	default:
		return false
	}
}

// FileExistsTool checks if a file exists
type FileExistsTool struct{ workDirAware }

func (t *FileExistsTool) Name() string {
	return "file_exists"
}

func (t *FileExistsTool) Description() string {
	return "Check if a file or directory exists and get its metadata: name, size, type, permissions, mtime."
}

func (t *FileExistsTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to check for existence",
			},
		},
		Required: []string{"path"},
	}
}

func (t *FileExistsTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}

	absPath, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	info, err := os.Stat(absPath)
	exists := err == nil

	result := map[string]any{
		"path":   absPath,
		"exists": exists,
	}

	if exists {
		result["is_dir"] = info.IsDir()
		result["size"] = info.Size()
		result["mode"] = info.Mode().String()
		result["modified"] = info.ModTime().Format("2006-01-02 15:04:05")
		result["name"] = info.Name()
	}

	return &Result{
		Success: true,
		Data:    result,
	}, nil
}
