package builtin

import (
	"fmt"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type pythonDocumentationEdit struct {
	docstring, prefix, suffix string
	start, end, line          int
	existing                  bool
}

func newPythonDocumentationParser() *gotreesitter.Parser {
	parser := gotreesitter.NewParser(grammars.PythonLanguage())
	parser.SetTimeoutMicros(1_000_000)
	return parser
}

func parsePythonDocumentation(parser *gotreesitter.Parser, content string) (*gotreesitter.Tree, error) {
	tree, err := parser.ParseStrict([]byte(content))
	if err != nil {
		if tree != nil {
			tree.Release()
		}
		return nil, fmt.Errorf("parse Python source: %w", err)
	}
	if tree.RootNode() == nil || tree.RootNode().HasError() {
		tree.Release()
		return nil, fmt.Errorf("Python source is invalid or incomplete")
	}
	return tree, nil
}

func (t *GenerateDocstringTool) pythonDocstringEdit(parser *gotreesitter.Parser, content, functionName, description string) (pythonDocumentationEdit, error) {
	edit := pythonDocumentationEdit{}
	tree, err := parsePythonDocumentation(parser, content)
	if err != nil {
		return edit, err
	}
	defer tree.Release()
	language := grammars.PythonLanguage()
	target, err := findNamedTreeFunction(tree.RootNode(), language, content, functionName)
	if err != nil {
		return edit, err
	}
	body := target.ChildByFieldName("body", language)
	if body == nil || body.NamedChildCount() == 0 {
		return edit, fmt.Errorf("function %s has no body", functionName)
	}
	for i := 0; i < body.NamedChildCount(); i++ {
		first := body.NamedChild(i)
		if first.Type(language) == "comment" {
			continue
		}
		if pythonDocLiteral(first, language, content) {
			edit.existing = true
			edit.line = int(first.StartPoint().Row) + 1
			return edit, nil
		}
		break
	}
	var colon *gotreesitter.Node
	for i := 0; i < target.ChildCount(); i++ {
		if child := target.Child(i); child.Type(language) == ":" {
			colon = child
			break
		}
	}
	if colon == nil {
		return edit, fmt.Errorf("function %s has no suite separator", functionName)
	}
	lineStart := strings.LastIndexByte(content[:body.StartByte()], '\n') + 1
	indent := content[lineStart:body.StartByte()]
	newline := "\n"
	if i := strings.IndexByte(content[target.StartByte():], '\n'); i > 0 && content[int(target.StartByte())+i-1] == '\r' {
		newline = "\r\n"
	}
	if body.StartPoint().Row == colon.EndPoint().Row {
		definitionStart := strings.LastIndexByte(content[:target.StartByte()], '\n') + 1
		base := content[definitionStart:target.StartByte()]
		unit := "    "
		if strings.Contains(base, "\t") {
			unit = "\t"
		}
		indent = base + unit
		edit.start = int(colon.EndByte())
		edit.end = int(body.StartByte())
		edit.prefix = newline
		edit.suffix = newline + indent
	} else {
		if strings.Trim(indent, " \t") != "" {
			return edit, fmt.Errorf("unsupported Python suite indentation")
		}
		edit.start = lineStart
		edit.end = lineStart
		edit.suffix = newline
	}
	edit.line = strings.Count(content[:edit.start], "\n") + 1
	if edit.prefix != "" {
		edit.line++
	}
	doc := generatedText{limit: t.mutationFileLimit()}
	doc.append(indent, "\"\"\"", newline)
	if description == "" {
		description = "TODO: add description"
	}
	for line := range strings.SplitSeq(description, "\n") {
		doc.append(indent)
		appendPythonDocText(&doc, line)
		doc.append(newline)
		if doc.err != nil {
			return edit, doc.err
		}
	}
	parameters := target.ChildByFieldName("parameters", language)
	first := true
	for i := 0; i < parameters.NamedChildCount(); i++ {
		parameter := parameters.NamedChild(i)
		name := parameter.ChildByFieldName("name", language)
		if name == nil {
			name = parameter
			for name != nil && name.Type(language) != "identifier" {
				name = name.NamedChild(0)
			}
		}
		if name == nil || name.Type(language) != "identifier" {
			continue
		}
		label := content[name.StartByte():name.EndByte()]
		if label == "self" || label == "cls" {
			continue
		}
		if first {
			doc.append(indent, newline, indent, "Args:", newline)
			first = false
		}
		if !doc.append(indent, "    ", label, ": TODO: describe parameter", newline) {
			return edit, doc.err
		}
	}
	doc.append(indent, newline, indent, "Returns:", newline, indent, "    TODO: describe return value", newline, indent, "\"\"\"")
	edit.docstring, err = doc.result()
	return edit, err
}

func pythonDocLiteral(node *gotreesitter.Node, language *gotreesitter.Language, content string) bool {
	switch node.Type(language) {
	case "expression_statement", "parenthesized_expression", "concatenated_string":
		count := 0
		for i := 0; i < node.NamedChildCount(); i++ {
			child := node.NamedChild(i)
			if child.Type(language) == "comment" {
				continue
			}
			if !pythonDocLiteral(child, language, content) {
				return false
			}
			count++
		}
		return count > 0 && (node.Type(language) == "concatenated_string" || count == 1)
	case "string":
		start := node.NamedChild(0)
		if start == nil || start.Type(language) != "string_start" {
			return false
		}
		prefix := content[start.StartByte():start.EndByte()]
		return !strings.ContainsAny(strings.ToLower(prefix), "fbt")
	}
	return false
}

func appendPythonDocText(doc *generatedText, text string) {
	for len(text) > 0 {
		i := strings.IndexAny(text, "\\\"\r\x00")
		if i < 0 {
			doc.append(text)
			return
		}
		if !doc.append(text[:i]) {
			return
		}
		escape := "\\" + text[i:i+1]
		if text[i] == '\r' {
			escape = "\\r"
		} else if text[i] == 0 {
			escape = "\\x00"
		}
		if !doc.append(escape) {
			return
		}
		text = text[i+1:]
	}
}
