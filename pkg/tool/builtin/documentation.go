package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GenerateDocstringTool generates documentation comments for functions
type GenerateDocstringTool struct{ workDirAware }

func (t *GenerateDocstringTool) Name() string {
	return "generate_docstring"
}

func (t *GenerateDocstringTool) Description() string {
	return "Generate documentation comments (docstrings/godoc/JSDoc) for a function or method. Analyzes function signature, parameters, and return values to create appropriate documentation. Supports Go, JavaScript/TypeScript, and Python. Use this to add or improve code documentation."
}

func (t *GenerateDocstringTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"file": {
				Type:        "string",
				Description: "File containing the function",
			},
			"function_name": {
				Type:        "string",
				Description: "Name of the function to document",
			},
			"description": {
				Type:        "string",
				Description: "Optional: custom description of what the function does",
			},
		},
		Required: []string{"file", "function_name"},
	}
}

func (t *GenerateDocstringTool) Execute(params map[string]any) (*Result, error) {
	file, ok := params["file"].(string)
	if !ok || file == "" {
		return &Result{
			Success: false,
			Error:   "file parameter must be a non-empty string",
		}, nil
	}

	functionName, ok := params["function_name"].(string)
	if !ok || functionName == "" {
		return &Result{
			Success: false,
			Error:   "function_name parameter must be a non-empty string",
		}, nil
	}

	description := ""
	if desc, ok := params["description"].(string); ok {
		description = desc
	}

	if strings.TrimSpace(t.workDir) != "" {
		abs, err := resolvePath(t.workDir, file)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		file = abs
	}

	// Read file
	content, err := os.ReadFile(file)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	// Detect language
	language := t.detectLanguage(file)

	// Find function and generate docstring
	docstring, lineNum, err := t.generateDocstring(string(content), functionName, description, language)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to generate docstring: %v", err),
		}, nil
	}

	// Insert docstring into file
	lines := strings.Split(string(content), "\n")
	if lineNum <= 0 || lineNum > len(lines) {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("invalid line number: %d", lineNum),
		}, nil
	}

	// Check if documentation already exists
	if lineNum > 1 {
		prevLine := strings.TrimSpace(lines[lineNum-2])
		if t.isDocComment(prevLine, language) {
			return &Result{
				Success: true,
				Data: map[string]any{
					"file":          file,
					"function_name": functionName,
					"message":       "Function already has documentation",
					"existing_doc":  prevLine,
				},
			}, nil
		}
	}

	// Insert docstring before function
	newLines := make([]string, 0, len(lines)+strings.Count(docstring, "\n")+1)
	newLines = append(newLines, lines[:lineNum-1]...)
	newLines = append(newLines, docstring)
	newLines = append(newLines, lines[lineNum-1:]...)

	newContent := strings.Join(newLines, "\n")

	// Write file
	if err := os.WriteFile(file, []byte(newContent), 0644); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"file":          file,
			"function_name": functionName,
			"line":          lineNum,
			"docstring":     docstring,
			"language":      language,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"file":          file,
		"function_name": functionName,
		"summary":       fmt.Sprintf("✓ Added documentation for %s at line %d", functionName, lineNum),
	}

	return result, nil
}

func (t *GenerateDocstringTool) detectLanguage(file string) string {
	ext := filepath.Ext(file)
	switch ext {
	case ".go":
		return "go"
	case ".js", ".ts":
		return "javascript"
	case ".py":
		return "python"
	default:
		return "unknown"
	}
}

func (t *GenerateDocstringTool) isDocComment(line, language string) bool {
	switch language {
	case "go":
		return strings.HasPrefix(line, "//")
	case "javascript":
		return strings.HasPrefix(line, "/**") || strings.HasPrefix(line, "//")
	case "python":
		return strings.HasPrefix(line, "\"\"\"") || strings.HasPrefix(line, "#")
	}
	return false
}

func (t *GenerateDocstringTool) generateDocstring(content, functionName, description, language string) (string, int, error) {
	// Find the function
	var pattern *regexp.Regexp
	switch language {
	case "go":
		pattern = regexp.MustCompile(fmt.Sprintf(`(?m)^func\s+(?:\(\w+\s+\*?\w+\)\s+)?%s\s*\([^)]*\)`, functionName))
	case "javascript":
		pattern = regexp.MustCompile(fmt.Sprintf(`(?m)^(?:export\s+)?(?:async\s+)?function\s+%s\s*\(`, functionName))
	case "python":
		pattern = regexp.MustCompile(fmt.Sprintf(`(?m)^def\s+%s\s*\(`, functionName))
	default:
		return "", 0, fmt.Errorf("unsupported language: %s", language)
	}

	match := pattern.FindStringIndex(content)
	if match == nil {
		return "", 0, fmt.Errorf("function %s not found", functionName)
	}

	// Find line number
	lineNum := strings.Count(content[:match[0]], "\n") + 1

	// Extract function signature
	lines := strings.Split(content, "\n")
	signature := lines[lineNum-1]

	// Generate appropriate docstring
	var docstring string
	switch language {
	case "go":
		docstring = t.generateGoDoc(functionName, signature, description)
	case "javascript":
		docstring = t.generateJSDoc(functionName, signature, description)
	case "python":
		docstring = t.generatePythonDoc(functionName, signature, description)
	}

	return docstring, lineNum, nil
}

func (t *GenerateDocstringTool) generateGoDoc(functionName, signature, description string) string {
	if description == "" {
		description = fmt.Sprintf("%s TODO: add description", functionName)
	}

	// Extract parameters
	params := t.extractGoParams(signature)
	returnType := t.extractGoReturnType(signature)

	doc := fmt.Sprintf("// %s\n", description)

	if len(params) > 0 {
		doc += "//\n"
		for _, param := range params {
			doc += fmt.Sprintf("// %s: TODO: describe parameter\n", param)
		}
	}

	if returnType != "" {
		doc += "//\n"
		doc += "// Returns: TODO: describe return value\n"
	}

	return strings.TrimRight(doc, "\n")
}

func (t *GenerateDocstringTool) generateJSDoc(functionName, signature, description string) string {
	if description == "" {
		description = "TODO: add description"
	}

	params := t.extractJSParams(signature)

	doc := "/**\n"
	doc += fmt.Sprintf(" * %s\n", description)

	if len(params) > 0 {
		doc += " *\n"
		for _, param := range params {
			doc += fmt.Sprintf(" * @param {*} %s - TODO: describe parameter\n", param)
		}
	}

	doc += " * @returns {*} TODO: describe return value\n"
	doc += " */"

	return doc
}

func (t *GenerateDocstringTool) generatePythonDoc(functionName, signature, description string) string {
	if description == "" {
		description = "TODO: add description"
	}

	params := t.extractPythonParams(signature)

	// Get indentation from signature
	indent := ""
	for _, ch := range signature {
		if ch == ' ' || ch == '\t' {
			indent += string(ch)
		} else {
			break
		}
	}

	doc := indent + "\"\"\"\n"
	doc += indent + description + "\n"

	if len(params) > 0 {
		doc += indent + "\n"
		doc += indent + "Args:\n"
		for _, param := range params {
			doc += indent + fmt.Sprintf("    %s: TODO: describe parameter\n", param)
		}
	}

	doc += indent + "\n"
	doc += indent + "Returns:\n"
	doc += indent + "    TODO: describe return value\n"
	doc += indent + "\"\"\""

	return doc
}

func (t *GenerateDocstringTool) extractGoParams(signature string) []string {
	// Extract parameters from Go function signature
	re := regexp.MustCompile(`\(([^)]*)\)`)
	matches := re.FindStringSubmatch(signature)
	if len(matches) < 2 {
		return []string{}
	}

	params := []string{}
	for _, param := range strings.Split(matches[1], ",") {
		param = strings.TrimSpace(param)
		if param != "" {
			// Extract param name (before type)
			parts := strings.Fields(param)
			if len(parts) > 0 {
				params = append(params, parts[0])
			}
		}
	}

	return params
}

func (t *GenerateDocstringTool) extractGoReturnType(signature string) string {
	// Simple return type extraction
	if strings.Contains(signature, ")") {
		parts := strings.Split(signature, ")")
		if len(parts) > 1 {
			returnType := strings.TrimSpace(parts[1])
			if returnType != "" && returnType != "{" {
				return returnType
			}
		}
	}
	return ""
}

func (t *GenerateDocstringTool) extractJSParams(signature string) []string {
	re := regexp.MustCompile(`\(([^)]*)\)`)
	matches := re.FindStringSubmatch(signature)
	if len(matches) < 2 {
		return []string{}
	}

	params := []string{}
	for _, param := range strings.Split(matches[1], ",") {
		param = strings.TrimSpace(param)
		if param != "" {
			// Remove default values and type annotations
			param = strings.Split(param, "=")[0]
			param = strings.Split(param, ":")[0]
			params = append(params, strings.TrimSpace(param))
		}
	}

	return params
}

func (t *GenerateDocstringTool) extractPythonParams(signature string) []string {
	re := regexp.MustCompile(`\(([^)]*)\)`)
	matches := re.FindStringSubmatch(signature)
	if len(matches) < 2 {
		return []string{}
	}

	params := []string{}
	for _, param := range strings.Split(matches[1], ",") {
		param = strings.TrimSpace(param)
		if param != "" && param != "self" && param != "cls" {
			// Remove type hints and default values
			param = strings.Split(param, ":")[0]
			param = strings.Split(param, "=")[0]
			params = append(params, strings.TrimSpace(param))
		}
	}

	return params
}

// ExplainCodeTool explains what a code snippet does
type ExplainCodeTool struct{ workDirAware }

func (t *ExplainCodeTool) Name() string {
	return "explain_code"
}

func (t *ExplainCodeTool) Description() string {
	return "Analyze and explain what a code snippet or function does. Provides a plain English explanation of the code's purpose, logic flow, and key operations. Use this to understand unfamiliar code or document complex logic."
}

func (t *ExplainCodeTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"file": {
				Type:        "string",
				Description: "File containing the code (optional if code is provided)",
			},
			"start_line": {
				Type:        "integer",
				Description: "Starting line number if explaining a range",
			},
			"end_line": {
				Type:        "integer",
				Description: "Ending line number if explaining a range",
			},
			"code": {
				Type:        "string",
				Description: "Code snippet to explain (optional if file is provided)",
			},
			"function_name": {
				Type:        "string",
				Description: "Specific function to explain",
			},
		},
		Required: []string{},
	}
}

func (t *ExplainCodeTool) Execute(params map[string]any) (*Result, error) {
	var codeToExplain string
	var source string

	// Get code from file or direct input
	if file, ok := params["file"].(string); ok && file != "" {
		if strings.TrimSpace(t.workDir) != "" {
			abs, err := resolvePath(t.workDir, file)
			if err != nil {
				return &Result{Success: false, Error: err.Error()}, nil
			}
			file = abs
		}
		content, err := os.ReadFile(file)
		if err != nil {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("failed to read file: %v", err),
			}, nil
		}

		source = file

		// Extract specific range or function
		if functionName, ok := params["function_name"].(string); ok && functionName != "" {
			codeToExplain, err = t.extractFunction(string(content), functionName, file)
			if err != nil {
				return &Result{
					Success: false,
					Error:   fmt.Sprintf("failed to extract function: %v", err),
				}, nil
			}
			source = fmt.Sprintf("%s:%s", file, functionName)
		} else if startLine, ok := params["start_line"].(float64); ok {
			endLine := startLine
			if el, ok := params["end_line"].(float64); ok {
				endLine = el
			}

			lines := strings.Split(string(content), "\n")
			if int(startLine) <= len(lines) && int(endLine) <= len(lines) {
				codeToExplain = strings.Join(lines[int(startLine)-1:int(endLine)], "\n")
				source = fmt.Sprintf("%s:%d-%d", file, int(startLine), int(endLine))
			}
		} else {
			codeToExplain = string(content)
		}
	} else if code, ok := params["code"].(string); ok && code != "" {
		codeToExplain = code
		source = "provided code snippet"
	} else {
		return &Result{
			Success: false,
			Error:   "either file or code parameter must be provided",
		}, nil
	}

	if codeToExplain == "" {
		return &Result{
			Success: false,
			Error:   "no code to explain",
		}, nil
	}

	// Analyze the code
	explanation := t.analyzeCode(codeToExplain)

	return &Result{
		Success: true,
		Data: map[string]any{
			"source":      source,
			"code":        codeToExplain,
			"explanation": explanation,
		},
	}, nil
}

func (t *ExplainCodeTool) extractFunction(content, functionName, file string) (string, error) {
	language := ""
	ext := filepath.Ext(file)
	switch ext {
	case ".go":
		language = "go"
	case ".js", ".ts":
		language = "javascript"
	case ".py":
		language = "python"
	}

	var pattern *regexp.Regexp
	switch language {
	case "go":
		pattern = regexp.MustCompile(fmt.Sprintf(`(?s)(func\s+(?:\(\w+\s+\*?\w+\)\s+)?%s\s*\([^)]*\)[^{]*\{.*?\n\})`, functionName))
	case "javascript":
		pattern = regexp.MustCompile(fmt.Sprintf(`(?s)((?:export\s+)?(?:async\s+)?function\s+%s\s*\([^)]*\)[^{]*\{.*?\n\})`, functionName))
	case "python":
		pattern = regexp.MustCompile(fmt.Sprintf(`(?s)(def\s+%s\s*\([^)]*\):.*?)(?:\n(?:def|class)\s|\z)`, functionName))
	default:
		return "", fmt.Errorf("unsupported file type: %s", ext)
	}

	matches := pattern.FindStringSubmatch(content)
	if len(matches) < 2 {
		return "", fmt.Errorf("function %s not found", functionName)
	}

	return matches[1], nil
}

func (t *ExplainCodeTool) analyzeCode(code string) map[string]any {
	// Provide basic code analysis
	lines := strings.Split(code, "\n")
	explanation := map[string]any{
		"lines":               len(lines),
		"has_loops":           t.hasPattern(code, `\b(for|while)\b`),
		"has_conditions":      t.hasPattern(code, `\bif\b`),
		"has_functions":       t.hasPattern(code, `\b(func|function|def)\b`),
		"has_error_handling":  t.hasPattern(code, `\b(try|catch|error|err)\b`),
		"complexity_estimate": t.estimateComplexity(code),
		"summary":             t.generateSummary(code),
	}

	return explanation
}

func (t *ExplainCodeTool) hasPattern(code, pattern string) bool {
	re := regexp.MustCompile(pattern)
	return re.MatchString(code)
}

func (t *ExplainCodeTool) estimateComplexity(code string) string {
	// Count complexity indicators
	indicators := []string{`\bif\b`, `\bfor\b`, `\bwhile\b`, `\bswitch\b`, `\bcase\b`, `\&\&`, `\|\|`}
	count := 0
	for _, pattern := range indicators {
		re := regexp.MustCompile(pattern)
		count += len(re.FindAllString(code, -1))
	}

	if count <= 5 {
		return "low"
	} else if count <= 15 {
		return "medium"
	}
	return "high"
}

func (t *ExplainCodeTool) generateSummary(code string) string {
	// Generate a basic summary
	parts := []string{}

	if t.hasPattern(code, `\b(func|function|def)\b`) {
		parts = append(parts, "defines functions")
	}
	if t.hasPattern(code, `\b(for|while)\b`) {
		parts = append(parts, "contains loops")
	}
	if t.hasPattern(code, `\bif\b`) {
		parts = append(parts, "has conditional logic")
	}
	if t.hasPattern(code, `\b(try|catch|error)\b`) {
		parts = append(parts, "includes error handling")
	}
	if t.hasPattern(code, `\breturn\b`) {
		parts = append(parts, "returns values")
	}

	if len(parts) == 0 {
		return "Code snippet with basic operations"
	}

	return "Code that " + strings.Join(parts, ", ")
}
