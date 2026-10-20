package builtin

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

type documentationEdit struct {
	docstring, prefix, suffix string
	start, end, line          int
	existing                  bool
	syntaxDigest              [32]byte
}

func (t *GenerateDocstringTool) goDocstringEdit(content, name, description string) (documentationEdit, error) {
	edit := documentationEdit{}
	fset := token.NewFileSet()
	source, err := parser.ParseFile(fset, "", content, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return edit, fmt.Errorf("parse Go source: %w", err)
	}
	fn, err := findNamedGoFunction(source, name)
	if err != nil {
		return edit, err
	}
	offset := fset.Position(fn.Pos()).Offset
	edit.line = fset.Position(fn.Pos()).Line
	if fn.Doc != nil {
		edit.existing = true
		edit.line = fset.Position(fn.Doc.Pos()).Line
		return edit, nil
	}
	indent, newline := commentPlacement(content, offset, &edit)
	if description == "" {
		description = fn.Name.Name + " TODO: add description"
	}
	doc := generatedText{limit: t.mutationFileLimit()}
	for line := range strings.SplitSeq(description, "\n") {
		if constraint.IsPlusBuild("// " + line) {
			return edit, fmt.Errorf("description would create a Go build constraint; rephrase the +build line")
		}
		if !doc.append(indent, "// ", line, newline) {
			return edit, doc.err
		}
	}
	first := true
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			if first {
				doc.append(indent, "//", newline)
				first = false
			}
			if !doc.append(indent, "// ", name.Name, ": TODO: describe parameter", newline) {
				return edit, doc.err
			}
		}
	}
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		doc.append(indent, "//", newline, indent, "// Returns: TODO: describe return value", newline)
	}
	text, err := doc.result()
	edit.docstring = strings.TrimSuffix(text, newline)
	return edit, err
}

func commentPlacement(content string, offset int, edit *documentationEdit) (string, string) {
	lineStart := strings.LastIndexByte(content[:offset], '\n') + 1
	indent := content[lineStart:offset]
	newline := "\n"
	if i := strings.IndexByte(content[offset:], '\n'); i > 0 && content[offset+i-1] == '\r' {
		newline = "\r\n"
	}
	edit.start = lineStart
	edit.end = lineStart
	edit.suffix = newline
	if strings.Trim(indent, " \t\r") != "" {
		edit.start = offset
		edit.end = offset
		edit.prefix = newline
		edit.line++
		indent = ""
	}
	return indent, newline
}

func (t *GenerateDocstringTool) jsDocstringEdit(p *gotreesitter.Parser, language *gotreesitter.Language, content, name, description string) (documentationEdit, error) {
	edit := documentationEdit{}
	if existing, ok := existingPreambleDocumentation(p, language, content, name); ok {
		return existing, nil
	}
	tree, err := parseFunctionTree(p, content)
	if err != nil {
		return edit, err
	}
	defer tree.Release()
	fn, err := findNamedTreeFunction(tree.RootNode(), language, content, name)
	if errors.Is(err, errNamedFunctionNotFound) {
		fn, err = findJSSignature(tree.RootNode(), language, content, name)
	}
	if err != nil {
		return edit, err
	}
	anchor, parameterSource, err := jsDocumentationAnchor(fn, language, content)
	if err != nil {
		return edit, err
	}
	edit.line = int(anchor.StartPoint().Row) + 1
	var existing *gotreesitter.Node
	var previousEnd uint32
	gotreesitter.Walk(tree.RootNode(), func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if node.Type(language) != "comment" || node.EndByte() > anchor.StartByte() {
			return gotreesitter.WalkContinue
		}
		if strings.TrimSpace(content[previousEnd:node.StartByte()]) != "" {
			existing = nil
		}
		if strings.HasPrefix(content[node.StartByte():node.EndByte()], "/**") {
			existing = node
		}
		previousEnd = node.EndByte()
		return gotreesitter.WalkContinue
	})
	if existing != nil && strings.TrimSpace(content[previousEnd:anchor.StartByte()]) == "" {
		edit.existing = true
		edit.line = int(existing.StartPoint().Row) + 1
		return edit, nil
	}
	indent, newline := commentPlacement(content, int(anchor.StartByte()), &edit)
	edit.syntaxDigest = documentationSyntaxDigest(tree.RootNode(), language, content)
	doc := generatedText{limit: t.mutationFileLimit()}
	doc.append(indent, "/**", newline)
	if description == "" {
		description = "TODO: add description"
	}
	for line := range strings.SplitSeq(description, "\n") {
		doc.append(indent, " * ")
		for len(line) > 0 {
			i := strings.IndexAny(line, "@*")
			if i < 0 {
				if !doc.append(line) {
					return edit, doc.err
				}
				break
			}
			if !doc.append(line[:i]) {
				return edit, doc.err
			}
			if line[i] == '@' {
				if !doc.append("&#64;") {
					return edit, doc.err
				}
				line = line[i+1:]
			} else if i+1 < len(line) && line[i+1] == '/' {
				if !doc.append("* /") {
					return edit, doc.err
				}
				line = line[i+2:]
			} else {
				if !doc.append("*") {
					return edit, doc.err
				}
				line = line[i+1:]
			}
		}
		if !doc.append(newline) {
			return edit, doc.err
		}
	}
	params := parameterSource.ChildByFieldName("parameters", language)
	if params == nil {
		params = parameterSource.ChildByFieldName("parameter", language)
	}
	first := true
	if params != nil {
		count := params.NamedChildCount()
		if params.Type(language) == "identifier" {
			count = 1
		}
		for i := 0; i < count; i++ {
			param := params
			if params.Type(language) != "identifier" {
				param = params.NamedChild(i)
			}
			for param != nil && param.Type(language) != "identifier" {
				switch param.Type(language) {
				case "assignment_pattern":
					param = param.ChildByFieldName("left", language)
				case "required_parameter", "optional_parameter":
					param = param.ChildByFieldName("pattern", language)
				case "rest_pattern":
					param = param.NamedChild(0)
				default:
					param = nil
				}
			}
			if param == nil {
				continue
			}
			if first {
				doc.append(indent, " *", newline)
				first = false
			}
			if !doc.append(indent, " * @param ", content[param.StartByte():param.EndByte()], " - TODO: describe parameter", newline) {
				return edit, doc.err
			}
		}
	}
	doc.append(indent, " * @returns TODO: describe return value", newline, indent, " */")
	edit.docstring, err = doc.result()
	return edit, err
}

func jsDocumentationAnchor(fn *gotreesitter.Node, language *gotreesitter.Language, content string) (*gotreesitter.Node, *gotreesitter.Node, error) {
	anchor := fn
	switch fn.Type(language) {
	case "arrow_function", "function_expression", "generator_function":
		parent := fn.Parent()
		for parent != nil && (parent.Type(language) == "parenthesized_expression" || parent.Type(language) == "as_expression" || parent.Type(language) == "satisfies_expression") {
			parent = parent.Parent()
		}
		if parent == nil {
			return nil, nil, fmt.Errorf("function has no documentation declaration")
		}
		switch parent.Type(language) {
		case "variable_declarator":
			anchor = parent
			declaration := parent.Parent()
			count := 0
			for i := 0; i < declaration.NamedChildCount(); i++ {
				if declaration.NamedChild(i).Type(language) == "variable_declarator" {
					count++
				}
			}
			if count == 1 {
				anchor = declaration
			}
		case "pair":
			anchor = parent
		case "assignment_expression":
			anchor = parent.Parent()
			if anchor == nil || anchor.Type(language) != "expression_statement" {
				return nil, nil, fmt.Errorf("split the function assignment into its own statement before documenting it")
			}
		default:
			return nil, nil, fmt.Errorf("function needs a declaration or property binding for documentation")
		}
	}
	anchor = jsDocumentationWrapper(anchor, language)
	parameters := fn
	if fn.Type(language) == "function_declaration" || fn.Type(language) == "method_definition" || isJSSignature(fn, language) {
		name := treeFunctionName(fn, language, content)
		for previous := anchor.PrevSibling(); previous != nil; previous = previous.PrevSibling() {
			if previous.Type(language) == "comment" || previous.Type(language) == ";" {
				continue
			}
			signature := jsSignatureDeclaration(previous, language)
			if signature == nil || treeFunctionName(signature, language, content) != name {
				break
			}
			anchor = previous
			parameters = signature
		}
	}
	return anchor, parameters, nil
}

func documentationSyntaxDigest(root *gotreesitter.Node, language *gotreesitter.Language, content string) [32]byte {
	hash := sha256.New()
	var walk func(*gotreesitter.Node)
	walk = func(node *gotreesitter.Node) {
		if node.Type(language) == "comment" {
			return
		}
		hash.Write([]byte(node.Type(language)))
		hash.Write([]byte{0})
		if node.ChildCount() == 0 {
			hash.Write([]byte(content[node.StartByte():node.EndByte()]))
			hash.Write([]byte{0})
		}
		for i := 0; i < node.ChildCount(); i++ {
			walk(node.Child(i))
		}
		hash.Write([]byte{0xff})
	}
	walk(root)
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func jsDocumentationWrapper(node *gotreesitter.Node, language *gotreesitter.Language) *gotreesitter.Node {
	for parent := node.Parent(); parent != nil && (parent.Type(language) == "export_statement" || parent.Type(language) == "ambient_declaration"); parent = node.Parent() {
		node = parent
	}
	return node
}

func isJSSignature(node *gotreesitter.Node, language *gotreesitter.Language) bool {
	switch node.Type(language) {
	case "function_signature", "method_signature", "abstract_method_signature":
		return true
	}
	return false
}

func jsSignatureDeclaration(node *gotreesitter.Node, language *gotreesitter.Language) *gotreesitter.Node {
	if isJSSignature(node, language) {
		return node
	}
	if node.Type(language) != "export_statement" && node.Type(language) != "ambient_declaration" {
		return nil
	}
	for i := 0; i < node.NamedChildCount(); i++ {
		if found := jsSignatureDeclaration(node.NamedChild(i), language); found != nil {
			return found
		}
	}
	return nil
}

func findJSSignature(root *gotreesitter.Node, language *gotreesitter.Language, content, target string) (*gotreesitter.Node, error) {
	var selected *gotreesitter.Node
	fullName := ""
	ambiguous := false
	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if !isJSSignature(node, language) {
			return gotreesitter.WalkContinue
		}
		candidate := treeFunctionName(node, language, content)
		qualified := qualifiedTreeFunctionName(node, language, content)
		if strings.Contains(target, ".") {
			candidate = qualified
		}
		if candidate != target {
			return gotreesitter.WalkContinue
		}
		if selected == nil {
			selected = node
			fullName = qualified
		} else if fullName != qualified {
			ambiguous = true
			return gotreesitter.WalkStop
		}
		return gotreesitter.WalkContinue
	})
	if ambiguous {
		return nil, fmt.Errorf("function %s is ambiguous; use a qualified name", target)
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: %s", errNamedFunctionNotFound, target)
	}
	return selected, nil
}
