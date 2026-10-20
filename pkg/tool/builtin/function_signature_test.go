package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestGetFunctionSignatureTool_ConfiguredTimeout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "add.go"), "package p\n// Add returns the total.\nfunc Add(a, b int) int { return a + b }\n")
	tool := &GetFunctionSignatureTool{}
	tool.SetWorkDir(dir)
	tool.SetMaxExecTimeSeconds(1)
	result, err := tool.Execute(map[string]any{"function": "Add", "path": "add.go"})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Data["signature"] != "func Add(a, b int) int" || !strings.Contains(result.Data["docs"].(string), "Add returns the total.") {
		t.Fatalf("incorrect signature or documentation: %+v", result.Data)
	}
}

func BenchmarkGetFunctionSignatureTool_SmallGo(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "target.go")
	if err := os.WriteFile(path, []byte("package p\n// Target returns the value.\nfunc Target(x int) int { return x }\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	tool := &GetFunctionSignatureTool{}
	params := map[string]any{"function": "Target", "path": dir}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := tool.Execute(params)
		if err != nil || !result.Success {
			b.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestGetFunctionSignatureTool_ParsedHeadersAndDocs(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct{ file, name, source, header, docs string }{
		{"service.go", "Service.Add", "package p\ntype Service[T any] struct{}\n// Add returns the total.\nfunc (s *Service[T]) Add(\n a T,\n b T,\n) (T, error) { return a, nil }\n", "func (s *Service[T]) Add(\n a T,\n b T,\n) (T, error)", "// Add returns the total."},
		{"service.py", "Service.add", "class Service:\n    async def add(\n        self,\n        a: int,\n        b: int,\n    ) -> int:\n        \"\"\"Return the total asynchronously.\"\"\"\n        return a + b\n", "async def add(\n        self,\n        a: int,\n        b: int,\n    ) -> int:", "\"\"\"Return the total asynchronously.\"\"\""},
		{"arrow.ts", "scale", "/** Scale the input. */\nexport const scale = (n: number): number => n * 2;", "(n: number): number =>", "/** Scale the input. */"},
		{"object.js", "util.add", "const util = {\n /** Add values. */\n add: function(a, b) { return a + b; }\n};", "function(a, b)", "/** Add values. */"},
		{"assignment.js", "exports.add", "/** Add exported values. */\nexports.add = function(a, b) { return a + b; };", "function(a, b)", "/** Add exported values. */"},
		{"reader.ts", "Reader.read", "interface Reader {\n /** Read a value. */\n read(a: string): number;\n}", "read(a: string): number", "/** Read a value. */"},
		{"emptydoc.py", "f", "# unrelated comment\ndef f():\n    value = 1\n    \"not a docstring\"\n    return value\n", "def f():", ""},
		{"fstring.py", "f", "def f():\n    f\"not a docstring\"\n    return 1\n", "def f():", ""},
		{"lambda.py", "scale", "scale = lambda x: x * 2\n", "lambda x:", ""},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, tc.file), tc.source)
			tool := &GetFunctionSignatureTool{}
			tool.SetWorkDir(dir)
			tool.SetMaxExecTimeSeconds(2)
			result, err := tool.Execute(map[string]any{"function": tc.name, "path": tc.file})
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			header := result.Data["signature"].(string)
			if header != tc.header || result.Data["docs"] != tc.docs {
				t.Fatalf("header=%q docs=%q, want %q / %q", header, result.Data["docs"], tc.header, tc.docs)
			}
			if result.Data["signature_truncated"] != false || result.Data["docs_truncated"] != false || result.Data["signature_bytes"] != len(tc.header) || result.Data["docs_bytes"] != len(tc.docs) {
				t.Fatalf("bad size metadata: %+v", result.Data)
			}
		})
	}
}

func TestGetFunctionSignatureTool_LargeWorkspace(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 9; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("filler%d.go", i)), "package p\n/*"+strings.Repeat("x", 1<<20)+"*/\n")
	}
	writeFile(t, filepath.Join(dir, "target.go"), "package p\nfunc Target() {}\n")
	tool := &GetFunctionSignatureTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"function": "Target"})
	if err != nil || !result.Success || result.Data["signature"] != "func Target()" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGetFunctionSignatureTool_LongHeaderAndDiagnostic(t *testing.T) {
	dir := t.TempDir()
	header := "func Target(" + strings.Repeat("parameter", 2000) + " int)"
	writeFile(t, filepath.Join(dir, "long.go"), "package p\n"+header+" {}\n")
	tool := &GetFunctionSignatureTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"function": "Target", "path": "long.go"})
	if err != nil || !result.Success || result.Data["signature_truncated"] != true || result.Data["signature_bytes"] != len(header) || !strings.HasPrefix(header, result.Data["signature"].(string)) {
		t.Fatalf("bad header prefix: success=%v err=%v", result.Success, err)
	}
	for _, useToon := range []bool{true, false} {
		encoded, err := toon.New(useToon).MarshalString(result)
		if err != nil || len(encoded) > DefaultFunctionSignatureBytes {
			t.Fatalf("TOON=%v bytes=%d err=%v", useToon, len(encoded), err)
		}
	}
	writeFile(t, filepath.Join(dir, "broken.go"), "package p\n"+strings.Repeat("Target", 100000))
	result, err = tool.Execute(map[string]any{"function": "Target", "path": "broken.go"})
	if err != nil || result.Success || !strings.Contains(result.Error, "parse") {
		t.Fatalf("expected parse failure, success=%v err=%v", result.Success, err)
	}
	for _, useToon := range []bool{true, false} {
		encoded, err := toon.New(useToon).MarshalString(result)
		if err != nil || len(encoded) > DefaultFunctionSignatureBytes {
			t.Fatalf("TOON=%v error bytes=%d err=%v", useToon, len(encoded), err)
		}
	}
}

func TestGetFunctionSignatureTool_Overloads(t *testing.T) {
	for _, source := range []string{
		"/** Echo a value. */\nfunction echo(x: string): string;\nfunction echo(x: number): number;\nfunction echo(x: any) { return x; }\n",
		"/** Echo a value. */\nexport declare function echo(x: string): string;\nexport declare function echo(x: number): number;\n",
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "echo.ts"), source)
		tool := &GetFunctionSignatureTool{}
		tool.SetWorkDir(dir)
		result, err := tool.Execute(map[string]any{"function": "echo", "path": "echo.ts"})
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		header := result.Data["signature"].(string)
		if !strings.Contains(header, "echo(x: string): string") || !strings.Contains(header, "echo(x: number): number") || strings.Contains(header, "return x") || !strings.Contains(result.Data["docs"].(string), "Echo a value") {
			t.Fatalf("wrong overload result: %+v", result.Data)
		}
	}
}

func TestGetFunctionSignatureTool_AmbiguityAndLiteralNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package p\nfunc Add() {}\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package p\nfunc Add() {}\n")
	tool := &GetFunctionSignatureTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"function": "Add"})
	if err != nil || result.Success || !strings.Contains(result.Error, "ambiguous") || !strings.Contains(result.Error, "a.go:") || !strings.Contains(result.Error, "b.go:") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = tool.Execute(map[string]any{"function": "Add", "path": "a.go"})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = tool.Execute(map[string]any{"function": "Add.*"})
	if err != nil || result.Success || !strings.Contains(result.Error, "not found") {
		t.Fatalf("regex name was not literal: result=%+v err=%v", result, err)
	}
	writeFile(t, filepath.Join(dir, "methods.py"), "class A:\n    def add(self): pass\nclass B:\n    def add(self): pass\n")
	result, err = tool.Execute(map[string]any{"function": "add", "path": "methods.py"})
	if err != nil || result.Success || !strings.Contains(result.Error, "ambiguous") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = tool.Execute(map[string]any{"function": "B.add", "path": "methods.py"})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGetFunctionSignatureTool_BoundedOutput(t *testing.T) {
	dir := t.TempDir()
	doc := "/*" + strings.Repeat("λ\"\\\t", 10000) + "*/"
	writeFile(t, filepath.Join(dir, "large.go"), "package p\n"+doc+"\nfunc Target() {}\n")
	tool := &GetFunctionSignatureTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultFunctionSignatureBytes, 2048} {
		tool.SetMaxOutputBytes(budget)
		result, err := tool.Execute(map[string]any{"function": "Target", "path": "large.go"})
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		text := result.Data["docs"].(string)
		if result.Data["signature"] != "func Target()" || result.Data["signature_truncated"] != false || result.Data["docs_bytes"] != len(doc) || result.Data["docs_truncated"] != true || result.Data["docs_returned_bytes"] != len(text) || !strings.HasPrefix(doc, text) || !utf8.ValidString(text) {
			t.Fatalf("bad bounded documentation: %+v", result.Data)
		}
		for _, useToon := range []bool{true, false} {
			encoded, err := toon.New(useToon).MarshalString(result)
			if err != nil || len(encoded) > budget {
				t.Fatalf("TOON=%v bytes=%d budget=%d err=%v", useToon, len(encoded), budget, err)
			}
		}
	}
	tool.SetMaxOutputBytes(20)
	result, err := tool.Execute(map[string]any{"function": "Target", "path": "large.go"})
	if err != nil || result.Success || !strings.Contains(result.Error, "output budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGetFunctionSignatureTool_InputAndCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "source.go"), "package p\nfunc Target() {}\n")
	tool := &GetFunctionSignatureTool{}
	tool.SetWorkDir(dir)
	tool.SetMaxFileSizeBytes(4)
	result, err := tool.Execute(map[string]any{"function": "Target", "path": "source.go"})
	if err != nil || result.Success || !strings.Contains(result.Error, "byte budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = tool.ExecuteWithContext(ctx, map[string]any{"function": "Target", "path": "source.go"})
	if err != nil || result.Success || !strings.Contains(result.Error, "canceled") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	tool.SetMaxFileSizeBytes(0)
	writeFile(t, filepath.Join(dir, "broken.py"), "def Target(:\n    pass\n")
	result, err = tool.Execute(map[string]any{"function": "Target", "path": "broken.py"})
	if err != nil || result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = tool.Execute(map[string]any{"function": json.Number("1")})
	if err != nil || result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
