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
)

func TestFindReferencesTool_SingleFileAndDefinitions(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		file, source  string
		filtered, all int
		firstColumn   int
	}{
		{"file:colon.go", "package p\nfunc Target(){ Target() }\nfunc use(){ Target() }\n", 2, 2, 16},
		{"only.go", "package p\nfunc Target(){}\nfunc use(){Target()}\n", 1, 2, 12},
		{"types.go", "package p\ntype Target struct{}\nvar _ Target\n", 1, 2, 7},
		{"method.go", "package p\ntype Service struct{}\nfunc(s Service) Target(){s.Target()}\n", 1, 1, 28},
		{"source.py", "def Target(): Target()\nTarget()\n# def Target is text\n", 3, 3, 15},
		{"source.js", "function Target(){ Target(); }\nTarget();\n// function Target() {}\n", 3, 3, 20},
		{"arrow.ts", "const Target = () => Target();\nTarget();\n", 2, 2, 22},
		{"interface.ts", "interface API { Target(): string; }\nobj.Target();\n", 1, 2, 5},
		{"alias.py", "type Target[T] = list[T]\nx: Target[int]\n", 1, 2, 4},
		{"computed.js", "obj[Target] = function() {};\nclass A { [Target](){} }\n", 2, 2, 5},
		{"receiver.js", "Target.run = function() {};\n", 1, 1, 1},
		{"binding.js", "obj.Target = function() {};\nobj.Target();\n", 1, 2, 5},
		{"lambda.py", "Target = lambda x: x\nTarget(1)\n", 1, 2, 1},
		{"other.txt", "func Target()\nTarget TargetExtra _Target Target_ $Target Target$ αTarget Targetα\n", 2, 2, 6},
		{"qualified.txt", "Target.run()\nother.Target.run()\n", 2, 2, 1},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, tc.file), tc.source)
			tool := &FindReferencesTool{}
			tool.SetWorkDir(dir)
			for _, include := range []bool{false, true} {
				symbol := "Target"
				if tc.file == "qualified.txt" {
					symbol = "Target.run"
				}
				result, err := tool.Execute(map[string]any{"symbol": symbol, "path": tc.file, "include_definition": include})
				want := tc.filtered
				if include {
					want = tc.all
				}
				if err != nil || !result.Success || result.Data["count"] != want {
					t.Fatalf("include=%v result=%+v err=%v want=%d", include, result, err, want)
				}
				rows := result.Data["references"].([]map[string]any)
				if !include && rows[0]["column"] != tc.firstColumn {
					t.Fatalf("first location %+v, want column %d", rows[0], tc.firstColumn)
				}
				if rows[0]["file"] != filepath.Join(dir, tc.file) {
					t.Fatalf("wrong filename: %+v", rows)
				}
			}
		})
	}
}

func TestFindReferencesTool_LiteralBoundaries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "text.txt"), "TargetExtra _Target Target_ $Target Target$ αTarget Targetα\nTarget\nTarget.run\nTarget.*\n")
	tool := &FindReferencesTool{}
	tool.SetWorkDir(dir)
	for _, tc := range []struct {
		query string
		count int
	}{{"Target", 3}, {"Target.*", 1}, {"missing", 0}} {
		r, e := tool.Execute(map[string]any{"symbol": tc.query})
		if e != nil || !r.Success || r.Data["count"] != tc.count {
			t.Fatalf("query=%s result=%+v err=%v", tc.query, r, e)
		}
	}
}
func TestFindReferencesTool_BoundedResults(t *testing.T) {
	dir := t.TempDir()
	for i := 69; i >= 0; i-- {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), strings.Repeat("界", 1000)+" Target "+strings.Repeat("界", 1000)+"\n")
	}
	tool := &FindReferencesTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultReferenceOutputBytes, 2048} {
		tool.SetMaxOutputBytes(budget)
		r, e := tool.Execute(map[string]any{"symbol": "Target"})
		if e != nil || !r.Success || r.Data["count"] != 70 || r.Data["truncated"] != true {
			t.Fatalf("result=%+v err=%v", r, e)
		}
		rows := r.Data["references"].([]map[string]any)
		if len(rows) == 0 || len(rows) > DefaultReferenceMatches || r.Data["returned"] != len(rows) {
			t.Fatalf("wrong rows: %+v", r.Data)
		}
		for i, row := range rows {
			content := row["content"].(string)
			if !strings.HasSuffix(row["file"].(string), fmt.Sprintf("f%03d.txt", i)) || len(content) > DefaultReferenceExcerptBytes || !utf8.ValidString(content) || !strings.Contains(content, "Target") || row["content_truncated"] != true || row["column"] != 3002 || row["content_start_column"].(int) < 2900 {
				t.Fatalf("bad row: %+v", row)
			}
		}
		for _, useToon := range []bool{true, false} {
			s, e := toon.New(useToon).MarshalString(r)
			if e != nil || len(s) > budget {
				t.Fatalf("toon=%v bytes=%d err=%v", useToon, len(s), e)
			}
		}
	}
	tool.SetMaxOutputBytes(20)
	r, e := tool.Execute(map[string]any{"symbol": "Target"})
	if e != nil || r.Success || !strings.Contains(r.Error, "output budget") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
func TestFindReferencesTool_FailuresAndCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bad.go"), "package p\nfunc Target( {")
	tool := &FindReferencesTool{}
	tool.SetWorkDir(dir)
	for _, params := range []map[string]any{
		{}, {"symbol": ""}, {"symbol": "Target\n"}, {"symbol": strings.Repeat("x", 1025)}, {"symbol": "Target", "path": false}, {"symbol": "Target", "include_definition": "false"}, {"symbol": "Target", "path": "../"}, {"symbol": "Target", "path": "missing"}, {"symbol": "Target", "path": "bad.go"},
	} {
		r, e := tool.Execute(params)
		if e != nil || r.Success || r.Error == "" {
			t.Fatalf("params=%+v result=%+v err=%v", params, r, e)
		}
	}
	r, e := tool.Execute(map[string]any{"symbol": "Target", "include_definition": true})
	if e != nil || !r.Success || r.Data["count"] != 1 {
		t.Fatalf("text fallback result=%+v err=%v", r, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e = tool.ExecuteWithContext(ctx, map[string]any{"symbol": "Target"})
	if e != nil || r.Success || !strings.Contains(r.Error, "canceled") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
	tool.SetMaxFileSizeBytes(4)
	r, e = tool.Execute(map[string]any{"symbol": "Target"})
	if e != nil || r.Success || !strings.Contains(r.Error, "byte budget") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
func TestFindReferencesTool_ScopeAndBinary(t *testing.T) {
	dir := t.TempDir()
	tool := &FindReferencesTool{}
	tool.SetWorkDir(dir)
	writeFile(t, filepath.Join(dir, ".hidden.txt"), "Target")
	writeFile(t, filepath.Join(dir, "binary"), "Target\x00")
	writeFile(t, filepath.Join(dir, "invalid"), "Target\xff")
	for _, name := range []string{".git", "node_modules", "vendor", ".venv", "venv", "__pycache__"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, name, "text.txt"), "Target")
	}
	if err := os.Symlink(filepath.Join(dir, ".hidden.txt"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	r, e := tool.Execute(map[string]any{"symbol": "Target"})
	if e != nil || !r.Success || r.Data["count"] != 1 || r.Data["binary_files_skipped"] != 2 || r.Data["files_scanned"] != 3 {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}

func BenchmarkFindReferencesTool(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.go"), []byte("package p\nfunc Target(){}\nfunc use(){Target()}\n"), 0644); err != nil {
		b.Fatal(err)
	}
	tool := &FindReferencesTool{}
	tool.SetWorkDir(dir)
	params := map[string]any{"symbol": "Target"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, e := tool.Execute(params)
		if e != nil || !r.Success {
			b.Fatalf("result=%+v err=%v", r, e)
		}
	}
}
