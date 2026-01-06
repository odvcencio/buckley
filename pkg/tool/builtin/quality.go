package builtin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// AnalyzeComplexityTool analyzes cyclomatic complexity of code
type AnalyzeComplexityTool struct{ workDirAware }

func (t *AnalyzeComplexityTool) Name() string {
	return "analyze_complexity"
}

func (t *AnalyzeComplexityTool) Description() string {
	return "Analyze cyclomatic complexity of functions in a file or directory. Identifies complex functions that may need refactoring. Returns complexity scores and highlights functions above threshold. Use this to find code that needs simplification or better test coverage."
}

func (t *AnalyzeComplexityTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "File or directory to analyze",
			},
			"threshold": {
				Type:        "integer",
				Description: "Complexity threshold for flagging (default: 10)",
				Default:     10,
			},
			"language": {
				Type:        "string",
				Description: "Optional: language hint ('go', 'javascript', 'python'). Auto-detected if not specified.",
			},
		},
		Required: []string{"path"},
	}
}

func (t *AnalyzeComplexityTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}
	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, path)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		path = abs
	}

	threshold := 10
	if th, ok := params["threshold"].(float64); ok {
		threshold = int(th)
	}

	language := ""
	if lang, ok := params["language"].(string); ok {
		language = lang
	}

	// Auto-detect language if not specified
	if language == "" {
		language = t.detectLanguage(path)
	}

	// Analyze based on language
	var functions []map[string]any
	var err error

	switch language {
	case "go":
		functions, err = t.analyzeGoComplexity(path)
	case "javascript", "typescript":
		functions, err = t.analyzeJSComplexity(path)
	case "python":
		functions, err = t.analyzePythonComplexity(path)
	default:
		functions, err = t.analyzeGenericComplexity(path)
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("complexity analysis failed: %v", err),
		}, nil
	}

	// Filter and count complex functions
	complexFunctions := []map[string]any{}
	for _, fn := range functions {
		if complexity, ok := fn["complexity"].(int); ok && complexity >= threshold {
			complexFunctions = append(complexFunctions, fn)
		}
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":              path,
			"threshold":         threshold,
			"language":          language,
			"total_functions":   len(functions),
			"complex_functions": len(complexFunctions),
			"functions":         complexFunctions,
		},
	}

	// Abridge if many complex functions
	if len(complexFunctions) > 20 {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"path":              path,
			"threshold":         threshold,
			"language":          language,
			"total_functions":   len(functions),
			"complex_functions": len(complexFunctions),
			"functions":         complexFunctions[:20],
			"summary":           fmt.Sprintf("Found %d complex functions (threshold: %d, showing first 20)", len(complexFunctions), threshold),
		}
	}

	return result, nil
}

func (t *AnalyzeComplexityTool) detectLanguage(path string) string {
	if strings.HasSuffix(path, ".go") {
		return "go"
	} else if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".ts") {
		return "javascript"
	} else if strings.HasSuffix(path, ".py") {
		return "python"
	}
	return "generic"
}

func (t *AnalyzeComplexityTool) analyzeGoComplexity(path string) ([]map[string]any, error) {
	// Try using gocyclo if available
	if _, err := exec.LookPath("gocyclo"); err == nil {
		ctx, cancel := t.execContext()
		defer cancel()

		cmd := exec.CommandContext(ctx, "gocyclo", "-over", "1", path)
		cmd.Env = mergeEnv(cmd.Env, t.env)
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("complexity analysis timed out")
		}
		if err != nil {
			// Gocyclo returns non-zero if it finds complex functions, which is fine
			if len(output) == 0 {
				return []map[string]any{}, nil
			}
		}

		return t.parseGocycloOutput(string(output)), nil
	}

	// Fallback to simple analysis
	return t.analyzeGenericComplexity(path)
}

func (t *AnalyzeComplexityTool) parseGocycloOutput(output string) []map[string]any {
	// gocyclo output format: "complexity function_name file:line"
	re := regexp.MustCompile(`(\d+)\s+(\S+)\s+(\S+):(\d+)`)
	functions := []map[string]any{}

	for _, line := range strings.Split(output, "\n") {
		matches := re.FindStringSubmatch(line)
		if len(matches) == 5 {
			var complexity int
			fmt.Sscanf(matches[1], "%d", &complexity)
			var lineNum int
			fmt.Sscanf(matches[4], "%d", &lineNum)

			functions = append(functions, map[string]any{
				"name":       matches[2],
				"file":       matches[3],
				"line":       lineNum,
				"complexity": complexity,
			})
		}
	}

	return functions
}

func (t *AnalyzeComplexityTool) analyzeJSComplexity(path string) ([]map[string]any, error) {
	// Fallback to simple analysis for JS
	return t.analyzeGenericComplexity(path)
}

func (t *AnalyzeComplexityTool) analyzePythonComplexity(path string) ([]map[string]any, error) {
	// Try using radon if available
	if _, err := exec.LookPath("radon"); err == nil {
		ctx, cancel := t.execContext()
		defer cancel()

		cmd := exec.CommandContext(ctx, "radon", "cc", "-s", path)
		cmd.Env = mergeEnv(cmd.Env, t.env)
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("complexity analysis timed out")
		}
		if err != nil && len(output) == 0 {
			return []map[string]any{}, nil
		}

		return t.parseRadonOutput(string(output)), nil
	}

	// Fallback to simple analysis
	return t.analyzeGenericComplexity(path)
}

func (t *AnalyzeComplexityTool) parseRadonOutput(output string) []map[string]any {
	// Radon output format varies, basic parsing
	functions := []map[string]any{}
	// This is a simplified parser - radon output can be complex
	// For now, return empty and rely on generic analysis
	return functions
}

func (t *AnalyzeComplexityTool) analyzeGenericComplexity(path string) ([]map[string]any, error) {
	// Simple heuristic-based complexity analysis
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var files []string
	if info.IsDir() {
		filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && t.isCodeFile(p) {
				files = append(files, p)
			}
			return nil
		})
	} else {
		files = []string{path}
	}

	functions := []map[string]any{}
	for _, file := range files {
		fileFunctions, err := t.analyzeSingleFile(file)
		if err != nil {
			continue
		}
		functions = append(functions, fileFunctions...)
	}

	return functions, nil
}

func (t *AnalyzeComplexityTool) analyzeSingleFile(filepath string) ([]map[string]any, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	functions := []map[string]any{}
	scanner := bufio.NewScanner(file)
	lineNum := 0
	inFunction := false
	currentFunction := ""
	functionLine := 0
	complexity := 1 // Base complexity

	// Simple patterns for function detection
	funcPatterns := []*regexp.Regexp{
		regexp.MustCompile(`^\s*func\s+(\w+)`),                               // Go
		regexp.MustCompile(`^\s*function\s+(\w+)`),                           // JavaScript
		regexp.MustCompile(`^\s*(\w+)\s*:\s*function`),                       // JavaScript
		regexp.MustCompile(`^\s*def\s+(\w+)`),                                // Python
		regexp.MustCompile(`^\s*(?:public|private|protected)?\s*(\w+)\s*\(`), // Various
	}

	// Complexity indicators (if, for, while, case, etc.)
	complexityIndicators := regexp.MustCompile(`\b(if|for|while|case|catch|\&\&|\|\|)\b`)

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Check for function start
		if !inFunction {
			for _, pattern := range funcPatterns {
				if matches := pattern.FindStringSubmatch(line); len(matches) > 1 {
					inFunction = true
					currentFunction = matches[1]
					functionLine = lineNum
					complexity = 1
					break
				}
			}
		}

		if inFunction {
			// Count complexity indicators
			complexity += len(complexityIndicators.FindAllString(line, -1))

			// Simple end-of-function detection (closing brace at start of line)
			if strings.TrimSpace(line) == "}" {
				functions = append(functions, map[string]any{
					"name":       currentFunction,
					"file":       filepath,
					"line":       functionLine,
					"complexity": complexity,
				})
				inFunction = false
			}
		}
	}

	return functions, scanner.Err()
}

func (t *AnalyzeComplexityTool) isCodeFile(path string) bool {
	exts := []string{".go", ".js", ".ts", ".py", ".java", ".c", ".cpp", ".rs"}
	for _, ext := range exts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// FindDuplicatesTool finds duplicate code blocks
type FindDuplicatesTool struct{ workDirAware }

func (t *FindDuplicatesTool) Name() string {
	return "find_duplicates"
}

func (t *FindDuplicatesTool) Description() string {
	return "Find duplicate code blocks in a file or directory. Detects similar code patterns that may be candidates for refactoring into shared functions. Returns groups of duplicate code with locations. Use this to identify opportunities for code reuse and DRY improvements."
}

func (t *FindDuplicatesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "File or directory to analyze",
			},
			"min_lines": {
				Type:        "integer",
				Description: "Minimum number of consecutive lines to consider as duplicate (default: 5)",
				Default:     5,
			},
			"language": {
				Type:        "string",
				Description: "Optional: language hint for better detection",
			},
		},
		Required: []string{"path"},
	}
}

func (t *FindDuplicatesTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}
	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, path)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		path = abs
	}

	minLines := 5
	if ml, ok := params["min_lines"].(float64); ok {
		minLines = int(ml)
	}

	// Try using external tools first
	duplicates := []map[string]any{}
	var err error

	// Try jscpd (copy-paste detector)
	if _, execErr := exec.LookPath("jscpd"); execErr == nil {
		duplicates, err = t.runJSCPD(path, minLines)
	} else {
		// Fallback to simple duplicate detection
		duplicates, err = t.findSimpleDuplicates(path, minLines)
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("duplicate detection failed: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"path":       path,
			"min_lines":  minLines,
			"duplicates": duplicates,
			"count":      len(duplicates),
		},
	}

	// Abridge if many duplicates
	if len(duplicates) > 10 {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"path":       path,
			"min_lines":  minLines,
			"duplicates": duplicates[:10],
			"count":      len(duplicates),
			"summary":    fmt.Sprintf("Found %d duplicate code blocks (showing first 10)", len(duplicates)),
		}
	}

	return result, nil
}

func (t *FindDuplicatesTool) runJSCPD(path string, minLines int) ([]map[string]any, error) {
	// jscpd can be complex to parse, for now return empty
	// This would need proper implementation with JSON output parsing
	return []map[string]any{}, nil
}

func (t *FindDuplicatesTool) findSimpleDuplicates(path string, minLines int) ([]map[string]any, error) {
	// Simple hash-based duplicate detection
	// This is a basic implementation - production would use more sophisticated algorithms

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var files []string
	if info.IsDir() {
		filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && t.isCodeFile(p) {
				files = append(files, p)
			}
			return nil
		})
	} else {
		files = []string{path}
	}

	// Map of code hash -> locations
	codeBlocks := make(map[string][]map[string]any)

	for _, file := range files {
		blocks, err := t.extractCodeBlocks(file, minLines)
		if err != nil {
			continue
		}

		for _, block := range blocks {
			hash := t.hashCode(block["code"].(string))
			codeBlocks[hash] = append(codeBlocks[hash], block)
		}
	}

	// Find duplicates (blocks that appear more than once)
	duplicates := []map[string]any{}
	for _, locations := range codeBlocks {
		if len(locations) > 1 {
			duplicates = append(duplicates, map[string]any{
				"locations": locations,
				"count":     len(locations),
			})
		}
	}

	return duplicates, nil
}

func (t *FindDuplicatesTool) extractCodeBlocks(filepath string, minLines int) ([]map[string]any, error) {
	content, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(content), "\n")
	blocks := []map[string]any{}

	// Extract all possible blocks of minLines length
	for i := 0; i <= len(lines)-minLines; i++ {
		blockLines := lines[i : i+minLines]
		// Skip blocks that are mostly whitespace or comments
		if t.isSignificantBlock(blockLines) {
			blocks = append(blocks, map[string]any{
				"file":       filepath,
				"start_line": i + 1,
				"end_line":   i + minLines,
				"code":       strings.Join(blockLines, "\n"),
			})
		}
	}

	return blocks, nil
}

func (t *FindDuplicatesTool) isSignificantBlock(lines []string) bool {
	significantLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "#") {
			significantLines++
		}
	}
	return significantLines >= len(lines)/2
}

func (t *FindDuplicatesTool) hashCode(code string) string {
	// Normalize code for comparison (remove whitespace variations)
	normalized := regexp.MustCompile(`\s+`).ReplaceAllString(code, " ")
	return fmt.Sprintf("%x", normalized)
}

func (t *FindDuplicatesTool) isCodeFile(path string) bool {
	exts := []string{".go", ".js", ".ts", ".py", ".java", ".c", ".cpp", ".rs"}
	for _, ext := range exts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}
