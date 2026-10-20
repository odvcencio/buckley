package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
	"m31labs.dev/buckley/pkg/storage"
)

func TestFindSymbolTool_LiveDeclarations(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "source.go"), "package p\n// func Target() is only a comment.\nfunc Target() {}\nfunc TargetExtra() {}\n")
	tool := &FindSymbolTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"symbol": "Target", "type": "function"})
	if err != nil || !result.Success || result.Data["count"] != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	rows := result.Data["matches"].([]map[string]any)
	if rows[0]["line"] != 3 {
		t.Fatalf("wrong definition: %+v", rows)
	}
}

func TestFindSymbolTool_SyntaxAndKinds(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		file, source, symbol, filter string
		count                        int
		names                        []string
	}{
		{"methods.go", "package p\ntype Service[T any] struct{}\nfunc(s *Service[T]) Target() {}\ntype API interface { Target() string }\n", "Target", "function", 2, []string{"Service.Target", "API.Target"}},
		{"types.go", "package p\ntype Target = string\ntype TargetExtra struct{}\n", "Target", "type", 1, []string{"Target"}},
		{"types.ts", "interface Target { read(): number; }\nclass TargetExtra {}\n", "Target", "interface", 1, []string{"Target"}},
		{"classes.py", "# class Target: comment\nclass Target:\n    def run(self): pass\n", "Target", "class", 1, []string{"Target"}},
		{"method.py", "class A:\n    async def Target(self): pass\nclass B:\n    def Target(self): pass\n", "B.Target", "function", 1, []string{"B.Target"}},
		{"arrow.ts", "const TargetExtra = () => 1;\nconst Target = (n: number) => n;\nconst text = 'function Target() {}';\n", "Target", "function", 1, []string{"Target"}},
		{"literal.js", "// function Target() {}\nconst text = 'function Target() {}';\n", "Target", "function", 0, nil},
		{"literal.ts", "function Target() {}\n", "Target.*", "function", 0, nil},
		{"alias.ts", "type Target = { value: string };\n", "Target", "type", 1, []string{"Target"}},
		{"interface.ts", "interface Reader { Target(): string; }\n", "Reader.Target", "function", 1, []string{"Reader.Target"}},
		{"assignment.js", "exports.Target = function(a) { return a; };\n", "Target", "function", 1, []string{"exports.Target"}},
		{"lambda.py", "Target = lambda x: x + 1\n", "Target", "function", 1, []string{"Target"}},
	} {
		t.Run(tc.file+tc.symbol, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, tc.file), tc.source)
			tool := &FindSymbolTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"symbol": tc.symbol, "type": tc.filter, "path": tc.file, "source": "live"})
			if err != nil || !result.Success || result.Data["count"] != tc.count || result.Data["snapshot"] != false {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			rows := result.Data["matches"].([]map[string]any)
			for i, name := range tc.names {
				if rows[i]["name"] != name {
					t.Fatalf("wrong names: %+v", rows)
				}
			}
		})
	}
}

func TestFindSymbolTool_BoundedLiveResults(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 70; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%03d.go", i)), "package p\nfunc Target("+strings.Repeat("parameter", 1000)+" int) {}\n")
	}
	tool := &FindSymbolTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultSymbolOutputBytes, 2048} {
		tool.SetMaxOutputBytes(budget)
		result, err := tool.Execute(map[string]any{"symbol": "Target", "type": "function"})
		if err != nil || !result.Success || result.Data["count"] != 70 || result.Data["truncated"] != true {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		rows := result.Data["matches"].([]map[string]any)
		if len(rows) < 1 || len(rows) > DefaultSymbolMatches || result.Data["returned"] != len(rows) {
			t.Fatal("bad retained count")
		}
		for i, row := range rows {
			if !strings.HasSuffix(row["file"].(string), fmt.Sprintf("f%03d.go", i)) || len(row["signature"].(string)) > DefaultSymbolSignatureBytes || row["signature_truncated"] != true || !utf8.ValidString(row["signature"].(string)) {
				t.Fatalf("bad bounded row: %+v", row)
			}
		}
		for _, useToon := range []bool{true, false} {
			encoded, err := toon.New(useToon).MarshalString(result)
			if err != nil || len(encoded) > budget {
				t.Fatalf("TOON=%v bytes=%d err=%v", useToon, len(encoded), err)
			}
		}
	}
	tool.SetMaxOutputBytes(20)
	result, err := tool.Execute(map[string]any{"symbol": "Target"})
	if err != nil || result.Success || !strings.Contains(result.Error, "output budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestFindSymbolTool_SourceSelectionAndScope(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writeFile(t, filepath.Join(dir, "live.go"), "package p\nfunc Target() {}\n")
	for _, path := range []string{"pkg/a.go", "pkg_nested/a.go", "other.go", "../outside.go"} {
		if err := store.UpsertFileRecord(context.Background(), &storage.FileRecord{Path: path}); err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceSymbols(context.Background(), path, []storage.SymbolRecord{{Name: "Target", Kind: "function", StartLine: 4, Signature: "stale snapshot"}}); err != nil {
			t.Fatal(err)
		}
	}
	tool := &FindSymbolTool{Store: store}
	tool.SetWorkDir(dir)
	for _, tc := range []struct {
		path, mode, source string
		count              int
	}{{"pkg", "index", "index", 1}, {"pkg/a.go", "index", "index", 1}, {".", "index", "index", 3}, {".", "live", "live", 1}, {".", "auto", "index", 3}} {
		result, err := tool.Execute(map[string]any{"symbol": "Target", "path": tc.path, "source": tc.mode})
		if err != nil || !result.Success || result.Data["source"] != tc.source || result.Data["count"] != tc.count {
			t.Fatalf("case=%+v result=%+v err=%v", tc, result, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(map[string]any{"symbol": "Target"})
	if err != nil || result.Success || !strings.Contains(result.Error, "index query") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = tool.Execute(map[string]any{"symbol": "Target", "source": "live"})
	if err != nil || !result.Success || result.Data["count"] != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestFindSymbolTool_FailureAndCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "source.go"), "package p\nfunc Target() {}\n")
	for _, tc := range []struct {
		params  map[string]any
		message string
	}{
		{map[string]any{"symbol": "Target", "type": "wrong"}, "unsupported"},
		{map[string]any{"symbol": "Target", "source": "wrong"}, "source"},
		{map[string]any{"symbol": "Target", "path": false}, "path"},
		{map[string]any{"symbol": "Target", "source": "index"}, "unavailable"},
	} {
		tool := &FindSymbolTool{}
		tool.SetWorkDir(dir)
		result, err := tool.Execute(tc.params)
		if err != nil || result.Success || !strings.Contains(result.Error, tc.message) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	tool := &FindSymbolTool{}
	tool.SetWorkDir(dir)
	tool.SetMaxFileSizeBytes(4)
	result, err := tool.Execute(map[string]any{"symbol": "Target"})
	if err != nil || result.Success || !strings.Contains(result.Error, "byte budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = tool.ExecuteWithContext(ctx, map[string]any{"symbol": "Target"})
	if err != nil || result.Success || !strings.Contains(result.Error, "canceled") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	tool.SetMaxFileSizeBytes(0)
	writeFile(t, filepath.Join(dir, "broken.py"), "def Target(:\n    pass\n")
	result, err = tool.Execute(map[string]any{"symbol": "Target", "path": "broken.py"})
	if err != nil || result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	writeFile(t, filepath.Join(dir, "broken.go"), "package p\n"+strings.Repeat("Target", 100000))
	result, err = tool.Execute(map[string]any{"symbol": "Target", "path": "broken.go"})
	if err != nil || result.Success || len(result.Error) > 1024 {
		t.Fatalf("unbounded or missing parse failure: success=%v bytes=%d err=%v", result.Success, len(result.Error), err)
	}
}

func BenchmarkFindSymbolTool_SmallGo(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.go"), []byte("package p\nfunc Target() {}\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	tool := &FindSymbolTool{}
	tool.SetWorkDir(dir)
	params := map[string]any{"symbol": "Target", "type": "function", "path": "."}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := tool.Execute(params)
		if err != nil || !result.Success || result.Data["count"] != 1 {
			b.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestFindSymbolTool_IndexExactNameAndKind(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []storage.SymbolRecord{
		{FilePath: "wrong.go", Name: "TargetExtra", Kind: "function", StartLine: 2, EndLine: 2},
		{FilePath: "types.py", Name: "Target", Kind: "class", StartLine: 1, EndLine: 2},
		{FilePath: "right.go", Name: "Target", Kind: "function", StartLine: 3, EndLine: 3},
	} {
		if err := store.UpsertFileRecord(context.Background(), &storage.FileRecord{Path: row.FilePath}); err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceSymbols(context.Background(), row.FilePath, []storage.SymbolRecord{row}); err != nil {
			t.Fatal(err)
		}
	}
	tool := &FindSymbolTool{Store: store}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"symbol": "Target", "type": "function"})
	if err != nil || !result.Success || result.Data["count"] != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	row := result.Data["matches"].([]map[string]any)[0]
	if filepath.Base(row["file"].(string)) != "right.go" {
		t.Fatalf("wrong indexed match: %+v", row)
	}
}

func TestFindSymbolTool_SearchFailureIsNotClean(t *testing.T) {
	tool := &FindSymbolTool{}
	result, err := tool.Execute(map[string]any{"symbol": "Target", "path": filepath.Join(t.TempDir(), "absent")})
	if err != nil || result.Success {
		t.Fatalf("result=%+v err=%v; expected explicit search failure", result, err)
	}
}
