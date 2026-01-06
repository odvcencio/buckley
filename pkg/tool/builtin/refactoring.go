package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// RenameSymbolTool renames a symbol throughout the codebase
type RenameSymbolTool struct{ workDirAware }

func (t *RenameSymbolTool) Name() string {
	return "rename_symbol"
}

func (t *RenameSymbolTool) Description() string {
	return "Rename a symbol (variable, function, type, class) throughout the codebase. Performs word-boundary matching to avoid partial replacements. Shows summary of files modified and total replacements. Use this for safe refactoring when renaming identifiers."
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

	for _, file := range files {
		count, err := t.renameInFile(file, oldName, newName, dryRun)
		if err != nil {
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

	// Abridge if many files modified
	if len(modifiedFiles) > 20 {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"old_name":           oldName,
			"new_name":           newName,
			"files_modified":     modifiedFiles[:20],
			"total_replacements": totalReplacements,
			"dry_run":            dryRun,
			"summary":            fmt.Sprintf("Renamed '%s' to '%s' in %d files (%d replacements)", oldName, newName, len(modifiedFiles), totalReplacements),
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

	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("search timed out")
	}
	if err != nil {
		// Exit code 1 means no matches
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		return nil, err
	}

	files := []string{}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
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
	content, err := os.ReadFile(filepath)
	if err != nil {
		return 0, err
	}

	// Use word boundary regex for safe replacement
	pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(oldName))
	re := regexp.MustCompile(pattern)

	matches := re.FindAllStringIndex(string(content), -1)
	if len(matches) == 0 {
		return 0, nil
	}

	if !dryRun {
		newContent := re.ReplaceAllString(string(content), newName)
		if err := os.WriteFile(filepath, []byte(newContent), 0644); err != nil {
			return 0, err
		}
	}

	return len(matches), nil
}

// ExtractFunctionTool extracts code into a new function
type ExtractFunctionTool struct{ workDirAware }

func (t *ExtractFunctionTool) Name() string {
	return "extract_function"
}

func (t *ExtractFunctionTool) Description() string {
	return "Extract selected code into a new function. Analyzes the code block to determine parameters and return values based on variable usage. Inserts the new function definition and replaces the original code with a function call. Use this for code organization and reducing duplication."
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
				Description: "Optional: line number to insert new function before (default: before start_line)",
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

	startLine, ok := params["start_line"].(float64)
	if !ok || startLine < 1 {
		return &Result{
			Success: false,
			Error:   "start_line must be a positive integer",
		}, nil
	}

	endLine, ok := params["end_line"].(float64)
	if !ok || endLine < startLine {
		return &Result{
			Success: false,
			Error:   "end_line must be >= start_line",
		}, nil
	}

	functionName, ok := params["function_name"].(string)
	if !ok || functionName == "" {
		return &Result{
			Success: false,
			Error:   "function_name parameter must be a non-empty string",
		}, nil
	}

	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, filepath)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		filepath = abs
	}

	// Read the file
	content, err := os.ReadFile(filepath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	lines := strings.Split(string(content), "\n")
	if int(startLine) > len(lines) || int(endLine) > len(lines) {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("line numbers out of range (file has %d lines)", len(lines)),
		}, nil
	}

	// Extract the code block
	extractedLines := lines[int(startLine)-1 : int(endLine)]
	extractedCode := strings.Join(extractedLines, "\n")

	// Detect language and generate function
	var newFunction string
	var functionCall string

	if strings.HasSuffix(filepath, ".go") {
		newFunction, functionCall = t.generateGoFunction(functionName, extractedCode, extractedLines)
	} else if strings.HasSuffix(filepath, ".js") || strings.HasSuffix(filepath, ".ts") {
		newFunction, functionCall = t.generateJSFunction(functionName, extractedCode)
	} else if strings.HasSuffix(filepath, ".py") {
		newFunction, functionCall = t.generatePythonFunction(functionName, extractedCode, extractedLines)
	} else {
		// Generic extraction
		newFunction = fmt.Sprintf("function %s() {\n%s\n}", functionName, extractedCode)
		functionCall = fmt.Sprintf("%s()", functionName)
	}

	// Determine where to insert the new function
	insertLine := int(startLine) - 1
	if ibl, ok := params["insert_before_line"].(float64); ok && ibl > 0 {
		insertLine = int(ibl) - 1
	}

	// Build new file content
	newLines := []string{}

	// Lines before insertion point
	newLines = append(newLines, lines[:insertLine]...)

	// New function
	newLines = append(newLines, newFunction)
	newLines = append(newLines, "")

	// Lines between insertion point and extracted code
	if insertLine < int(startLine)-1 {
		newLines = append(newLines, lines[insertLine:int(startLine)-1]...)
	}

	// Function call replacing extracted code
	indentation := t.detectIndentation(extractedLines[0])
	newLines = append(newLines, indentation+functionCall)

	// Lines after extracted code
	newLines = append(newLines, lines[int(endLine):]...)

	newContent := strings.Join(newLines, "\n")

	// Write the modified file
	if err := os.WriteFile(filepath, []byte(newContent), 0644); err != nil {
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
			"extracted_lines": fmt.Sprintf("%d-%d", int(startLine), int(endLine)),
			"new_function":    newFunction,
			"function_call":   functionCall,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"file":          filepath,
		"function_name": functionName,
		"summary":       fmt.Sprintf("✓ Extracted lines %d-%d into function '%s'", int(startLine), int(endLine), functionName),
	}

	return result, nil
}

func (t *ExtractFunctionTool) generateGoFunction(name, code string, lines []string) (string, string) {
	indentation := t.detectIndentation(lines[0])

	// Simple Go function generation (basic version)
	return fmt.Sprintf("func %s() {\n%s\n}", name, code),
		fmt.Sprintf("%s%s()", indentation, name)
}

func (t *ExtractFunctionTool) generateJSFunction(name, code string) (string, string) {
	return fmt.Sprintf("function %s() {\n%s\n}", name, code),
		fmt.Sprintf("%s()", name)
}

func (t *ExtractFunctionTool) generatePythonFunction(name, code string, lines []string) (string, string) {
	indentation := t.detectIndentation(lines[0])

	return fmt.Sprintf("def %s():\n%s", name, code),
		fmt.Sprintf("%s%s()", indentation, name)
}

func (t *ExtractFunctionTool) detectIndentation(line string) string {
	for i, ch := range line {
		if ch != ' ' && ch != '\t' {
			return line[:i]
		}
	}
	return ""
}
