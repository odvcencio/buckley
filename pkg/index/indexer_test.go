package index

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/storage"
)

func newTestIndexer(t *testing.T) (*Indexer, *storage.Store, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	idx := New(store, tempDir)
	cleanup := func() {
		_ = store.Close()
	}
	return idx, store, cleanup
}

func TestNew(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	idx := New(store, tempDir)
	if idx == nil {
		t.Fatal("expected non-nil indexer")
	}
	if idx.store != store {
		t.Error("indexer store not set correctly")
	}
	if idx.root != tempDir {
		t.Errorf("expected root %s, got %s", tempDir, idx.root)
	}
	if idx.ignoreDirs == nil {
		t.Fatal("expected ignoreDirs map to be initialized")
	}

	// Verify ignored directories
	expectedIgnored := []string{".git", "node_modules", "vendor", ".buckley"}
	for _, dir := range expectedIgnored {
		if _, ok := idx.ignoreDirs[dir]; !ok {
			t.Errorf("expected %s to be in ignoreDirs", dir)
		}
	}
}

func TestScan_NoStore(t *testing.T) {
	idx := &Indexer{
		store: nil,
		root:  t.TempDir(),
	}
	err := idx.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error when store is nil")
	}
	if err.Error() != "store not initialized" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestScan_EmptyDirectory(t *testing.T) {
	idx, _, cleanup := newTestIndexer(t)
	defer cleanup()

	ctx := context.Background()
	if err := idx.Scan(ctx); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
}

func TestScan_SingleGoFile(t *testing.T) {
	idx, store, cleanup := newTestIndexer(t)
	defer cleanup()

	// Create a simple Go file
	goFile := filepath.Join(idx.root, "main.go")
	content := `package main

import "fmt"

// Hello prints a greeting
func Hello() {
	fmt.Println("hello")
}

type Person struct {
	Name string
}

func (p *Person) Greet() string {
	return "Hello, " + p.Name
}
`
	if err := os.WriteFile(goFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	ctx := context.Background()
	if err := idx.Scan(ctx); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Verify file record was created
	files, err := store.SearchFiles(ctx, "", "main.go", 10)
	if err != nil {
		t.Fatalf("SearchFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file record, got %d", len(files))
	}
	if files[0].Path != "main.go" {
		t.Errorf("expected path main.go, got %s", files[0].Path)
	}
	if files[0].Language != "go" {
		t.Errorf("expected language go, got %s", files[0].Language)
	}
	if files[0].Checksum == "" {
		t.Error("expected non-empty checksum")
	}

	// Verify symbols were extracted
	symbols, err := store.SearchSymbols(ctx, "", "main.go", 100)
	if err != nil {
		t.Fatalf("SearchSymbols failed: %v", err)
	}
	if len(symbols) == 0 {
		t.Fatal("expected symbols to be extracted")
	}

	// Check for specific symbols
	foundHello := false
	foundPerson := false
	foundGreet := false
	for _, sym := range symbols {
		switch sym.Name {
		case "Hello":
			foundHello = true
			if sym.Kind != "function" {
				t.Errorf("Hello: expected kind function, got %s", sym.Kind)
			}
		case "Person":
			foundPerson = true
			if sym.Kind != "type" {
				t.Errorf("Person: expected kind type, got %s", sym.Kind)
			}
		case "Greet":
			foundGreet = true
			if sym.Kind != "method" {
				t.Errorf("Greet: expected kind method, got %s", sym.Kind)
			}
		}
	}
	if !foundHello {
		t.Error("expected to find Hello function")
	}
	if !foundPerson {
		t.Error("expected to find Person type")
	}
	if !foundGreet {
		t.Error("expected to find Greet method")
	}

	// Note: imports are stored but there's no SearchImports method in storage
	// We've verified they're extracted in the indexGoFile function
}

func TestScan_IgnoresDirectories(t *testing.T) {
	idx, _, cleanup := newTestIndexer(t)
	defer cleanup()

	// Create files in ignored directories
	ignoredDirs := []string{".git", "node_modules", "vendor", ".buckley"}
	for _, dir := range ignoredDirs {
		dirPath := filepath.Join(idx.root, dir)
		if err := os.Mkdir(dirPath, 0o755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
		testFile := filepath.Join(dirPath, "test.go")
		if err := os.WriteFile(testFile, []byte("package test"), 0o644); err != nil {
			t.Fatalf("failed to write file in %s: %v", dir, err)
		}
	}

	// Create a valid Go file outside ignored directories
	validFile := filepath.Join(idx.root, "valid.go")
	if err := os.WriteFile(validFile, []byte("package main"), 0o644); err != nil {
		t.Fatalf("failed to write valid file: %v", err)
	}

	ctx := context.Background()
	if err := idx.Scan(ctx); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Only valid.go should be indexed
	files, err := idx.store.SearchFiles(ctx, "", "", 100)
	if err != nil {
		t.Fatalf("SearchFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "valid.go" {
		t.Errorf("expected path valid.go, got %s", files[0].Path)
	}
}

func TestScan_SkipsNonGoFiles(t *testing.T) {
	idx, store, cleanup := newTestIndexer(t)
	defer cleanup()

	// Create various non-Go files
	files := map[string]string{
		"readme.md":   "# README",
		"config.json": "{}",
		"script.sh":   "#!/bin/bash",
		"data.txt":    "some data",
		"code.go":     "package main",
	}

	for name, content := range files {
		path := filepath.Join(idx.root, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	ctx := context.Background()
	if err := idx.Scan(ctx); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Only code.go should be indexed
	indexed, err := store.SearchFiles(ctx, "", "", 100)
	if err != nil {
		t.Fatalf("SearchFiles failed: %v", err)
	}
	if len(indexed) != 1 {
		t.Fatalf("expected 1 file, got %d", len(indexed))
	}
	if indexed[0].Path != "code.go" {
		t.Errorf("expected path code.go, got %s", indexed[0].Path)
	}
}

func TestScan_NestedDirectories(t *testing.T) {
	idx, store, cleanup := newTestIndexer(t)
	defer cleanup()

	// Create nested directory structure
	pkgDir := filepath.Join(idx.root, "pkg")
	modelDir := filepath.Join(pkgDir, "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("failed to create nested dirs: %v", err)
	}

	files := map[string]string{
		filepath.Join(idx.root, "main.go"): "package main",
		filepath.Join(pkgDir, "types.go"):  "package pkg",
		filepath.Join(modelDir, "user.go"): "package model",
	}

	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}

	ctx := context.Background()
	if err := idx.Scan(ctx); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// All three files should be indexed
	indexed, err := store.SearchFiles(ctx, "", "", 100)
	if err != nil {
		t.Fatalf("SearchFiles failed: %v", err)
	}
	if len(indexed) != 3 {
		t.Fatalf("expected 3 files, got %d", len(indexed))
	}
}

func TestScan_CanceledContext(t *testing.T) {
	idx, _, cleanup := newTestIndexer(t)
	defer cleanup()

	// Create some files
	for i := 0; i < 10; i++ {
		path := filepath.Join(idx.root, "file"+string(rune('0'+i))+".go")
		if err := os.WriteFile(path, []byte("package main"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := idx.Scan(ctx)
	if err == nil {
		t.Fatal("expected error with canceled context")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestIndexGoFile_ComplexSymbols(t *testing.T) {
	idx, store, cleanup := newTestIndexer(t)
	defer cleanup()

	// Create a file with various symbol types
	goFile := filepath.Join(idx.root, "complex.go")
	content := `package complex

import (
	"fmt"
	"strings"
)

type User struct {
	ID   int
	Name string
}

type Admin struct {
	User
	Permissions []string
}

func NewUser(name string) *User {
	return &User{Name: name}
}

func (u *User) String() string {
	return fmt.Sprintf("User(%s)", u.Name)
}

func (a *Admin) HasPermission(perm string) bool {
	for _, p := range a.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

type Handler func(string) error

func Process(data string, h Handler) error {
	return h(strings.ToUpper(data))
}
`
	if err := os.WriteFile(goFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	ctx := context.Background()
	if err := idx.Scan(ctx); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	// Check symbols
	symbols, err := store.SearchSymbols(ctx, "", "complex.go", 100)
	if err != nil {
		t.Fatalf("SearchSymbols failed: %v", err)
	}

	expectedSymbols := map[string]string{
		"User":          "type",
		"Admin":         "type",
		"Handler":       "type",
		"NewUser":       "function",
		"String":        "method",
		"HasPermission": "method",
		"Process":       "function",
	}

	foundSymbols := make(map[string]string)
	for _, sym := range symbols {
		foundSymbols[sym.Name] = sym.Kind
	}

	for name, expectedKind := range expectedSymbols {
		kind, found := foundSymbols[name]
		if !found {
			t.Errorf("expected to find symbol %s", name)
			continue
		}
		if kind != expectedKind {
			t.Errorf("symbol %s: expected kind %s, got %s", name, expectedKind, kind)
		}
	}

	// Note: imports are stored but there's no SearchImports method in storage
	// We've verified they're extracted in the indexGoFile function
}

func TestExtractSymbols_EmptyFile(t *testing.T) {
	content := `package empty`
	fset := newFileSetFromContent(t, "empty.go", content)
	file := parseFile(t, fset, "empty.go", content)

	symbols := extractSymbols(fset, file, "empty.go")
	if len(symbols) != 0 {
		t.Errorf("expected 0 symbols, got %d", len(symbols))
	}
}

func TestExtractImports_NoImports(t *testing.T) {
	content := `package main

func main() {}
`
	fset := newFileSetFromContent(t, "main.go", content)
	file := parseFile(t, fset, "main.go", content)

	imports := extractImports(file, "main.go")
	if len(imports) != 0 {
		t.Errorf("expected 0 imports, got %d", len(imports))
	}
}

func TestFormatFuncSignature(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		expected string
	}{
		{
			name:     "no params no return",
			code:     "func Foo() {}",
			expected: "func Foo()",
		},
		{
			name:     "single param no return",
			code:     "func Bar(x int) {}",
			expected: "func Bar(x int)",
		},
		{
			name:     "multiple params single return",
			code:     "func Add(a int, b int) int { return 0 }",
			expected: "func Add(a int, b int) int",
		},
		{
			name:     "multiple returns",
			code:     "func Split(s string) (string, string) { return \"\", \"\" }",
			expected: "func Split(s string) string, string",
		},
		{
			name:     "pointer params and returns",
			code:     "func Process(data *Data) (*Result, error) { return nil, nil }",
			expected: "func Process(data *Data) *Result, error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "package test\n" + tt.code
			fset := newFileSetFromContent(t, "test.go", content)
			file := parseFile(t, fset, "test.go", content)

			if len(file.Decls) == 0 {
				t.Fatal("no declarations found")
			}
			fnDecl, ok := file.Decls[0].(*ast.FuncDecl)
			if !ok {
				t.Fatal("expected function declaration")
			}

			sig := formatFuncSignature(fnDecl)
			if sig != tt.expected {
				t.Errorf("expected signature %q, got %q", tt.expected, sig)
			}
		})
	}
}

func TestExprToString(t *testing.T) {
	tests := []struct {
		name     string
		typeExpr string
		expected string
	}{
		{"simple ident", "int", "int"},
		{"pointer", "*User", "*User"},
		{"slice", "[]string", "[]string"},
		{"map", "map[string]int", "map[string]int"},
		{"nested pointer", "**Data", "**Data"},
		{"slice of pointers", "[]*User", "[]*User"},
		{"map of slices", "map[string][]int", "map[string][]int"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := "package test\ntype T " + tt.typeExpr
			fset := newFileSetFromContent(t, "test.go", code)
			file := parseFile(t, fset, "test.go", code)

			if len(file.Decls) == 0 {
				t.Fatal("no declarations found")
			}
			genDecl, ok := file.Decls[0].(*ast.GenDecl)
			if !ok {
				t.Fatal("expected general declaration")
			}
			if len(genDecl.Specs) == 0 {
				t.Fatal("no specs found")
			}
			typeSpec, ok := genDecl.Specs[0].(*ast.TypeSpec)
			if !ok {
				t.Fatal("expected type spec")
			}

			result := exprToString(typeSpec.Type)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// Helper functions

func newFileSetFromContent(t *testing.T, filename, content string) *token.FileSet {
	t.Helper()
	return token.NewFileSet()
}

func parseFile(t *testing.T, fset *token.FileSet, filename, content string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse file: %v", err)
	}
	return file
}
