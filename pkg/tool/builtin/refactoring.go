package builtin

import (
	"fmt"
	"maps"
	"os/exec"
	"regexp"
	"strings"
)

// RenameSymbolTool renames a symbol throughout the codebase
type RenameSymbolTool struct{ workDirAware }

// DefaultRenameSearchBytes bounds file discovery before any rename is applied.
const DefaultRenameSearchBytes = 64 << 10

const renamePreviewFiles = 20

func (t *RenameSymbolTool) Name() string {
	return "rename_symbol"
}

func (t *RenameSymbolTool) Description() string {
	return "Rename a symbol throughout the codebase with word-boundary matching and bounded file reads and replacements. Reports modified files, replacement count, and file failures including partial results. Dry runs validate the same budgets without writing. Narrow path/file_pattern if file discovery exceeds its budget."
}

func (t *RenameSymbolTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"old_name": {
				Type:        "string",
				Description: "Current symbol name to rename",
			},
			"new_name": {
				Type:        "string",
				Description: "New symbol name",
			},
			"path": {
				Type:        "string",
				Description: "Optional: directory to search (default: current directory)",
				Default:     ".",
			},
			"file_pattern": {
				Type:        "string",
				Description: "Optional: glob pattern to filter files (e.g., '*.go', '*.js')",
			},
			"dry_run": {
				Type:        "boolean",
				Description: "Preview changes without modifying files (default: false)",
				Default:     false,
			},
		},
		Required: []string{"old_name", "new_name"},
	}
}

func (t *RenameSymbolTool) Execute(params map[string]any) (*Result, error) {
	oldName, ok := params["old_name"].(string)
	if !ok || oldName == "" {
		return &Result{
			Success: false,
			Error:   "old_name parameter must be a non-empty string",
		}, nil
	}

	newName, ok := params["new_name"].(string)
	if !ok || newName == "" {
		return &Result{
			Success: false,
			Error:   "new_name parameter must be a non-empty string",
		}, nil
	}

	if oldName == newName {
		return &Result{
			Success: false,
			Error:   "old_name and new_name must be different",
		}, nil
	}

	searchPath := "."
	if p, ok := params["path"].(string); ok && p != "" {
		searchPath = p
	}
	if strings.TrimSpace(t.workDir) != "" {
		_, rel, err := resolveRelPath(t.workDir, searchPath)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		searchPath = rel
	}

	filePattern := ""
	if fp, ok := params["file_pattern"].(string); ok {
		filePattern = fp
	}

	dryRun := false
	if dr, ok := params["dry_run"].(bool); ok {
		dryRun = dr
	}

	// Find all files containing the old symbol
	files, err := t.findFilesWithSymbol(oldName, searchPath, filePattern)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to find symbol: %v", err),
		}, nil
	}

	if len(files) == 0 {
		return &Result{
			Success: true,
			Data: map[string]any{
				"old_name":           oldName,
				"new_name":           newName,
				"files_modified":     []string{},
				"total_replacements": 0,
				"dry_run":            dryRun,
			},
		}, nil
	}

	// Perform replacements
	totalReplacements := 0
	modifiedFiles := []map[string]any{}
	failedFiles := []map[string]any{}
	failedCount := 0

	for _, file := range files {
		count, err := t.renameInFile(file, oldName, newName, dryRun)
		if err != nil {
			failedCount++
			if len(failedFiles) < renamePreviewFiles {
				failedFiles = append(failedFiles, map[string]any{"file": file, "error": err.Error()})
			}
			continue
		}
		if count > 0 {
			totalReplacements += count
			modifiedFiles = append(modifiedFiles, map[string]any{
				"file":         file,
				"replacements": count,
			})
		}
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"old_name":           oldName,
			"new_name":           newName,
			"files_modified":     modifiedFiles,
			"total_replacements": totalReplacements,
			"dry_run":            dryRun,
		},
	}
	if failedCount > 0 {
		result.Success = false
		result.Error = fmt.Sprintf("failed to rename %d file(s); %d file(s) succeeded; inspect files_failed before retrying", failedCount, len(modifiedFiles))
		if dryRun {
			result.Error = fmt.Sprintf("failed to validate rename for %d file(s); %d file(s) passed validation; inspect files_failed before retrying", failedCount, len(modifiedFiles))
		}
		result.Data["failed_count"] = failedCount
		result.Data["files_failed"] = failedFiles
	}

	// Abridge if many files modified
	if len(modifiedFiles) > renamePreviewFiles {
		result.ShouldAbridge = true
		result.DisplayData = maps.Clone(result.Data)
		result.DisplayData["files_modified"] = modifiedFiles[:renamePreviewFiles]
		result.DisplayData["summary"] = fmt.Sprintf("Renamed '%s' to '%s' in %d files (%d replacements)", oldName, newName, len(modifiedFiles), totalReplacements)
		if dryRun {
			result.DisplayData["summary"] = fmt.Sprintf("Would rename '%s' to '%s' in %d files (%d replacements)", oldName, newName, len(modifiedFiles), totalReplacements)
		}
		if failedCount > 0 {
			result.DisplayData["summary"] = result.Error
		}
	}

	return result, nil
}

func (t *RenameSymbolTool) findFilesWithSymbol(symbol, searchPath, filePattern string) ([]string, error) {
	pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(symbol))

	ctx, cancel := t.execContext()
	defer cancel()

	var cmd *exec.Cmd
	if _, err := exec.LookPath("rg"); err == nil {
		args := []string{"-l", "--no-heading", pattern, searchPath}
		if filePattern != "" {
			args = append(args, "--glob", filePattern)
		}
		cmd = exec.CommandContext(ctx, "rg", args...)
	} else {
		args := []string{"-rl", "-w", symbol, searchPath}
		cmd = exec.CommandContext(ctx, "grep", args...)
	}
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)

	limit := DefaultRenameSearchBytes
	if t.maxOutputBytes > 0 {
		limit = min(limit, t.maxOutputBytes)
	}
	stdout, stderr := newCappedSearchBuffer(limit), newCappedSearchBuffer(limit)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("search timed out")
	}
	if stdout.truncated || stderr.truncated {
		return nil, fmt.Errorf("symbol search output exceeds %d bytes; narrow path or file_pattern", limit)
	}
	if err != nil {
		// Exit code 1 means no matches
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
			return nil, fmt.Errorf("%w: %s", err, diagnostic)
		}
		return nil, err
	}

	files := []string{}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, line := range lines {
		if line != "" {
			files = append(files, line)
		}
	}

	return files, nil
}

func (t *RenameSymbolTool) renameInFile(filepath, oldName, newName string, dryRun bool) (int, error) {
	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, filepath)
		if err != nil {
			return 0, err
		}
		filepath = abs
	}
	content, err := t.readMutationFile(filepath)
	if err != nil {
		return 0, err
	}

	// Use word boundary regex for safe replacement
	pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(oldName))
	re := regexp.MustCompile(pattern)

	newContent, count, err := replaceRegexpBoundedMode(re, content, newName, 0, t.mutationFileLimit(), dryRun)
	if err != nil {
		return 0, err
	}
	if !dryRun && count > 0 {
		if err := writeMutationFile(filepath, newContent, 0644); err != nil {
			return 0, err
		}
	}

	return count, nil
}

// ExtractFunctionTool extracts code into a new function
type ExtractFunctionTool struct{ workDirAware }

func (t *ExtractFunctionTool) Name() string {
	return "extract_function"
}

func (t *ExtractFunctionTool) Description() string {
	return "Extract complete, self-contained statements into a zero-argument helper. Go defaults to a local closure that captures outer locals and validates scope and syntax before writing. Parameters and returns are not inferred; use edit_file for unsupported control flow or declarations needed outside the selection."
}

func (t *ExtractFunctionTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"file": {
				Type:        "string",
				Description: "File containing the code to extract",
			},
			"start_line": {
				Type:        "integer",
				Description: "Starting line number (1-indexed) of code to extract",
			},
			"end_line": {
				Type:        "integer",
				Description: "Ending line number (inclusive) of code to extract",
			},
			"function_name": {
				Type:        "string",
				Description: "Name for the new function",
			},
			"insert_before_line": {
				Type:        "integer",
				Description: "Optional: insertion line at or before start_line. Defaults to start_line; Go uses a local closure. An explicit earlier Go insertion creates a top-level helper and requires self-contained statements.",
			},
		},
		Required: []string{"file", "start_line", "end_line", "function_name"},
	}
}

func (t *ExtractFunctionTool) Execute(params map[string]any) (*Result, error) {
	filepath, ok := params["file"].(string)
	if !ok || filepath == "" {
		return &Result{
			Success: false,
			Error:   "file parameter must be a non-empty string",
		}, nil
	}

	startLine, err := readFileLineNumber("start_line", params["start_line"])
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	endLine, err := readFileLineNumber("end_line", params["end_line"])
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	if endLine < startLine {
		return &Result{Error: "end_line must be >= start_line"}, nil
	}
	insertLine := startLine
	if value, present := params["insert_before_line"]; present {
		insertLine, err = readFileLineNumber("insert_before_line", value)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		if insertLine > startLine {
			return &Result{Error: "insert_before_line must be at or before start_line"}, nil
		}
	}

	functionName, ok := params["function_name"].(string)
	if !ok || functionName == "" {
		return &Result{
			Success: false,
			Error:   "function_name parameter must be a non-empty string",
		}, nil
	}

	if int64(len(functionName)) > t.mutationFileLimit() {
		return &Result{Error: mutationBudgetError(t.mutationFileLimit()).Error()}, nil
	}

	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, filepath)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		filepath = abs
	}

	content, err := t.readMutationFile(filepath)
	if err != nil {
		return &Result{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}
	totalLines := strings.Count(content, "\n") + 1
	if startLine > totalLines || endLine > totalLines {
		return &Result{Error: fmt.Sprintf("line numbers out of range (file has %d lines)", totalLines)}, nil
	}
	start := mutationLineOffset(content, startLine)
	end := len(content)
	if endLine < totalLines {
		end = mutationLineOffset(content, endLine+1)
	}
	insert := mutationLineOffset(content, insertLine)
	extractedCode := strings.TrimSuffix(content[start:end], "\n")
	if strings.TrimSpace(extractedCode) == "" {
		return &Result{Error: "select at least one complete statement"}, nil
	}
	firstLine := ""
	for line := range strings.SplitSeq(extractedCode, "\n") {
		if strings.TrimSpace(line) != "" {
			firstLine = line
			break
		}
	}
	indentation := t.detectIndentation(firstLine)
	var newFunction, functionCall string
	if strings.HasSuffix(filepath, ".go") {
		newFunction, functionCall, err = prepareGoExtraction(filepath, content, extractedCode, functionName, start, end, insert)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
	} else if strings.HasSuffix(filepath, ".js") || strings.HasSuffix(filepath, ".ts") {
		newFunction, functionCall = t.generateJSFunction(functionName, extractedCode)
	} else if strings.HasSuffix(filepath, ".py") {
		newFunction, functionCall, err = t.generatePythonFunction(functionName, extractedCode, indentation, insert, start, content)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
	} else {
		newFunction = fmt.Sprintf("function %s() {\n%s\n}", functionName, extractedCode)
		functionCall = fmt.Sprintf("%s()", functionName)
	}
	call := indentation + functionCall
	if strings.HasSuffix(content[start:end], "\n") {
		call += "\n"
	}
	if err := t.checkReplacementGrowth(len(content), end-start, len(newFunction)+2+len(call), 1); err != nil {
		return &Result{Error: err.Error()}, nil
	}
	var output strings.Builder
	output.Grow(len(content) - (end - start) + len(newFunction) + 2 + len(call))
	output.WriteString(content[:insert])
	output.WriteString(newFunction)
	output.WriteString("\n\n")
	output.WriteString(content[insert:start])
	output.WriteString(call)
	output.WriteString(content[end:])
	newContent := output.String()

	// Write the modified file
	if err := writeMutationFile(filepath, newContent, 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"file":            filepath,
			"function_name":   functionName,
			"extracted_lines": fmt.Sprintf("%d-%d", startLine, endLine),
			"new_function":    newFunction,
			"function_call":   functionCall,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"file":          filepath,
		"function_name": functionName,
		"summary":       fmt.Sprintf("✓ Extracted lines %d-%d into function '%s'", startLine, endLine, functionName),
	}

	return result, nil
}

func (t *ExtractFunctionTool) generateJSFunction(name, code string) (string, string) {
	return fmt.Sprintf("function %s() {\n%s\n}", name, code),
		fmt.Sprintf("%s()", name)
}

func (t *ExtractFunctionTool) generatePythonFunction(name, code, sourceIndent string, insert, start int, content string) (string, string, error) {
	targetIndent := sourceIndent
	if insert != start {
		line, _, _ := strings.Cut(content[insert:], "\n")
		targetIndent = t.detectIndentation(line)
	}
	unit := "    "
	if strings.Contains(sourceIndent, "\t") {
		unit = "\t"
	}
	size := int64(len(targetIndent) + len(name) + 8)
	limit := t.mutationFileLimit()
	for line := range strings.SplitSeq(code, "\n") {
		additional := int64(len(targetIndent) + len(unit) + len(strings.TrimPrefix(line, sourceIndent)) + 1)
		if additional > limit-size {
			return "", "", mutationBudgetError(limit)
		}
		size += additional
	}
	var body strings.Builder
	body.Grow(int(size))
	body.WriteString(targetIndent)
	body.WriteString("def ")
	body.WriteString(name)
	body.WriteString("():\n")
	for line := range strings.SplitSeq(code, "\n") {
		body.WriteString(targetIndent)
		body.WriteString(unit)
		body.WriteString(strings.TrimPrefix(line, sourceIndent))
		body.WriteByte('\n')
	}
	return strings.TrimSuffix(body.String(), "\n"), name + "()", nil
}

func (t *ExtractFunctionTool) detectIndentation(line string) string {
	for i, ch := range line {
		if ch != ' ' && ch != '\t' {
			return line[:i]
		}
	}
	return ""
}
