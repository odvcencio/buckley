package builtin

import (
	"fmt"
	"iter"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// parensContentRe extracts content within parentheses for parameter parsing.
var parensContentRe = regexp.MustCompile(`\(([^)]*)\)`)

// GenerateDocstringTool generates documentation comments for functions
type GenerateDocstringTool struct{ workDirAware }

func (t *GenerateDocstringTool) Name() string {
	return "generate_docstring"
}

func (t *GenerateDocstringTool) Description() string {
	return "Generate documentation comments (godoc, JSDoc, or docstring) for a function, from its signature. Supports Go, JS/TS, Python."
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
				Description: "Name of the function to document. Python accepts qualified names such as Class.method or outer.inner to disambiguate.",
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
	content, err := t.readMutationFile(file)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	// Detect language
	language := t.detectLanguage(file)

	var docstring string
	var pythonParser *gotreesitter.Parser
	var lineNum, insert, end int
	prefix, suffix := "", "\n"
	if language == "python" {
		if int64(len(functionName)) > t.mutationFileLimit() || int64(len(description)) > t.mutationFileLimit() {
			return &Result{Error: mutationBudgetError(t.mutationFileLimit()).Error()}, nil
		}
		pythonParser = newPythonDocumentationParser()
		edit, err := t.pythonDocstringEdit(pythonParser, content, functionName, description)
		if err != nil {
			return &Result{Error: fmt.Sprintf("failed to generate docstring: %v", err)}, nil
		}
		if edit.existing {
			return &Result{Success: true, Data: map[string]any{
				"file": file, "function_name": functionName, "line": edit.line, "language": language, "message": "Function already has documentation",
			}}, nil
		}
		docstring, lineNum, insert, end = edit.docstring, edit.line, edit.start, edit.end
		prefix, suffix = edit.prefix, edit.suffix
	} else {
		docstring, lineNum, err = t.generateDocstring(content, functionName, description, language)
		if err != nil {
			return &Result{Error: fmt.Sprintf("failed to generate docstring: %v", err)}, nil
		}
		insert = mutationLineOffset(content, lineNum)
		end = insert
		if lineNum > 1 {
			previous := mutationLineOffset(content, lineNum-1)
			prevLine := strings.TrimSpace(content[previous:insert])
			if t.isDocComment(prevLine, language) {
				return &Result{Success: true, Data: map[string]any{
					"file": file, "function_name": functionName, "message": "Function already has documentation", "existing_doc": prevLine,
				}}, nil
			}
		}
	}
	if err := t.checkReplacementGrowth(len(content), end-insert, len(prefix)+len(docstring)+len(suffix), 1); err != nil {
		return &Result{Error: err.Error()}, nil
	}
	var output strings.Builder
	output.Grow(len(content) - (end - insert) + len(prefix) + len(docstring) + len(suffix))
	output.WriteString(content[:insert])
	output.WriteString(prefix)
	output.WriteString(docstring)
	output.WriteString(suffix)
	output.WriteString(content[end:])
	newContent := output.String()
	if language == "python" {
		tree, err := parsePythonDocumentation(pythonParser, newContent)
		if err != nil {
			return &Result{Error: fmt.Sprintf("generated Python documentation is invalid: %v", err)}, nil
		}
		tree.Release()
	}

	// Write file
	if err := writeMutationFile(file, newContent, 0644); err != nil {
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
	}
	return false
}

func (t *GenerateDocstringTool) generateDocstring(content, functionName, description, language string) (string, int, error) {
	if int64(len(functionName)) > t.mutationFileLimit() || int64(len(description)) > t.mutationFileLimit() {
		return "", 0, mutationBudgetError(t.mutationFileLimit())
	}
	// Find the function
	var expression string
	name := regexp.QuoteMeta(functionName)
	switch language {
	case "go":
		expression = fmt.Sprintf(`(?m)^func\s+(?:\(\w+\s+\*?\w+\)\s+)?%s\s*\([^)]*\)`, name)
	case "javascript":
		expression = fmt.Sprintf(`(?m)^(?:export\s+)?(?:async\s+)?function\s+%s\s*\(`, name)
	default:
		return "", 0, fmt.Errorf("unsupported language: %s", language)
	}

	pattern, err := regexp.Compile(expression)
	if err != nil {
		return "", 0, fmt.Errorf("invalid function name: %w", err)
	}
	match := pattern.FindStringIndex(content)
	if match == nil {
		return "", 0, fmt.Errorf("function %s not found", functionName)
	}

	// Find line number
	lineNum := strings.Count(content[:match[0]], "\n") + 1

	// Extract function signature
	signature, _, _ := strings.Cut(content[match[0]:], "\n")

	// Generate appropriate docstring
	var docstring string
	switch language {
	case "go":
		docstring, err = t.generateGoDoc(functionName, signature, description)
	case "javascript":
		docstring, err = t.generateJSDoc(functionName, signature, description)
	}

	return docstring, lineNum, err
}

func (t *GenerateDocstringTool) generateGoDoc(functionName, signature, description string) (string, error) {
	if description == "" {
		description = fmt.Sprintf("%s TODO: add description", functionName)
	}
	doc := generatedText{limit: t.mutationFileLimit()}
	doc.append("// ", description, "\n")
	first := true
	for param := range docParameters(signature, "go") {
		if first {
			doc.append("//\n")
			first = false
		}
		if !doc.append("// ", param, ": TODO: describe parameter\n") {
			return doc.result()
		}
	}
	if t.extractGoReturnType(signature) != "" {
		doc.append("//\n// Returns: TODO: describe return value\n")
	}
	text, err := doc.result()
	return strings.TrimRight(text, "\n"), err
}

func (t *GenerateDocstringTool) generateJSDoc(functionName, signature, description string) (string, error) {
	if description == "" {
		description = "TODO: add description"
	}
	doc := generatedText{limit: t.mutationFileLimit()}
	doc.append("/**\n * ", description, "\n")
	first := true
	for param := range docParameters(signature, "javascript") {
		if first {
			doc.append(" *\n")
			first = false
		}
		if !doc.append(" * @param {*} ", param, " - TODO: describe parameter\n") {
			return doc.result()
		}
	}
	doc.append(" * @returns {*} TODO: describe return value\n */")
	return doc.result()
}

func (t *GenerateDocstringTool) extractGoReturnType(signature string) string {
	if _, after, ok := strings.Cut(signature, ")"); ok {
		result := strings.TrimSpace(after)
		if result != "" && result != "{" {
			return result
		}
	}
	return ""
}

func docParameters(signature, language string) iter.Seq[string] {
	return func(yield func(string) bool) {
		matches := parensContentRe.FindStringSubmatch(signature)
		if len(matches) < 2 {
			return
		}
		for param := range strings.SplitSeq(matches[1], ",") {
			param = strings.TrimSpace(param)
			if param == "" {
				continue
			}
			switch language {
			case "go":
				for field := range strings.FieldsSeq(param) {
					param = field
					break
				}
			case "javascript":
				param, _, _ = strings.Cut(param, "=")
				param, _, _ = strings.Cut(param, ":")
			}
			if !yield(strings.TrimSpace(param)) {
				return
			}
		}
	}
}
