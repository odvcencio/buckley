package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPostEditDiagnostics_BuildsContainingPackage(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.com/test\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatalf("creating module: %v", err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "broken.go"), []byte("package test\n\nfunc broken( {\n"), 0644); err != nil {
		t.Fatalf("creating root package with an error: %v", err)
	}

	packageDir := filepath.Join(moduleDir, "nested")
	if err := os.Mkdir(packageDir, 0755); err != nil {
		t.Fatalf("creating nested package: %v", err)
	}
	goFile := filepath.Join(packageDir, "valid.go")
	if err := os.WriteFile(goFile, []byte("package nested\n\nfunc Valid() {}\n"), 0644); err != nil {
		t.Fatalf("creating nested package source: %v", err)
	}

	if got := postEditDiagnostics(goFile); got != "" {
		t.Fatalf("postEditDiagnostics() = %q, want no diagnostics from the valid nested package", got)
	}
}
