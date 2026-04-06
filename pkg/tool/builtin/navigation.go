package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

// FindSymbolTool finds where symbols are defined
type FindSymbolTool struct {
	workDirAware
	Store *storage.Store
}

func (t *FindSymbolTool) Name() string {
	return "find_symbol"
}

func (t *FindSymbolTool) Description() string {
	return "Find where a symbol (function, class, type, interface) is defined in the codebase. Searches for Go functions, types, interfaces, JavaScript/TypeScript functions/classes, and Python functions/classes. Returns file path and line number of definitions."
}

func (t *FindSymbolTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"symbol": {
				Type:        "string",
				Description: "Symbol name to find (e.g., 'executeTask', 'Validator', 'NewExecutor')",
			},
			"type": {
				Type:        "string",
				Description: "Optional: symbol type filter ('function', 'type', 'interface', 'class')",
			},
			"path": {
				Type:        "string",
				Description: "Optional: directory to search (default: current directory)",
				Default:     ".",
			},
		},
		Required: []string{"symbol"},
	}
}

func (t *FindSymbolTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *FindSymbolTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	symbol, ok := params["symbol"].(string)
	if !ok || symbol == "" {
		return &Result{
			Success: false,
			Error:   "symbol parameter must be a non-empty string",
		}, nil
	}

	symbolType := ""
	if st, ok := params["type"].(string); ok {
		symbolType = st
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

	if t.Store != nil {
		if data := t.queryIndex(ctx, symbol, searchPath); data != nil {
			return &Result{
				Success: true,
				Data:    data,
				DisplayData: map[string]any{
					"summary": fmt.Sprintf("Found %d indexed definition(s) for '%s'", data["count"], symbol),
				},
			}, nil
		}
	}

	// Build search patterns based on language
	patterns := t.buildSearchPatterns(symbol, symbolType)

	matches := []map[string]any{}
	seen := make(map[string]bool) // Deduplicate

	for _, pattern := range patterns {
		results, err := t.searchForPattern(ctx, pattern, searchPath)
		if err != nil {
			continue
		}

		for _, result := range results {
			key := fmt.Sprintf("%s:%d", result["file"], result["line"])
			if !seen[key] {
				matches = append(matches, result)
				seen[key] = true
			}
		}
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"symbol":  symbol,
			"matches": matches,
			"count":   len(matches),
		},
	}

	// Abridge if many matches
	if len(matches) > 20 {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"symbol":  symbol,
			"matches": matches[:20],
			"count":   len(matches),
			"summary": fmt.Sprintf("Found %d definitions of '%s' (showing first 20)", len(matches), symbol),
		}
	}

	return result, nil
}

func (t *FindSymbolTool) buildSearchPatterns(symbol, symbolType string) []string {
	patterns := []string{}

	switch symbolType {
	case "function":
		// Go functions
		patterns = append(patterns, fmt.Sprintf(`func\s+%s\s*\(`, symbol))
		patterns = append(patterns, fmt.Sprintf(`func\s+\(\w+\s+\*?\w+\)\s+%s\s*\(`, symbol))
		// JavaScript/TypeScript functions
		patterns = append(patterns, fmt.Sprintf(`function\s+%s\s*\(`, symbol))
		patterns = append(patterns, fmt.Sprintf(`const\s+%s\s*=\s*\(`, symbol))
		patterns = append(patterns, fmt.Sprintf(`%s\s*:\s*function\s*\(`, symbol))
		// Python functions
		patterns = append(patterns, fmt.Sprintf(`def\s+%s\s*\(`, symbol))

	case "type", "class":
		// Go types
		patterns = append(patterns, fmt.Sprintf(`type\s+%s\s+(struct|interface)`, symbol))
		// JavaScript/TypeScript classes
		patterns = append(patterns, fmt.Sprintf(`class\s+%s`, symbol))
		// Python classes
		patterns = append(patterns, fmt.Sprintf(`class\s+%s\s*(\(|:)`, symbol))

	case "interface":
		// Go interfaces
		patterns = append(patterns, fmt.Sprintf(`type\s+%s\s+interface`, symbol))
		// TypeScript interfaces
		patterns = append(patterns, fmt.Sprintf(`interface\s+%s`, symbol))

	default:
		// Search for all types
		// Go
		patterns = append(patterns, fmt.Sprintf(`func\s+%s\s*\(`, symbol))
		patterns = append(patterns, fmt.Sprintf(`func\s+\(\w+\s+\*?\w+\)\s+%s\s*\(`, symbol))
		patterns = append(patterns, fmt.Sprintf(`type\s+%s\s+`, symbol))
		// JavaScript/TypeScript
		patterns = append(patterns, fmt.Sprintf(`function\s+%s\s*\(`, symbol))
		patterns = append(patterns, fmt.Sprintf(`const\s+%s\s*=`, symbol))
		patterns = append(patterns, fmt.Sprintf(`class\s+%s`, symbol))
		patterns = append(patterns, fmt.Sprintf(`interface\s+%s`, symbol))
		// Python
		patterns = append(patterns, fmt.Sprintf(`def\s+%s\s*\(`, symbol))
		patterns = append(patterns, fmt.Sprintf(`class\s+%s\s*(\(|:)`, symbol))
	}

	return patterns
}

func (t *FindSymbolTool) searchForPattern(ctx context.Context, pattern, searchPath string) ([]map[string]any, error) {
	// Use ripgrep if available, otherwise grep
	ctx, cancel := t.execContextWithParent(ctx)
	defer cancel()

	var cmd *exec.Cmd
	if _, err := exec.LookPath("rg"); err == nil {
		cmd = exec.CommandContext(ctx, "rg", "-n", "--no-heading", pattern, searchPath)
	} else {
		cmd = exec.CommandContext(ctx, "grep", "-rn", "-E", pattern, searchPath)
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
		// Exit code 1 means no matches, which is not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []map[string]any{}, nil
		}
		return nil, err
	}

	results := []map[string]any{}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}

		lineNum := 0
		fmt.Sscanf(parts[1], "%d", &lineNum)

		results = append(results, map[string]any{
			"file":    parts[0],
			"line":    lineNum,
			"content": strings.TrimSpace(parts[2]),
		})
	}

	return results, nil
}

func (t *FindSymbolTool) queryIndex(ctx context.Context, symbol, searchPath string) map[string]any {
	if symbol == "" || t.Store == nil {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pathGlob := ""
	if searchPath != "" && searchPath != "." {
		trimmed := filepath.ToSlash(strings.TrimSuffix(searchPath, "/"))
		if trimmed != "" {
			pathGlob = trimmed + "/*"
		}
	}

	records, err := t.Store.SearchSymbols(ctx, symbol, pathGlob, 25)
	if err != nil || len(records) == 0 {
		return nil
	}

	matches := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		matches = append(matches, map[string]any{
			"file":      rec.FilePath,
			"line":      rec.StartLine,
			"kind":      rec.Kind,
			"signature": rec.Signature,
		})
	}

	return map[string]any{
		"symbol":  symbol,
		"matches": matches,
		"count":   len(matches),
		"source":  "index",
	}
}

// FindReferencesTool finds where symbols are used
type FindReferencesTool struct{ workDirAware }

func (t *FindReferencesTool) Name() string {
	return "find_references"
}

func (t *FindReferencesTool) Description() string {
	return "Find all places where a symbol is referenced or called in the codebase. Searches for function calls, type usage, variable references, etc. Returns file paths and line numbers of all usages. Results limited to first 50 in conversation."
}

func (t *FindReferencesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"symbol": {
				Type:        "string",
				Description: "Symbol name to find references for",
			},
			"path": {
				Type:        "string",
				Description: "Optional: directory to search (default: current directory)",
				Default:     ".",
			},
			"include_definition": {
				Type:        "boolean",
				Description: "Include the definition in results (default: false)",
				Default:     false,
			},
		},
		Required: []string{"symbol"},
	}
}

func (t *FindReferencesTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *FindReferencesTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	symbol, ok := params["symbol"].(string)
	if !ok || symbol == "" {
		return &Result{
			Success: false,
			Error:   "symbol parameter must be a non-empty string",
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

	includeDef := false
	if id, ok := params["include_definition"].(bool); ok {
		includeDef = id
	}

	// Search for the symbol as a word boundary
	pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(symbol))

	ctx, cancel := t.execContextWithParent(ctx)
	defer cancel()

	var cmd *exec.Cmd
	if _, err := exec.LookPath("rg"); err == nil {
		cmd = exec.CommandContext(ctx, "rg", "-n", "--no-heading", pattern, searchPath)
	} else {
		cmd = exec.CommandContext(ctx, "grep", "-rn", "-w", symbol, searchPath)
	}
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)

	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "search command timed out",
		}, nil
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return &Result{
				Success: true,
				Data: map[string]any{
					"symbol":     symbol,
					"references": []map[string]any{},
					"count":      0,
				},
			}, nil
		}
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	references := []map[string]any{}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}

		lineNum := 0
		fmt.Sscanf(parts[1], "%d", &lineNum)

		content := strings.TrimSpace(parts[2])

		// Skip definitions unless requested
		if !includeDef {
			if strings.Contains(content, "func "+symbol) ||
				strings.Contains(content, "type "+symbol) ||
				strings.Contains(content, "class "+symbol) ||
				strings.Contains(content, "def "+symbol) {
				continue
			}
		}

		references = append(references, map[string]any{
			"file":    parts[0],
			"line":    lineNum,
			"content": content,
		})
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"symbol":     symbol,
			"references": references,
			"count":      len(references),
		},
	}

	// Abridge if many references
	const maxDisplay = 50
	if len(references) > maxDisplay {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"symbol":     symbol,
			"references": references[:maxDisplay],
			"count":      len(references),
			"summary":    fmt.Sprintf("Found %d references to '%s' (showing first %d)", len(references), symbol, maxDisplay),
		}
	}

	return result, nil
}

// GetFunctionSignatureTool gets function signature and documentation
type GetFunctionSignatureTool struct{ workDirAware }

func (t *GetFunctionSignatureTool) Name() string {
	return "get_function_signature"
}

func (t *GetFunctionSignatureTool) Description() string {
	return "Get the signature and documentation of a function including parameters, return types, and doc comments. Searches for Go functions with godoc, JavaScript/TypeScript functions with JSDoc, and Python functions with docstrings. Use this to understand how to call a function."
}

func (t *GetFunctionSignatureTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"function": {
				Type:        "string",
				Description: "Function name to get signature for",
			},
			"path": {
				Type:        "string",
				Description: "Optional: directory to search (default: current directory)",
				Default:     ".",
			},
		},
		Required: []string{"function"},
	}
}

func (t *GetFunctionSignatureTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *GetFunctionSignatureTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	funcName, ok := params["function"].(string)
	if !ok || funcName == "" {
		return &Result{
			Success: false,
			Error:   "function parameter must be a non-empty string",
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

	// Find the function definition
	signature, docs, file, line, err := t.findFunctionSignature(ctx, funcName, searchPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("function not found: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"function":  funcName,
			"signature": signature,
			"docs":      docs,
			"file":      file,
			"line":      line,
		},
	}, nil
}

func (t *GetFunctionSignatureTool) findFunctionSignature(ctx context.Context, funcName, searchPath string) (string, string, string, int, error) {
	// Search for function definition
	patterns := []string{
		fmt.Sprintf(`func\s+%s\s*\(`, funcName),
		fmt.Sprintf(`func\s+\(\w+\s+\*?\w+\)\s+%s\s*\(`, funcName),
		fmt.Sprintf(`function\s+%s\s*\(`, funcName),
		fmt.Sprintf(`def\s+%s\s*\(`, funcName),
	}

	for _, pattern := range patterns {
		ctx, cancel := t.execContextWithParent(ctx)

		var cmd *exec.Cmd
		if _, err := exec.LookPath("rg"); err == nil {
			cmd = exec.CommandContext(ctx, "rg", "-n", "-A", "10", pattern, searchPath)
		} else {
			cmd = exec.CommandContext(ctx, "grep", "-rn", "-A", "10", "-E", pattern, searchPath)
		}
		if strings.TrimSpace(t.workDir) != "" {
			cmd.Dir = strings.TrimSpace(t.workDir)
		}
		cmd.Env = mergeEnv(cmd.Env, t.env)

		output, err := cmd.CombinedOutput()
		cancel()
		if ctx.Err() != nil {
			return "", "", "", 0, fmt.Errorf("search timed out")
		}
		if err != nil {
			continue
		}

		lines := strings.Split(string(output), "\n")
		if len(lines) == 0 {
			continue
		}

		// Parse first match
		firstLine := lines[0]
		parts := strings.SplitN(firstLine, ":", 3)
		if len(parts) < 3 {
			continue
		}

		file := parts[0]
		lineNum := 0
		fmt.Sscanf(parts[1], "%d", &lineNum)

		// Extract signature and docs
		signature, docs := t.extractSignatureAndDocs(file, lineNum, funcName)

		return signature, docs, file, lineNum, nil
	}

	return "", "", "", 0, fmt.Errorf("function '%s' not found", funcName)
}

func (t *GetFunctionSignatureTool) extractSignatureAndDocs(file string, lineNum int, funcName string) (string, string) {
	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, file)
		if err != nil {
			return "", ""
		}
		file = abs
	}

	content, err := os.ReadFile(file)
	if err != nil {
		return "", ""
	}

	lines := strings.Split(string(content), "\n")
	if lineNum <= 0 || lineNum > len(lines) {
		return "", ""
	}

	// Get the signature line
	sigLine := strings.TrimSpace(lines[lineNum-1])
	signature := sigLine

	// Try to get full signature if it spans multiple lines
	if !strings.Contains(sigLine, "{") && lineNum < len(lines) {
		for i := lineNum; i < len(lines) && i < lineNum+5; i++ {
			signature += " " + strings.TrimSpace(lines[i])
			if strings.Contains(lines[i], "{") {
				break
			}
		}
	}

	// Extract documentation comments above the function
	docs := ""
	docLines := []string{}

	// Look backwards for comments
	for i := lineNum - 2; i >= 0 && i >= lineNum-20; i-- {
		line := strings.TrimSpace(lines[i])

		if line == "" {
			if len(docLines) > 0 {
				break
			}
			continue
		}

		// Go comments
		if strings.HasPrefix(line, "//") {
			docLines = append([]string{strings.TrimPrefix(line, "//")}, docLines...)
		} else if strings.HasPrefix(line, "/*") || strings.HasSuffix(line, "*/") {
			docLines = append([]string{line}, docLines...)
		} else if strings.HasPrefix(line, "*") {
			docLines = append([]string{strings.TrimPrefix(line, "*")}, docLines...)
		} else if strings.HasPrefix(line, "#") { // Python
			docLines = append([]string{strings.TrimPrefix(line, "#")}, docLines...)
		} else if strings.HasPrefix(line, `"""`) || strings.HasPrefix(line, "'''") { // Python docstrings
			docLines = append([]string{line}, docLines...)
		} else {
			// Non-comment line, stop searching
			if len(docLines) > 0 {
				break
			}
		}
	}

	if len(docLines) > 0 {
		docs = strings.Join(docLines, "\n")
	}

	return signature, docs
}
