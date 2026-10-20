package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultComplexityOutputBytes = 8 << 10
	DefaultComplexityFunctions   = 20
	DefaultComplexityEntries     = DefaultAnalysisEntries
)

// AnalyzeComplexityTool estimates decisions in parsed functions without external programs.
type AnalyzeComplexityTool struct{ workDirAware }

func (t *AnalyzeComplexityTool) Name() string { return "analyze_complexity" }
func (t *AnalyzeComplexityTool) Description() string {
	return "Estimate cyclomatic complexity from syntax for Go, Python, JavaScript, and TypeScript functions, methods, and closures. Starts at 1 and counts branches, loops, boolean decisions, and handlers; excludes comments, strings, and nested function bodies. Returns the most complex functions first, with honest total and truncated counts. Directory scans skip .git, node_modules, vendor, virtual environments, and symlinks. Input, traversal, and output budgets require narrowing large scans. This is a syntax metric, not a correctness assessment."
}
func (t *AnalyzeComplexityTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"path":      {Type: "string", Description: "File or directory to analyze."},
		"threshold": {Type: "integer", Description: "Minimum complexity to return (default 10); omit or use null for default.", Default: 10},
		"language":  {Type: "string", Description: "Optional go, python, javascript, or typescript. For directories, filters matching extensions; otherwise detected per file. Omit or use null to detect."},
	}, Required: []string{"path"}}
	for _, name := range []string{"threshold", "language"} {
		p := schema.Properties[name]
		p.RawSchema = map[string]any{"type": []string{p.Type, "null"}, "description": p.Description}
		if p.Default != nil {
			p.RawSchema["default"] = p.Default
		}
		if name == "threshold" {
			p.RawSchema["minimum"] = 1
		}
		schema.Properties[name] = p
	}
	return schema
}
func (t *AnalyzeComplexityTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *AnalyzeComplexityTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	path, ok := params["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return &Result{Error: "path parameter must be a non-empty string"}, nil
	}
	threshold := 10
	if value := params["threshold"]; value != nil {
		n, err := positiveIntParameter("threshold", value)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		threshold = n
	}
	language := ""
	if value := params["language"]; value != nil {
		var ok bool
		language, ok = value.(string)
		if !ok {
			return &Result{Error: "language parameter must be a string"}, nil
		}
		switch language {
		case "", "go", "python", "javascript", "typescript":
		default:
			return &Result{Error: "unsupported language; use go, python, javascript, or typescript"}, nil
		}
	}
	path, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	analysis := complexityAnalysis{sourceScan: sourceScan{ctx: ctx, remaining: t.mutationFileLimit()}, threshold: threshold, functions: make([]complexityFunction, 0, DefaultComplexityFunctions)}
	err = analysis.scan(path, language)
	if err != nil {
		message := err.Error()
		// Parser diagnostics can include an entire malformed source token.
		if len(message) > 512 {
			message = strings.ToValidUTF8(message[:512], "�") + "... (diagnostic truncated)"
		}
		return &Result{Error: "complexity analysis failed: " + message}, nil
	}
	if language == "" {
		language = "mixed"
		if detected, _ := complexityLanguage(path); analysis.files == 1 && analysis.entries == 0 && detected != "" {
			language = detected
		}
	}
	data := map[string]any{"path": path, "threshold": threshold, "language": language, "metric": "syntax_decisions", "total_functions": analysis.total, "complex_functions": analysis.complex, "files_analyzed": analysis.files, "source_bytes": t.mutationFileLimit() - analysis.remaining}
	budget := DefaultComplexityOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	codec := toon.New(true)
	for count := len(analysis.functions); ; count /= 2 {
		rows := make([]map[string]any, 0, count)
		for _, fn := range analysis.functions[:count] {
			rows = append(rows, map[string]any{"name": fn.name, "file": fn.file, "line": fn.line, "complexity": fn.complexity})
		}
		data["functions"] = rows
		data["returned"] = count
		data["truncated"] = count < analysis.complex
		result := &Result{Success: true, Data: data}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		text, err := codec.MarshalString(result)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return &Result{Error: fmt.Sprintf("complexity analysis canceled: %v", err)}, nil
		}
		if max(len(encoded), len(text)) <= budget {
			return result, nil
		}
		if count <= 1 {
			return &Result{Error: fmt.Sprintf("complexity result exceeds %d-byte output budget; narrow the path or increase threshold", budget)}, nil
		}
	}
}

type complexityFunction struct {
	name, file       string
	line, complexity int
}
type complexityAnalysis struct {
	sourceScan
	threshold, total, complex int
	functions                 []complexityFunction
}

func (a *complexityAnalysis) record(fn complexityFunction) error {
	a.total++
	if fn.complexity < a.threshold {
		return nil
	}
	a.complex++
	i := sort.Search(len(a.functions), func(i int) bool {
		other := a.functions[i]
		if fn.complexity != other.complexity {
			return fn.complexity > other.complexity
		}
		if fn.file != other.file {
			return fn.file < other.file
		}
		if fn.line != other.line {
			return fn.line < other.line
		}
		return fn.name < other.name
	})
	if i == DefaultComplexityFunctions {
		return nil
	}
	if len(fn.name) > DefaultComplexityOutputBytes {
		return fmt.Errorf("function name exceeds %d-byte output budget in %s", DefaultComplexityOutputBytes, fn.file)
	}
	if len(a.functions) < DefaultComplexityFunctions {
		a.functions = append(a.functions, complexityFunction{})
	}
	copy(a.functions[i+1:], a.functions[i:len(a.functions)-1])
	fn.name = strings.Clone(fn.name)
	a.functions[i] = fn
	return nil
}

func complexityLanguage(path string) (language, grammarFile string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go", ""
	case ".py":
		return "python", "source.py"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript", "source.js"
	case ".ts":
		return "typescript", "source.ts"
	case ".tsx":
		return "typescript", "source.tsx"
	}
	return "", ""
}

func (a *complexityAnalysis) scan(path, filter string) error {
	return a.sourceScan.scan(path, 0, func(file string, root bool) (bool, error) {
		language, _ := complexityLanguage(file)
		if root && filter != "" {
			return true, nil
		}
		if root && language == "" {
			return false, fmt.Errorf("unsupported source file %s", file)
		}
		return language != "" && (filter == "" || language == filter), nil
	}, func(file, source string) error {
		language, grammar := complexityLanguage(file)
		if file == path && filter != "" {
			language = filter
			switch filter {
			case "python":
				grammar = "source.py"
			case "javascript":
				grammar = "source.js"
			case "typescript":
				if grammar != "source.tsx" {
					grammar = "source.ts"
				}
			}
		}
		if language == "go" {
			return a.goFile(file, source)
		}
		return a.treeFile(file, source, grammar)
	})
}

func (a *complexityAnalysis) goFile(path, source string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if err != nil || a.ctx.Err() != nil {
			return false
		}
		var body *ast.BlockStmt
		name := ""
		switch fn := node.(type) {
		case *ast.FuncDecl:
			body = fn.Body
			name = fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				receiver := fn.Recv.List[0].Type
				for receiver != nil {
					switch r := receiver.(type) {
					case *ast.StarExpr:
						receiver = r.X
					case *ast.IndexExpr:
						receiver = r.X
					case *ast.IndexListExpr:
						receiver = r.X
					case *ast.Ident:
						name = r.Name + "." + name
						receiver = nil
					default:
						receiver = nil
					}
				}
			}
		case *ast.FuncLit:
			body = fn.Body
			name = "<anonymous>"
		default:
			return true
		}
		if body == nil {
			return true
		}
		fn := complexityFunction{name: name, file: path, line: fset.Position(node.Pos()).Line, complexity: 1}
		ast.Inspect(body, func(inner ast.Node) bool {
			if a.ctx.Err() != nil {
				return false
			}
			switch n := inner.(type) {
			case *ast.FuncLit:
				return false
			case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
				fn.complexity++
			case *ast.CaseClause:
				if n.List != nil {
					fn.complexity++
				}
			case *ast.CommClause:
				if n.Comm != nil {
					fn.complexity++
				}
			case *ast.BinaryExpr:
				if n.Op == token.LAND || n.Op == token.LOR {
					fn.complexity++
				}
			}
			return true
		})
		err = a.record(fn)
		return err == nil
	})
	if err != nil {
		return err
	}
	return a.ctx.Err()
}

func complexityTreeFunction(kind string) bool {
	switch kind {
	case "function_definition", "function_declaration", "generator_function_declaration", "function_expression", "generator_function", "arrow_function", "method_definition", "lambda":
		return true
	}
	return false
}

func (a *complexityAnalysis) treeFile(path, source, grammar string) error {
	language := grammars.DetectLanguage(grammar).Language()
	tree, err := parseFunctionTree(newFunctionParser(language), source)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Release()
	var visit func(*gotreesitter.Node, *complexityFunction, int) error
	visit = func(node *gotreesitter.Node, owner *complexityFunction, depth int) error {
		if err := a.ctx.Err(); err != nil {
			return err
		}
		if depth > 512 {
			return fmt.Errorf("syntax depth exceeds 512 in %s", path)
		}
		if node.IsMissing() || node.IsError() {
			return fmt.Errorf("parse %s: incomplete or invalid syntax at line %d", path, node.StartPoint().Row+1)
		}
		kind := node.Type(language)
		if grammar == "source.py" && kind == "block" {
			hasStatement := false
			for i := 0; i < node.NamedChildCount(); i++ {
				if node.NamedChild(i).Type(language) != "comment" {
					hasStatement = true
					break
				}
			}
			if !hasStatement {
				return fmt.Errorf("parse %s: empty Python suite at line %d", path, node.StartPoint().Row+1)
			}
		}
		if complexityTreeFunction(kind) {
			body := node.ChildByFieldName("body", language)
			if body == nil {
				return fmt.Errorf("parse %s: missing function body at line %d", path, node.StartPoint().Row+1)
			}
			name, err := boundedTreeFunctionName(node, language, source)
			if err != nil {
				return err
			}
			fn := complexityFunction{name: name, file: path, line: int(node.StartPoint().Row) + 1, complexity: 1}
			for i := 0; i < node.ChildCount(); i++ {
				child := node.Child(i)
				var active *complexityFunction
				if child == body {
					active = &fn
				}
				if err := visit(child, active, depth+1); err != nil {
					return err
				}
			}
			return a.record(fn)
		}
		if owner != nil {
			switch kind {
			case "if_statement", "elif_clause", "if_clause", "for_statement", "for_in_statement", "while_statement", "do_statement", "except_clause", "except_group_clause", "catch_clause", "switch_case", "conditional_expression", "ternary_expression", "boolean_operator", "for_in_clause", "assert_statement", "with_statement":
				owner.complexity++
			case "case_clause":
				for i := 0; i < node.NamedChildCount(); i++ {
					pattern := node.NamedChild(i)
					if pattern.Type(language) == "case_pattern" && source[pattern.StartByte():pattern.EndByte()] != "_" {
						owner.complexity++
						break
					}
				}
			case "binary_expression":
				if operator := node.ChildByFieldName("operator", language); operator != nil {
					switch source[operator.StartByte():operator.EndByte()] {
					case "&&", "||", "??":
						owner.complexity++
					}
				}
			}
		}
		for i := 0; i < node.ChildCount(); i++ {
			if err := visit(node.Child(i), owner, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(tree.RootNode(), nil, 0)
}
