package builtin

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

var errNamedFunctionNotFound = errors.New("function not found")

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
	p := newFunctionParser(language)
	tree, err := parseFunctionTree(p, content)
	if err != nil {
		return explainSelection{}, err
	}
	defer tree.Release()
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
	selected, err := findNamedGoFunction(source, target)
	if err != nil {
		return explainSelection{}, err
	}
	return explainSelection{fset.Position(selected.Pos()).Offset, fset.Position(selected.End()).Offset}, nil
}

func findNamedGoFunction(source *ast.File, target string) (*ast.FuncDecl, error) {
	var selected *ast.FuncDecl
	for _, declaration := range source.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if strings.Contains(target, ".") {
			name = qualifiedGoFunctionName(fn)
		}
		if name != target {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("function %s is ambiguous; use a qualified name such as Class.method", target)
		}
		selected = fn
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: %s", errNamedFunctionNotFound, target)
	}
	return selected, nil
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
			name = qualifiedTreeFunctionName(node, language, content)
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
		return nil, fmt.Errorf("%w: %s", errNamedFunctionNotFound, target)
	}
	if matches > 1 {
		return nil, fmt.Errorf("function %s is ambiguous; use a qualified name such as Class.method", target)
	}
	return selected, nil
}

func treeFunctionName(node *gotreesitter.Node, language *gotreesitter.Language, content string) string {
	switch node.Type(language) {
	case "type_alias_statement":
		name := node.NamedChild(0)
		for name != nil && name.Type(language) != "identifier" {
			name = name.NamedChild(0)
		}
		if name != nil {
			return content[name.StartByte():name.EndByte()]
		}
	case "function_expression", "generator_function", "arrow_function", "lambda", "object", "class":
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
			case "assignment":
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

func newFunctionParser(language *gotreesitter.Language) *gotreesitter.Parser {
	p := gotreesitter.NewParser(language)
	p.SetTimeoutMicros(1_000_000)
	return p
}

func parseFunctionTree(p *gotreesitter.Parser, content string) (*gotreesitter.Tree, error) {
	tree, err := p.ParseStrict([]byte(content))
	if err != nil {
		if tree != nil {
			tree.Release()
		}
		return nil, fmt.Errorf("parse function source: %w", err)
	}
	if tree.RootNode() == nil || tree.RootNode().HasError() {
		tree.Release()
		return nil, fmt.Errorf("function source is invalid or incomplete")
	}
	return tree, nil
}

func qualifiedTreeFunctionName(node *gotreesitter.Node, language *gotreesitter.Language, content string) string {
	name := treeFunctionName(node, language, content)
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type(language) {
		case "function_definition", "class_definition", "function_declaration", "generator_function_declaration", "function_expression", "generator_function", "arrow_function", "method_definition", "class_declaration", "abstract_class_declaration", "class", "object", "internal_module", "interface_declaration", "type_alias_declaration":
			if prefix := treeFunctionName(parent, language, content); prefix != "" {
				name = prefix + "." + name
			}
		}
	}
	return name
}

const maxQualifiedFunctionBytes = 8 << 10

func boundedTreeFunctionName(node *gotreesitter.Node, language *gotreesitter.Language, source string) (string, error) {
	parts := []string{}
	length := 0
	for current := node; current != nil; current = current.Parent() {
		kind := current.Type(language)
		if current != node && !complexityTreeFunction(kind) {
			switch kind {
			case "class_definition", "class_declaration", "abstract_class_declaration", "class", "object", "internal_module", "interface_declaration", "type_alias_declaration", "type_alias_statement":
			default:
				continue
			}
		}
		name := treeFunctionName(current, language, source)
		if name == "" && current == node {
			name = "<anonymous>"
		}
		if name != "" {
			length += len(name) + 1
			if length > maxQualifiedFunctionBytes {
				return "", fmt.Errorf("qualified function name exceeds %d-byte output budget", maxQualifiedFunctionBytes)
			}
			parts = append(parts, name)
		}
	}
	slices.Reverse(parts)
	return strings.Join(parts, "."), nil
}

func qualifiedGoFunctionName(fn *ast.FuncDecl) string {
	name := fn.Name.Name
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return name
	}
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
			return node.Name + "." + name
		default:
			return name
		}
	}
	return name
}

func walkDeclarationTree(ctx context.Context, root *gotreesitter.Node, language *gotreesitter.Language, python bool, visit func(*gotreesitter.Node) error) error {
	var walk func(*gotreesitter.Node, int) error
	walk = func(node *gotreesitter.Node, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 512 {
			return fmt.Errorf("declaration syntax depth exceeds 512")
		}
		if node.IsMissing() || node.IsError() {
			return fmt.Errorf("invalid declaration syntax at line %d", node.StartPoint().Row+1)
		}
		if python && node.Type(language) == "block" {
			hasStatement := false
			for i := 0; i < node.NamedChildCount(); i++ {
				if node.NamedChild(i).Type(language) != "comment" {
					hasStatement = true
					break
				}
			}
			if !hasStatement {
				return fmt.Errorf("empty Python suite at line %d", node.StartPoint().Row+1)
			}
		}
		if err := visit(node); err != nil {
			return err
		}
		for i := 0; i < node.ChildCount(); i++ {
			if err := walk(node.Child(i), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, 0)
}
