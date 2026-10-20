package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultReferenceScanBytes    = 64 << 20
	DefaultReferenceOutputBytes  = 8 << 10
	DefaultReferenceMatches      = 50
	DefaultReferenceExcerptBytes = 256
	DefaultReferenceSearchTime   = 20 * time.Second
)

// FindReferencesTool finds bounded textual candidates, not resolved bindings.
type FindReferencesTool struct{ workDirAware }

func (t *FindReferencesTool) Name() string { return "find_references" }
func (t *FindReferencesTool) Description() string {
	return "Find literal, case-sensitive whole-word textual candidates, not resolved bindings. Includes comments/strings. Count is matching lines; returns bounded excerpts, byte columns, total/returned counts, and truncation. Excludes named function/method/type declaration names in Go, JS/TS, Python unless include_definition=true. Other files use text matching. Scans UTF-8 text; skips binaries, directory symlinks and dependency dirs (.git/node_modules/vendor/.venv/venv/__pycache__); includes other hidden/ignored files. Budget/syntax/scan failures are errors. Results sort by file/line; narrow path if truncated."
}
func (t *FindReferencesTool) Parameters() ParameterSchema {
	return ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"symbol":             {Type: "string", Description: "Literal single-line symbol or qualified spelling, at most 1024 bytes; not a regex."},
		"path":               {Type: "string", Description: "File or directory to search (default current directory).", Default: "."},
		"include_definition": {Type: "boolean", Description: "Include named function/method and type declaration names (default false). Set true for text-only searches, including incomplete source.", Default: false},
	}, Required: []string{"symbol"}}
}
func (t *FindReferencesTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *FindReferencesTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	symbol, ok := params["symbol"].(string)
	if !ok || symbol == "" || len(symbol) > 1024 || !utf8.ValidString(symbol) || strings.ContainsAny(symbol, "\r\n\x00") {
		return &Result{Error: "symbol must be non-empty UTF-8 single-line text of at most 1024 bytes"}, nil
	}
	path := "."
	if value := params["path"]; value != nil {
		var ok bool
		path, ok = value.(string)
		if !ok || strings.TrimSpace(path) == "" {
			return &Result{Error: "path must be a non-empty string"}, nil
		}
	}
	includeDefinitions := false
	if value := params["include_definition"]; value != nil {
		var ok bool
		includeDefinitions, ok = value.(bool)
		if !ok {
			return &Result{Error: "include_definition must be a boolean"}, nil
		}
	}
	path, err := resolvePath(t.workDir, path)
	if err != nil {
		return referenceFailure(err), nil
	}
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	if t.maxExecTime <= 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, DefaultReferenceSearchTime)
		defer timeoutCancel()
	}
	found := referenceMatches{symbol: symbol}
	skipped := 0
	scan := sourceScan{ctx: ctx, remaining: DefaultReferenceScanBytes, fileLimit: t.mutationFileLimit()}
	err = scan.scan(path, 0, func(string, bool) (bool, error) { return true, nil }, func(file, source string) error {
		if strings.ContainsRune(source, 0) || !utf8.ValidString(source) {
			skipped++
			return nil
		}
		if !strings.Contains(source, symbol) {
			return nil
		}
		var definitions []referenceSpan
		if !includeDefinitions {
			var err error
			definitions, err = referenceDefinitions(ctx, file, source)
			if err != nil {
				return err
			}
		}
		return found.file(ctx, file, source, definitions)
	})
	if err != nil {
		return referenceFailure(err), nil
	}
	data := map[string]any{"symbol": strings.Clone(symbol), "path": strings.Clone(path), "kind": "textual_candidates", "include_definition": includeDefinitions, "count": found.count, "files_scanned": scan.files, "binary_files_skipped": skipped}
	budget := DefaultReferenceOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	codec := toon.New(true)
	for keep := len(found.rows); ; keep /= 2 {
		rows := make([]map[string]any, 0, keep)
		for _, row := range found.rows[:keep] {
			rows = append(rows, map[string]any{"file": row.file, "line": row.line, "column": row.column, "content": row.content, "content_bytes": row.bytes, "content_start_column": row.startColumn, "content_truncated": len(row.content) < row.bytes})
		}
		data["references"], data["returned"], data["truncated"] = rows, keep, keep < found.count
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
			return referenceFailure(err), nil
		}
		if max(len(encoded), len(text)) <= budget {
			return result, nil
		}
		if keep <= 1 {
			return &Result{Error: fmt.Sprintf("reference result exceeds %d-byte output budget; narrow the path", budget)}, nil
		}
	}
}
func referenceFailure(err error) *Result {
	message := err.Error()
	if len(message) > 512 {
		message = strings.ToValidUTF8(message[:512], "�") + "... (diagnostic truncated)"
	}
	return &Result{Error: "reference search failed: " + message}
}

type referenceSpan struct{ start, end int }
type referenceRow struct {
	file, content                    string
	line, column, bytes, startColumn int
}
type referenceMatches struct {
	symbol string
	count  int
	rows   []referenceRow
}

func referenceWord(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}
func (a *referenceMatches) file(ctx context.Context, file, source string, definitions []referenceSpan) error {
	line, offset := 1, 0
	for offset < len(source) {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := len(source)
		if newline := strings.IndexByte(source[offset:], '\n'); newline >= 0 {
			end = offset + newline
		}
		text := source[offset:end]
		for from := 0; from <= len(text)-len(a.symbol); {
			if err := ctx.Err(); err != nil {
				return err
			}
			at := strings.Index(text[from:], a.symbol)
			if at < 0 {
				break
			}
			at += from
			from = at + len(a.symbol)
			if at > 0 {
				r, _ := utf8.DecodeLastRuneInString(text[:at])
				if referenceWord(r) {
					continue
				}
			}
			if from < len(text) {
				r, _ := utf8.DecodeRuneInString(text[from:])
				if referenceWord(r) {
					continue
				}
			}
			start := offset + at
			i := sort.Search(len(definitions), func(i int) bool { return definitions[i].end > start })
			if i < len(definitions) && definitions[i].start <= start && definitions[i].end >= start+len(a.symbol) {
				continue
			}
			a.count++
			a.keep(file, line, at+1, text)
			break // The public count is matching lines, not token occurrences.
		}
		offset, line = end+1, line+1
	}
	return nil
}
func (a *referenceMatches) keep(file string, line, column int, text string) {
	at := sort.Search(len(a.rows), func(i int) bool {
		return a.rows[i].file > file || (a.rows[i].file == file && a.rows[i].line >= line)
	})
	if at == DefaultReferenceMatches {
		return
	}
	if len(a.rows) < DefaultReferenceMatches {
		a.rows = append(a.rows, referenceRow{})
	}
	copy(a.rows[at+1:], a.rows[at:len(a.rows)-1])
	start := max(0, min(column-1-DefaultReferenceExcerptBytes/4, len(text)-DefaultReferenceExcerptBytes))
	for start > 0 && !utf8.RuneStart(text[start]) {
		start++
	}
	content := strings.Clone(functionSignaturePrefix(text[start:], min(len(text)-start, DefaultReferenceExcerptBytes)))
	a.rows[at] = referenceRow{file: strings.Clone(file), line: line, column: column, content: content, bytes: len(text), startColumn: start + 1}
}

// Only declaration-name spans are filtered; same-line references remain candidates.
func referenceDefinitions(ctx context.Context, file, source string) ([]referenceSpan, error) {
	var spans []referenceSpan
	add := func(start, end int) error {
		if len(spans) >= 100000 {
			return fmt.Errorf("declaration budget exceeded; narrow the path")
		}
		spans = append(spans, referenceSpan{start, end})
		return nil
	}
	language, grammar := complexityLanguage(file)
	if language == "go" {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, source, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w; include_definition true searches incomplete source as text", file, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if err != nil || ctx.Err() != nil {
				return false
			}
			var name *ast.Ident
			switch node := node.(type) {
			case *ast.FuncDecl:
				name = node.Name
			case *ast.TypeSpec:
				name = node.Name
			case *ast.Field:
				if _, ok := node.Type.(*ast.FuncType); ok {
					for _, name := range node.Names {
						if err = add(fset.Position(name.Pos()).Offset, fset.Position(name.End()).Offset); err != nil {
							return false
						}
					}
				}
			}
			if name != nil {
				err = add(fset.Position(name.Pos()).Offset, fset.Position(name.End()).Offset)
			}
			return err == nil
		})
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	} else if language != "" {
		lang := grammars.DetectLanguage(grammar).Language()
		tree, err := parseFunctionTree(newFunctionParser(lang), source)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w; include_definition true searches incomplete source as text", file, err)
		}
		defer tree.Release()
		addName := func(name *gotreesitter.Node) error {
			if name == nil {
				return nil
			}
			switch name.Type(lang) {
			case "member_expression":
				name = name.ChildByFieldName("property", lang)
			case "attribute":
				name = name.ChildByFieldName("attribute", lang)
			}
			if name == nil {
				return nil
			}
			switch name.Type(lang) {
			case "identifier", "type_identifier", "property_identifier", "private_property_identifier", "string":
				return add(int(name.StartByte()), int(name.EndByte()))
			}
			return nil
		}
		err = walkDeclarationTree(ctx, tree.RootNode(), lang, language == "python", func(node *gotreesitter.Node) error {
			switch node.Type(lang) {
			case "class_definition", "class_declaration", "abstract_class_declaration", "class", "interface_declaration", "type_alias_declaration", "enum_declaration":
			case "type_alias_statement":
				name := node.NamedChild(0)
				for name != nil && name.Type(lang) != "identifier" && name.NamedChildCount() > 0 {
					name = name.NamedChild(0)
				}
				if name != nil {
					return add(int(name.StartByte()), int(name.EndByte()))
				}
				return nil
			default:
				if !complexityTreeFunction(node.Type(lang)) && !isJSSignature(node, lang) {
					return nil
				}
			}
			if err := addName(node.ChildByFieldName("name", lang)); err != nil {
				return err
			}
			if parent := node.Parent(); parent != nil {
				for parent != nil && (parent.Type(lang) == "parenthesized_expression" || parent.Type(lang) == "as_expression" || parent.Type(lang) == "satisfies_expression") {
					parent = parent.Parent()
				}
				if parent != nil {
					field := ""
					switch parent.Type(lang) {
					case "variable_declarator":
						field = "name"
					case "pair":
						field = "key"
					case "assignment", "assignment_expression":
						field = "left"
					}
					if field != "" {
						return addName(parent.ChildByFieldName(field, lang))
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end < spans[j].end
	})
	// Nested declarations can share a name (e.g. a binding and a named expression).
	merged := spans[:0]
	for _, span := range spans {
		if len(merged) > 0 && span.start <= merged[len(merged)-1].end {
			merged[len(merged)-1].end = max(merged[len(merged)-1].end, span.end)
		} else {
			merged = append(merged, span)
		}
	}
	return merged, nil
}
