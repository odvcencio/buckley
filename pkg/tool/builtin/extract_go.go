package builtin

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

func prepareGoExtraction(path, content, code, name string, start, end, insert int) (string, string, error) {
	fail := func(reason string) (string, string, error) {
		return "", "", fmt.Errorf("cannot extract Go statements: %s; use edit_file for this refactor", reason)
	}
	if !token.IsIdentifier(name) || name == "_" || name == "init" {
		return fail("function_name must be a nonblank Go identifier other than init")
	}
	set := token.NewFileSet()
	mode := parser.Mode(0)
	if insert != start {
		mode = parser.ParseComments
	}
	file, err := parser.ParseFile(set, path, content, mode)
	if err != nil {
		return fail(fmt.Sprintf("invalid source: %v", err))
	}
	base := set.File(file.Pos()).Base()
	lo, hi := token.Pos(base+start), token.Pos(base+end)
	var block *ast.BlockStmt
	var containers []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		switch n := node.(type) {
		case *ast.BlockStmt:
			if n.Lbrace < lo && hi <= n.Rbrace {
				block = n
			}
		case *ast.FuncDecl:
			if n.Body != nil && n.Body.Lbrace < lo && hi <= n.Body.Rbrace {
				containers = append(containers, n)
			}
		case *ast.FuncLit:
			if n.Body.Lbrace < lo && hi <= n.Body.Rbrace {
				containers = append(containers, n)
			}
		}
		return true
	})
	if len(containers) == 0 || block == nil {
		return fail("select statements inside one function body")
	}
	local := insert == start
	if !local {
		validInsertion := false
		for _, decl := range file.Decls {
			first := decl.Pos()
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Doc != nil {
					first = d.Doc.Pos()
				}
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				if d.Doc != nil {
					first = d.Doc.Pos()
				}
			}
			offset := int(first) - base
			if insert <= offset && goExtractionTrivia(content[insert:offset]) {
				validInsertion = true
				break
			}
		}
		if !validInsertion {
			return fail("an earlier insertion must precede a top-level declaration and its documentation")
		}
	}

	var problem string
	ast.Inspect(file, func(node ast.Node) bool {
		id, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == name {
			problem = "function_name is already used in the source"
			return false
		}
		if id.Obj == nil {
			return true
		}
		decl, ok := id.Obj.Decl.(ast.Node)
		if !ok {
			return true
		}
		declaredInside := lo <= decl.Pos() && decl.Pos() < hi
		usedInside := lo <= id.Pos() && id.Pos() < hi
		if declaredInside && !usedInside {
			problem = "a declaration in the selection is used outside it"
		}
		if !local && usedInside && !declaredInside {
			for _, parent := range containers {
				if parent.Pos() <= decl.Pos() && decl.Pos() < parent.End() {
					problem = "a top-level helper cannot capture an outer local"
				}
			}
		}
		return true
	})
	if problem != "" {
		return fail(problem)
	}
	var firstStatement, lastStatement ast.Stmt
	for _, statement := range block.List {
		if lo <= statement.Pos() && statement.End() <= hi {
			if firstStatement == nil {
				firstStatement = statement
			}
			lastStatement = statement
		}
	}
	if firstStatement == nil || !goExtractionTrivia(content[start:int(firstStatement.Pos())-base]) || !goExtractionTrivia(content[int(lastStatement.End())-base:end]) {
		return fail("select complete statements from one block")
	}

	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		if node.End() <= lo || node.Pos() >= hi {
			return false
		}
		if node.Pos() < lo || node.End() > hi {
			return true
		}
		switch n := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt, *ast.DeferStmt, *ast.BranchStmt:
			problem = "return, defer, and branch statements need an explicit refactor"
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "recover" {
				problem = "recover must remain in its original function"
			}
		}
		return true
	})
	if problem != "" {
		return fail(problem)
	}
	first, _, _ := strings.Cut(code, "\n")
	indent := (&ExtractFunctionTool{}).detectIndentation(first)
	function := fmt.Sprintf("func %s() {\n%s\n}", name, code)
	validation := function
	if local {
		function = fmt.Sprintf("%s%s := func() {\n%s\n%s}", indent, name, code, indent)
		validation = "func container(){\n" + function + "\n}"
	}
	if _, err := parser.ParseFile(token.NewFileSet(), path, "package p\n"+validation, parser.SkipObjectResolution); err != nil {
		return fail(fmt.Sprintf("generated helper is invalid: %v", err))
	}
	return function, name + "()", nil
}

func goExtractionTrivia(content string) bool {
	if strings.Trim(content, " \t\r\n") == "" {
		return true
	}
	var scan scanner.Scanner
	file := token.NewFileSet().AddFile("selection", -1, len(content))
	scan.Init(file, []byte(content), nil, scanner.ScanComments)
	for {
		_, kind, _ := scan.Scan()
		switch kind {
		case token.EOF:
			return scan.ErrorCount == 0
		case token.COMMENT, token.SEMICOLON:
		default:
			return false
		}
	}
}
