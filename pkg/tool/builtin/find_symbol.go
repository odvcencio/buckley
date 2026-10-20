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
	"time"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"m31labs.dev/buckley/pkg/encoding/toon"
	"m31labs.dev/buckley/pkg/storage"
)

const (
	DefaultSymbolOutputBytes    = 8 << 10
	DefaultSymbolMatches        = 20
	DefaultSymbolSignatureBytes = 256
	DefaultSymbolScanBytes      = 64 << 20
	DefaultSymbolSearchTime     = 20 * time.Second
)

// FindSymbolTool finds literal declaration names in live source or an indexed snapshot.
type FindSymbolTool struct {
	workDirAware
	Store *storage.Store
}

func (t *FindSymbolTool) Name() string { return "find_symbol" }
func (t *FindSymbolTool) Description() string {
	return "Find exact function/method, type, class, or interface declarations in Go, JS/TS, or Python. Accepts qualified names such as Service.add; names are literal. type includes all type declarations; class and interface select those kinds specifically. source auto uses matching indexed snapshot records when available, otherwise live parsing; use live for current source or index for snapshot-only lookup. Returns bounded signatures and locations, total/returned counts, and truncation. Budgets or search failures are errors, not clean scans."
}
func (t *FindSymbolTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"symbol": {Type: "string", Description: "Exact name or qualified name; maximum 1024 bytes."},
		"type":   {Type: "string", Description: "Optional function, type, class, or interface filter. Omit or use null for all supported kinds."},
		"path":   {Type: "string", Description: "Literal file or directory scope (default current directory).", Default: "."},
		"source": {Type: "string", Description: "auto (default), live, or index. Indexed results are a snapshot, not a freshness guarantee.", Default: "auto"},
	}, Required: []string{"symbol"}}
	for _, name := range []string{"type", "path", "source"} {
		p := schema.Properties[name]
		p.RawSchema = map[string]any{"type": []string{"string", "null"}, "description": p.Description}
		if p.Default != nil {
			p.RawSchema["default"] = p.Default
		}
		schema.Properties[name] = p
	}
	return schema
}
func (t *FindSymbolTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *FindSymbolTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	if t.maxExecTime <= 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, DefaultSymbolSearchTime)
		defer timeoutCancel()
	}
	symbol, ok := params["symbol"].(string)
	symbol = strings.TrimSpace(symbol)
	if !ok || symbol == "" || len(symbol) > 1024 {
		return &Result{Error: "symbol parameter must be a non-empty name of at most 1024 bytes"}, nil
	}
	values := map[string]string{"path": ".", "source": "auto"}
	for _, field := range []string{"type", "path", "source"} {
		if value := params[field]; value != nil {
			text, ok := value.(string)
			if !ok {
				return &Result{Error: field + " parameter must be a string"}, nil
			}
			if text != "" {
				values[field] = text
			}
		}
	}
	filter, mode := values["type"], values["source"]
	if filter != "" && filter != "function" && filter != "type" && filter != "class" && filter != "interface" {
		return &Result{Error: "unsupported symbol type"}, nil
	}
	if mode != "auto" && mode != "live" && mode != "index" {
		return &Result{Error: "source must be auto, live, or index"}, nil
	}
	path, err := resolvePath(t.workDir, values["path"])
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	found := symbolMatches{query: symbol, filter: filter, matches: make([]symbolMatch, 0, DefaultSymbolMatches)}
	source := "live"
	if mode != "live" && t.Store != nil {
		err = t.indexedSymbols(ctx, path, &found)
		if err != nil {
			return symbolLookupFailure(err), nil
		}
		if found.count > 0 || mode == "index" {
			source = "index"
		}
	} else if mode == "index" {
		return &Result{Error: "symbol index is unavailable; use source live"}, nil
	}
	if source == "live" {
		scan := sourceScan{ctx: ctx, remaining: DefaultSymbolScanBytes, fileLimit: t.mutationFileLimit()}
		err = scan.scan(path, 0, func(file string, root bool) (bool, error) {
			language, _ := complexityLanguage(file)
			if root && language == "" {
				return false, fmt.Errorf("unsupported symbol source %s", file)
			}
			return language != "", nil
		}, func(file, content string) error {
			leaf := symbol[strings.LastIndexByte(symbol, '.')+1:]
			if !strings.Contains(content, leaf) {
				return nil
			}
			if !utf8.ValidString(content) {
				return fmt.Errorf("symbol source must be UTF-8 text")
			}
			language, grammar := complexityLanguage(file)
			if language == "go" {
				return found.goFile(ctx, file, content)
			}
			return found.treeFile(ctx, file, content, grammar, language == "python")
		})
		if err != nil {
			return symbolLookupFailure(err), nil
		}
	}
	data := map[string]any{"symbol": strings.Clone(symbol), "type": filter, "path": strings.Clone(path), "source": source, "snapshot": source == "index", "count": found.count}
	budget := DefaultSymbolOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	codec := toon.New(true)
	for keep := len(found.matches); ; keep /= 2 {
		rows := make([]map[string]any, 0, keep)
		for _, match := range found.matches[:keep] {
			rows = append(rows, map[string]any{"name": match.name, "kind": match.kind, "file": match.file, "line": match.line, "signature": match.signature, "signature_bytes": match.signatureBytes, "signature_truncated": len(match.signature) < match.signatureBytes})
		}
		data["matches"] = rows
		data["returned"] = keep
		data["truncated"] = keep < found.count
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
			return symbolLookupFailure(err), nil
		}
		if max(len(encoded), len(text)) <= budget {
			return result, nil
		}
		if keep <= 1 {
			return &Result{Error: fmt.Sprintf("symbol result exceeds %d-byte output budget; narrow the path", budget)}, nil
		}
	}
}
func symbolLookupFailure(err error) *Result {
	message := err.Error()
	if len(message) > 512 {
		message = strings.ToValidUTF8(message[:512], "�") + "... (diagnostic truncated)"
	}
	return &Result{Error: "symbol lookup failed: " + message}
}
func symbolKinds(filter string) []string {
	switch filter {
	case "function":
		return []string{"function", "func", "method"}
	case "class":
		return []string{"class"}
	case "interface":
		return []string{"interface"}
	case "type":
		return []string{"type", "struct", "alias", "type_alias", "enum", "class", "interface"}
	}
	return []string{"function", "func", "method", "type", "struct", "alias", "type_alias", "enum", "class", "interface"}
}
func canonicalSymbolKind(kind string) string {
	switch strings.ToLower(kind) {
	case "function", "func", "method":
		return "function"
	case "class":
		return "class"
	case "interface":
		return "interface"
	default:
		return "type"
	}
}
func (t *FindSymbolTool) indexedSymbols(parent context.Context, path string, found *symbolMatches) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	paths := []string{filepath.ToSlash(path)}
	base, err := filepath.Abs(t.workDir)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(base, path)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		paths = append(paths, filepath.ToSlash(relative))
	}
	records, total, err := t.Store.FindSymbolDefinitions(ctx, found.query, symbolKinds(found.filter), paths, DefaultSymbolMatches)
	if err != nil {
		return fmt.Errorf("index query: %w; use source live to retry current files", err)
	}
	for _, rec := range records {
		file := filepath.FromSlash(rec.FilePath)
		if !filepath.IsAbs(file) {
			file = filepath.Join(base, file)
		}
		file, err = resolvePath(t.workDir, file)
		if err != nil {
			return fmt.Errorf("indexed path escapes the workspace; rebuild the index or use source live")
		}
		found.keep(symbolMatch{name: rec.Name, kind: canonicalSymbolKind(rec.Kind), file: file, line: rec.StartLine, signature: rec.Signature, signatureBytes: rec.SignatureBytes})
	}
	found.count = total
	return nil
}

type symbolMatch struct {
	name, kind, file, signature string
	line, signatureBytes        int
}
type symbolMatches struct {
	query, filter string
	count         int
	matches       []symbolMatch
}

func (a *symbolMatches) wanted(name, kind string) bool {
	if a.filter != "" && kind != a.filter && !(a.filter == "type" && kind != "function") {
		return false
	}
	if strings.Contains(a.query, ".") {
		return name == a.query
	}
	return name[strings.LastIndexByte(name, '.')+1:] == a.query
}
func (a *symbolMatches) keep(match symbolMatch) {
	at := sort.Search(len(a.matches), func(i int) bool {
		other := a.matches[i]
		if match.file != other.file {
			return match.file < other.file
		}
		if match.line != other.line {
			return match.line < other.line
		}
		if match.name != other.name {
			return match.name < other.name
		}
		if match.kind != other.kind {
			return match.kind < other.kind
		}
		return match.signature < other.signature
	})
	if at == DefaultSymbolMatches {
		return
	}
	if len(a.matches) < DefaultSymbolMatches {
		a.matches = append(a.matches, symbolMatch{})
	}
	copy(a.matches[at+1:], a.matches[at:len(a.matches)-1])
	match.name = strings.Clone(match.name)
	match.file = strings.Clone(match.file)
	match.signature = strings.Clone(functionSignaturePrefix(match.signature, min(len(match.signature), DefaultSymbolSignatureBytes)))
	a.matches[at] = match
}
func (a *symbolMatches) record(name, kind, file, header string, line int) error {
	if !a.wanted(name, kind) {
		return nil
	}
	if len(name) > maxQualifiedFunctionBytes {
		return fmt.Errorf("qualified symbol name exceeds metadata budget")
	}
	a.count++
	a.keep(symbolMatch{name: name, kind: kind, file: file, line: line, signature: header, signatureBytes: len(header)})
	return nil
}
func (a *symbolMatches) goFile(ctx context.Context, file, source string) error {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, source, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		if err != nil || ctx.Err() != nil {
			return false
		}
		switch node := node.(type) {
		case *ast.FuncDecl:
			if node.Name.Name != a.query[strings.LastIndexByte(a.query, '.')+1:] {
				return true
			}
			end := fset.Position(node.End()).Offset
			if node.Body != nil {
				end = fset.Position(node.Body.Lbrace).Offset
			}
			err = a.record(qualifiedGoFunctionName(node), "function", file, strings.TrimSpace(source[fset.Position(node.Pos()).Offset:end]), fset.Position(node.Pos()).Line)
		case *ast.TypeSpec:
			kind := "type"
			end := fset.Position(node.End()).Offset
			switch declared := node.Type.(type) {
			case *ast.StructType:
				end = fset.Position(declared.Fields.Opening).Offset
			case *ast.InterfaceType:
				kind = "interface"
				end = fset.Position(declared.Methods.Opening).Offset
				for _, field := range declared.Methods.List {
					if _, ok := field.Type.(*ast.FuncType); !ok {
						continue
					}
					for _, name := range field.Names {
						if name.Name != a.query[strings.LastIndexByte(a.query, '.')+1:] || (a.filter != "" && a.filter != "function") {
							continue
						}
						qualified := node.Name.Name + "." + name.Name
						if a.wanted(qualified, "function") {
							if e := a.record(qualified, "function", file, strings.TrimSpace(source[fset.Position(field.Pos()).Offset:fset.Position(field.End()).Offset]), fset.Position(field.Pos()).Line); e != nil {
								err = e
								return false
							}
						}
					}
				}
			}
			if a.wanted(node.Name.Name, kind) {
				header := strings.TrimSpace(source[fset.Position(node.Pos()).Offset:end])
				prefix := "type " + functionSignaturePrefix(header, min(len(header), DefaultSymbolSignatureBytes-5))
				a.count++
				a.keep(symbolMatch{name: node.Name.Name, kind: kind, file: file, line: fset.Position(node.Pos()).Line, signature: prefix, signatureBytes: 5 + len(header)})
			}
		}
		return err == nil
	})
	if err != nil {
		return err
	}
	return ctx.Err()
}
func (a *symbolMatches) treeFile(ctx context.Context, file, source, grammar string, python bool) error {
	language := grammars.DetectLanguage(grammar).Language()
	tree, err := parseFunctionTree(newFunctionParser(language), source)
	if err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	defer tree.Release()
	return walkDeclarationTree(ctx, tree.RootNode(), language, python, func(node *gotreesitter.Node) error {
		kind := ""
		switch node.Type(language) {
		case "class_definition", "class_declaration", "abstract_class_declaration", "class":
			kind = "class"
		case "interface_declaration":
			kind = "interface"
		case "type_alias_declaration", "enum_declaration":
			kind = "type"
		default:
			if complexityTreeFunction(node.Type(language)) || isJSSignature(node, language) {
				kind = "function"
			}
		}
		if kind == "" {
			return nil
		}
		if a.filter != "" && kind != a.filter && !(a.filter == "type" && kind != "function") {
			return nil
		}
		raw := treeFunctionName(node, language, source)
		if raw == "" {
			return nil
		}
		if raw[strings.LastIndexByte(raw, '.')+1:] != a.query[strings.LastIndexByte(a.query, '.')+1:] {
			return nil
		}
		name, err := boundedTreeFunctionName(node, language, source)
		if err != nil {
			return err
		}
		if !a.wanted(name, kind) {
			return nil
		}
		end := node.EndByte()
		if body := node.ChildByFieldName("body", language); body != nil {
			end = body.StartByte()
		}
		return a.record(name, kind, file, strings.TrimSpace(source[node.StartByte():end]), int(node.StartPoint().Row)+1)
	})
}
