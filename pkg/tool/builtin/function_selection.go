package builtin

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type explainSelection struct{ start, end int }

func selectExplainFunction(content, target, file string) (explainSelection, error) {
	if filepath.Ext(file) == ".go" {
		return selectExplainGoFunction(content, target, file)
	}
	entry := grammars.DetectLanguage(file)
	if entry == nil {
		return explainSelection{}, fmt.Errorf("unsupported function selection for %s; select lines instead", filepath.Ext(file))
	}
	switch entry.Name {
	case "python", "javascript", "typescript", "tsx":
	default:
		return explainSelection{}, fmt.Errorf("unsupported function selection for %s; select lines instead", filepath.Ext(file))
	}
	language := entry.Language()
	p := gotreesitter.NewParser(language)
	p.SetTimeoutMicros(1_000_000)
	tree, err := p.ParseStrict([]byte(content))
	if tree != nil {
		defer tree.Release()
	}
	if err != nil {
		return explainSelection{}, fmt.Errorf("parse function source: %w", err)
	}
	if tree.RootNode() == nil || tree.RootNode().HasError() {
		return explainSelection{}, fmt.Errorf("function source is invalid or incomplete; select lines to inspect a fragment")
	}
	node, err := findNamedTreeFunction(tree.RootNode(), language, content, target)
	if err != nil {
		return explainSelection{}, err
	}
	return explainSelection{int(node.StartByte()), int(node.EndByte())}, nil
}

func selectExplainGoFunction(content, target, file string) (explainSelection, error) {
	fset := token.NewFileSet()
	source, err := parser.ParseFile(fset, file, content, parser.SkipObjectResolution)
	if err != nil {
		return explainSelection{}, fmt.Errorf("parse Go function source: %w", err)
	}
	var selected *ast.FuncDecl
	for _, declaration := range source.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if strings.Contains(target, ".") && fn.Recv != nil && len(fn.Recv.List) > 0 {
			receiver := fn.Recv.List[0].Type
			for receiver != nil {
				switch node := receiver.(type) {
				case *ast.StarExpr:
					receiver = node.X
				case *ast.IndexExpr:
					receiver = node.X
				case *ast.IndexListExpr:
					receiver = node.X
				case *ast.Ident:
					name = node.Name + "." + name
					receiver = nil
				default:
					receiver = nil
				}
			}
		}
		if name != target {
			continue
		}
		if selected != nil {
			return explainSelection{}, fmt.Errorf("function %s is ambiguous; use a qualified name such as Class.method", target)
		}
		selected = fn
	}
	if selected == nil {
		return explainSelection{}, fmt.Errorf("function %s not found", target)
	}
	return explainSelection{fset.Position(selected.Pos()).Offset, fset.Position(selected.End()).Offset}, nil
}

func findNamedTreeFunction(root *gotreesitter.Node, language *gotreesitter.Language, content, target string) (*gotreesitter.Node, error) {
	var selected *gotreesitter.Node
	matches := 0
	qualified := strings.Contains(target, ".")
	leaf := target[strings.LastIndexByte(target, '.')+1:]
	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		switch node.Type(language) {
		case "function_definition", "function_declaration", "generator_function_declaration", "function_expression", "generator_function", "arrow_function", "method_definition":
		default:
			return gotreesitter.WalkContinue
		}
		name := treeFunctionName(node, language, content)
		if name == "" || (name != leaf && name != target) {
			return gotreesitter.WalkContinue
		}
		if qualified {
			for parent := node.Parent(); parent != nil; parent = parent.Parent() {
				switch parent.Type(language) {
				case "function_definition", "class_definition", "function_declaration", "generator_function_declaration", "function_expression", "generator_function", "arrow_function", "method_definition", "class_declaration", "class", "object", "internal_module":
					if prefix := treeFunctionName(parent, language, content); prefix != "" {
						name = prefix + "." + name
					}
				}
			}
		}
		if name == target {
			selected = node
			matches++
			if matches > 1 {
				return gotreesitter.WalkStop
			}
		}
		return gotreesitter.WalkContinue
	})
	if matches == 0 {
		return nil, fmt.Errorf("function %s not found", target)
	}
	if matches > 1 {
		return nil, fmt.Errorf("function %s is ambiguous; use a qualified name such as Class.method", target)
	}
	return selected, nil
}

func treeFunctionName(node *gotreesitter.Node, language *gotreesitter.Language, content string) string {
	switch node.Type(language) {
	case "function_expression", "generator_function", "arrow_function", "object", "class":
		parent := node.Parent()
		for parent != nil && (parent.Type(language) == "parenthesized_expression" || parent.Type(language) == "as_expression" || parent.Type(language) == "satisfies_expression") {
			parent = parent.Parent()
		}
		if parent != nil {
			var binding *gotreesitter.Node
			switch parent.Type(language) {
			case "variable_declarator":
				binding = parent.ChildByFieldName("name", language)
			case "pair":
				binding = parent.ChildByFieldName("key", language)
			case "assignment_expression":
				binding = parent.ChildByFieldName("left", language)
			}
			if binding != nil {
				return content[binding.StartByte():binding.EndByte()]
			}
		}
	}
	if name := node.ChildByFieldName("name", language); name != nil {
		return content[name.StartByte():name.EndByte()]
	}
	return ""
}
