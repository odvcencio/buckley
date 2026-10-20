package builtin

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

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
				Description: "Name of the function to document. Qualified names such as Type.Method, Class.method, or outer.inner disambiguate targets.",
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
	if value, present := params["description"]; present && value != nil {
		desc, ok := value.(string)
		if !ok {
			return &Result{Error: "description parameter must be a string"}, nil
		}
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

	if int64(len(functionName)) > t.mutationFileLimit() || int64(len(description)) > t.mutationFileLimit() {
		return &Result{Error: mutationBudgetError(t.mutationFileLimit()).Error()}, nil
	}
	var edit documentationEdit
	var syntaxParser *gotreesitter.Parser
	var syntaxLanguage *gotreesitter.Language
	switch language {
	case "go":
		edit, err = t.goDocstringEdit(content, functionName, description)
	case "python":
		syntaxLanguage = grammars.PythonLanguage()
		syntaxParser = newFunctionParser(syntaxLanguage)
		edit, err = t.pythonDocstringEdit(syntaxParser, content, functionName, description)
	case "javascript":
		entry := grammars.DetectLanguage(file)
		if entry == nil {
			return &Result{Error: "unsupported JavaScript/TypeScript file type"}, nil
		}
		syntaxLanguage = entry.Language()
		syntaxParser = newFunctionParser(syntaxLanguage)
		edit, err = t.jsDocstringEdit(syntaxParser, syntaxLanguage, content, functionName, description)
	default:
		err = fmt.Errorf("unsupported language: %s", language)
	}
	if err != nil {
		return &Result{Error: fmt.Sprintf("failed to generate docstring: %v", err)}, nil
	}
	if edit.existing {
		return &Result{Success: true, Data: map[string]any{"file": file, "function_name": functionName, "line": edit.line, "language": language, "message": "Function already has documentation"}}, nil
	}
	docstring, lineNum, insert, end := edit.docstring, edit.line, edit.start, edit.end
	prefix, suffix := edit.prefix, edit.suffix
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
	if language == "go" {
		if _, err := parser.ParseFile(token.NewFileSet(), file, newContent, parser.SkipObjectResolution); err != nil {
			return &Result{Error: fmt.Sprintf("generated Go documentation is invalid: %v", err)}, nil
		}
	} else {
		tree, err := parseFunctionTree(syntaxParser, newContent)
		if err != nil {
			return &Result{Error: fmt.Sprintf("generated documentation is invalid: %v", err)}, nil
		}
		if language == "javascript" && documentationSyntaxDigest(tree.RootNode(), syntaxLanguage, newContent) != edit.syntaxDigest {
			tree.Release()
			return &Result{Error: "documentation would change code structure; split the target declaration onto its own line"}, nil
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
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return "javascript"
	case ".py":
		return "python"
	default:
		return "unknown"
	}
}
