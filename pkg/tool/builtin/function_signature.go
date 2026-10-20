package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"strings"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultFunctionSignatureBytes     = 8 << 10
	DefaultFunctionSignatureScanBytes = 64 << 20
)

// GetFunctionSignatureTool returns a parsed declaration header and attached documentation source.
type GetFunctionSignatureTool struct{ workDirAware }

func (t *GetFunctionSignatureTool) Name() string { return "get_function_signature" }
func (t *GetFunctionSignatureTool) Description() string {
	return "Get a Go, JS/TS, or Python function/method declaration header and attached documentation source. Accepts qualified names such as Service.add and a file or directory path. Rejects ambiguous matches; narrow the path or qualify the name. Bodies are excluded. Overload declarations are included when adjacent. Signature and documentation prefixes have explicit truncation and byte counts; use read_file for more. Bounded scans skip dependencies and directory-entry symlinks."
}
func (t *GetFunctionSignatureTool) Parameters() ParameterSchema {
	return ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"function": {Type: "string", Description: "Exact function or qualified method name (maximum 1024 bytes)."},
		"path":     {Type: "string", Description: "Source file or directory to search (default current directory). Omit or use null for default.", Default: ".", RawSchema: map[string]any{"type": []string{"string", "null"}, "description": "Source file or directory to search (default current directory). Omit or use null for default.", "default": "."}},
	}, Required: []string{"function"}}
}
func (t *GetFunctionSignatureTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *GetFunctionSignatureTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	name, ok := params["function"].(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" || len(name) > 1024 {
		return &Result{Error: "function parameter must be a non-empty name of at most 1024 bytes"}, nil
	}
	path := "."
	if value := params["path"]; value != nil {
		var ok bool
		path, ok = value.(string)
		if !ok {
			return &Result{Error: "path parameter must be a string"}, nil
		}
		if strings.TrimSpace(path) == "" {
			path = "."
		}
	}
	path, err := resolvePath(t.workDir, path)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	scan := sourceScan{ctx: ctx, remaining: DefaultFunctionSignatureScanBytes, fileLimit: t.mutationFileLimit()}
	var found *functionSignature
	leaf := name[strings.LastIndexByte(name, '.')+1:]
	err = scan.scan(path, 0, func(file string, root bool) (bool, error) {
		language, _ := complexityLanguage(file)
		if root && language == "" {
			return false, fmt.Errorf("unsupported function source %s", file)
		}
		return language != "", nil
	}, func(file, source string) error {
		if !strings.Contains(source, leaf) {
			return nil
		}
		candidate, err := parsedFunctionSignature(ctx, file, source, name)
		if errors.Is(err, errNamedFunctionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if found != nil {
			return fmt.Errorf("function %s is ambiguous at %s:%d and %s:%d; narrow path or use a qualified name", name, found.file, found.line, candidate.file, candidate.line)
		}
		found = &candidate
		return nil
	})
	if err != nil {
		message := err.Error()
		if len(message) > 512 {
			message = strings.ToValidUTF8(message[:512], "�") + "... (diagnostic truncated)"
		}
		return &Result{Error: "function signature failed: " + message}, nil
	}
	if found == nil {
		return &Result{Error: fmt.Sprintf("function %s not found in the scanned Go, JS/TS, or Python sources", name)}, nil
	}
	data := map[string]any{"function": strings.Clone(name), "file": strings.Clone(found.file), "line": found.line, "signature_bytes": len(found.signature), "docs_bytes": len(found.docs)}
	budget := DefaultFunctionSignatureBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	signatureBytes, docsBytes := min(len(found.signature), budget), 0
	codec := toon.New(true)
	encode := func() (int, error) {
		data["signature"] = strings.Clone(functionSignaturePrefix(found.signature, signatureBytes))
		data["docs"] = strings.Clone(functionSignaturePrefix(found.docs, docsBytes))
		data["signature_returned_bytes"] = len(data["signature"].(string))
		data["docs_returned_bytes"] = len(data["docs"].(string))
		data["signature_truncated"] = len(data["signature"].(string)) < len(found.signature)
		data["docs_truncated"] = len(data["docs"].(string)) < len(found.docs)
		result := &Result{Success: true, Data: data}
		encoded, err := json.Marshal(result)
		if err != nil {
			return 0, err
		}
		text, err := codec.MarshalString(result)
		return max(len(encoded), len(text)), err
	}
	var baseBytes int
	for {
		size, err := encode()
		if err != nil {
			return nil, err
		}
		if size <= budget {
			baseBytes = size
			break
		}
		if signatureBytes == 0 {
			return &Result{Error: fmt.Sprintf("signature metadata exceeds %d-byte output budget; narrow the path", budget)}, nil
		}
		signatureBytes /= 2
	}
	docsBytes = min(len(found.docs), budget-baseBytes)
	for {
		size, err := encode()
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return &Result{Error: fmt.Sprintf("function signature canceled: %v", err)}, nil
		}
		if size <= budget {
			return &Result{Success: true, Data: data}, nil
		}
		docsBytes /= 2
	}
}
func functionSignaturePrefix(text string, size int) string {
	for size > 0 && size < len(text) && !utf8.RuneStart(text[size]) {
		size--
	}
	return text[:size]
}

type functionSignature struct {
	file, signature, docs string
	line                  int
}

func parsedFunctionSignature(ctx context.Context, file, source, name string) (functionSignature, error) {
	language, grammar := complexityLanguage(file)
	if language == "go" {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, source, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return functionSignature{}, fmt.Errorf("parse %s: %w", file, err)
		}
		fn, err := findNamedGoFunction(parsed, name)
		if err != nil {
			return functionSignature{}, err
		}
		start, end := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
		if fn.Body != nil {
			end = fset.Position(fn.Body.Lbrace).Offset
		}
		docs := ""
		if fn.Doc != nil {
			docs = source[fset.Position(fn.Doc.Pos()).Offset:fset.Position(fn.Doc.End()).Offset]
		}
		return functionSignature{file: file, line: fset.Position(fn.Pos()).Line, signature: strings.TrimSpace(source[start:end]), docs: docs}, ctx.Err()
	}
	lang := grammars.DetectLanguage(grammar).Language()
	tree, err := parseFunctionTree(newFunctionParser(lang), source)
	if err != nil {
		return functionSignature{}, fmt.Errorf("parse %s: %w", file, err)
	}
	defer tree.Release()
	fn, err := signatureTreeFunction(ctx, tree.RootNode(), lang, source, name, language == "python")
	if err != nil {
		return functionSignature{}, err
	}
	body := fn.ChildByFieldName("body", lang)
	start, end := int(fn.StartByte()), int(fn.EndByte())
	if body != nil {
		end = int(body.StartByte())
	}
	docs := ""
	if language == "python" {
		if body == nil {
			return functionSignature{}, fmt.Errorf("Python function has no body")
		}
		for i := 0; i < body.NamedChildCount(); i++ {
			first := body.NamedChild(i)
			if first.Type(lang) == "comment" {
				continue
			}
			if pythonDocLiteral(first, lang, source) {
				docs = source[first.StartByte():first.EndByte()]
			}
			break
		}
	} else {
		anchor, parameters, err := jsDocumentationAnchor(fn, lang, source)
		if err != nil {
			return functionSignature{}, err
		}
		if isJSSignature(parameters, lang) {
			start = int(anchor.StartByte())
		}
		if body == nil {
			full := qualifiedTreeFunctionName(fn, lang, source)
			for next := anchor.NextSibling(); next != nil; next = next.NextSibling() {
				if next.Type(lang) == "comment" || next.Type(lang) == ";" {
					continue
				}
				signature := jsSignatureDeclaration(next, lang)
				if signature == nil || qualifiedTreeFunctionName(signature, lang, source) != full {
					break
				}
				end = int(signature.EndByte())
			}
		}
		boundary := anchor.StartByte()
		for previous := anchor.PrevSibling(); previous != nil && previous.Type(lang) == "comment"; previous = previous.PrevSibling() {
			if strings.TrimSpace(source[previous.EndByte():boundary]) != "" {
				break
			}
			if strings.HasPrefix(source[previous.StartByte():previous.EndByte()], "/**") {
				docs = source[previous.StartByte():previous.EndByte()]
				break
			}
			boundary = previous.StartByte()
		}
	}
	return functionSignature{file: file, line: strings.Count(source[:start], "\n") + 1, signature: strings.TrimSpace(source[start:end]), docs: docs}, ctx.Err()
}

func signatureTreeFunction(ctx context.Context, root *gotreesitter.Node, language *gotreesitter.Language, source, target string, python bool) (*gotreesitter.Node, error) {
	var selected, declaration *gotreesitter.Node
	qualified := ""
	leaf := target[strings.LastIndexByte(target, '.')+1:]
	var visit func(*gotreesitter.Node, int) error
	visit = func(node *gotreesitter.Node, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 512 {
			return fmt.Errorf("function syntax depth exceeds 512")
		}
		if node.IsMissing() || node.IsError() {
			return fmt.Errorf("invalid function syntax at line %d", node.StartPoint().Row+1)
		}
		kind := node.Type(language)
		if python && kind == "block" {
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
		signature := isJSSignature(node, language)
		if signature || complexityTreeFunction(kind) {
			candidate := treeFunctionName(node, language, source)
			if candidate == leaf || candidate == target {
				full, err := boundedTreeFunctionName(node, language, source)
				if err != nil {
					return err
				}
				if !strings.Contains(target, ".") || full == target {
					if qualified != "" && qualified != full {
						return fmt.Errorf("function %s is ambiguous; use a qualified name", target)
					}
					qualified = full
					if signature {
						if declaration == nil {
							declaration = node
						}
					} else {
						if selected != nil {
							return fmt.Errorf("function %s is ambiguous; narrow the path", target)
						}
						selected = node
					}
				}
			}
		}
		for i := 0; i < node.ChildCount(); i++ {
			if err := visit(node.Child(i), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, 0); err != nil {
		return nil, err
	}
	if selected != nil {
		return selected, nil
	}
	if declaration != nil {
		return declaration, nil
	}
	return nil, fmt.Errorf("%w: %s", errNamedFunctionNotFound, target)
}
