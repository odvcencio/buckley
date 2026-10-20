package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestAnalyzeComplexityTool_SyntaxResults(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		file, source string
		want         map[string]int
	}{
		{"sample.py", "def simple():\n    return 'if for while'\n\nasync def branch(a, b):\n    if a and b:\n        return 1\n    elif b:\n        return 2\n    return 0\n", map[string]int{"simple": 1, "branch": 4}},
		{"sample.go", "package p\nfunc simple() {}\nfunc branch(a,b bool) { if a && b { return }; for a { a=false } }\n", map[string]int{"simple": 1, "branch": 4}},
		{"sample.ts", "const simple = () => 'if for while';\nclass Service { branch(a: boolean, b: boolean) { return a && b ? 1 : 0; } }\n", map[string]int{"simple": 1, "Service.branch": 3}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.source), 0o600); err != nil {
				t.Fatal(err)
			}
			tool := &AnalyzeComplexityTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"path": tc.file, "threshold": 1})
			if err != nil || !result.Success {
				t.Fatalf("analyze: result=%+v err=%v", result, err)
			}
			got := map[string]int{}
			for _, row := range result.Data["functions"].([]map[string]any) {
				got[row["name"].(string)] = row["complexity"].(int)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("functions=%v, want %v", got, tc.want)
			}
			if result.Data["total_functions"] != len(tc.want) {
				t.Fatalf("total_functions=%v, want %d", result.Data["total_functions"], len(tc.want))
			}
		})
	}
}

func TestAnalyzeComplexityTool_RejectsIncompleteAnalysis(t *testing.T) {
	for _, tc := range []struct {
		name, file, source, message string
		params                      map[string]any
		limit                       int64
	}{
		{name: "invalid Go", file: "broken.go", source: "package p\nfunc broken(", message: "parse"},
		{name: "invalid Python", file: "broken.py", source: "def broken(:\n    pass\n", message: "parse"},
		{name: "empty Python function", file: "broken.py", source: "def broken():\n", message: "empty Python suite"},
		{name: "empty Python class", file: "broken.py", source: "class Broken:\n    # comment only\n", message: "empty Python suite"},
		{name: "empty Python conditional", file: "broken.py", source: "if True:\n", message: "empty Python suite"},
		{name: "invalid JS", file: "broken.js", source: "function broken( {", message: "parse"},
		{name: "unsupported", file: "source.rb", source: "def f; end", message: "unsupported"},
		{name: "fractional threshold", file: "source.py", source: "def f(): pass\n", params: map[string]any{"threshold": 1.5}, message: "positive integer"},
		{name: "zero threshold", file: "source.py", source: "def f(): pass\n", params: map[string]any{"threshold": 0}, message: "positive integer"},
		{name: "invalid language", file: "source.py", source: "def f(): pass\n", params: map[string]any{"language": "ruby"}, message: "unsupported language"},
		{name: "source limit", file: "source.py", source: "def f(): pass\n", limit: 4, message: "byte budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, tc.file), tc.source)
			tool := &AnalyzeComplexityTool{}
			tool.SetWorkDir(dir)
			tool.SetMaxFileSizeBytes(tc.limit)
			params := map[string]any{"path": tc.file}
			for name, value := range tc.params {
				params[name] = value
			}
			result, err := tool.Execute(params)
			if err != nil || result.Success || !strings.Contains(result.Error, tc.message) {
				t.Fatalf("result=%+v err=%v; want failure containing %q", result, err, tc.message)
			}
		})
	}
}

func TestAnalyzeComplexityTool_BoundedResults(t *testing.T) {
	dir := t.TempDir()
	var source strings.Builder
	source.WriteString("package p\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&source, "func f%03d(x bool) { if x { return } }\n", i)
	}
	source.WriteString("func worst(x bool) { if x { return }; if !x { return }; if x { return } }\n")
	writeFile(t, filepath.Join(dir, "large.go"), source.String())
	tool := &AnalyzeComplexityTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultComplexityOutputBytes, 2048} {
		tool.SetMaxOutputBytes(budget)
		result, err := tool.Execute(map[string]any{"path": "large.go", "threshold": json.Number("1")})
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		rows := result.Data["functions"].([]map[string]any)
		if result.Data["total_functions"] != 501 || result.Data["complex_functions"] != 501 || result.Data["returned"] != len(rows) || result.Data["truncated"] != true {
			t.Fatalf("bad counts: %+v", result.Data)
		}
		if len(rows) == 0 || len(rows) > DefaultComplexityFunctions || rows[0]["name"] != "worst" || rows[0]["complexity"] != 4 {
			t.Fatalf("bad top results: %+v", rows)
		}
		for i := 2; i < len(rows); i++ {
			if rows[i-1]["line"].(int) >= rows[i]["line"].(int) {
				t.Fatalf("unstable ties: %+v", rows)
			}
		}
		for _, useToon := range []bool{true, false} {
			encoded, err := toon.New(useToon).MarshalString(result)
			if err != nil || len(encoded) > budget {
				t.Fatalf("TOON=%v bytes=%d budget=%d err=%v", useToon, len(encoded), budget, err)
			}
		}
	}
	tool.SetMaxOutputBytes(32)
	result, err := tool.Execute(map[string]any{"path": "large.go", "threshold": 1})
	if err != nil || result.Success || !strings.Contains(result.Error, "output budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnalyzeComplexityTool_DirectoryScopeAndBudgets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package p\nfunc a() {}\n")
	writeFile(t, filepath.Join(dir, "b.py"), "def b(): return 1\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "not code")
	if err := os.Mkdir(filepath.Join(dir, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "node_modules", "broken.go"), "invalid Go")
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "escape.py"), "invalid Python")
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	tool := &AnalyzeComplexityTool{}
	tool.SetWorkDir(dir)
	for _, tc := range []struct {
		language any
		files    int
	}{{nil, 2}, {"go", 1}, {"python", 1}} {
		result, err := tool.Execute(map[string]any{"path": ".", "threshold": 1, "language": tc.language})
		if err != nil || !result.Success || result.Data["files_analyzed"] != tc.files || result.Data["total_functions"] != tc.files {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	result, err := tool.Execute(map[string]any{"path": "linked/escape.py"})
	if err != nil || result.Success || !strings.Contains(result.Error, "escapes workdir") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	tool.SetMaxFileSizeBytes(25)
	result, err = tool.Execute(map[string]any{"path": "."})
	if err != nil || result.Success || !strings.Contains(result.Error, "byte budget") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = tool.ExecuteWithContext(ctx, map[string]any{"path": "."})
	if err != nil || result.Success || !strings.Contains(result.Error, "canceled") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	tool.SetMaxFileSizeBytes(0)
	writeFile(t, filepath.Join(dir, "b.py"), "def invalid(:\n")
	result, err = tool.Execute(map[string]any{"path": "."})
	if err != nil || result.Success || !strings.Contains(result.Error, "parse") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnalyzeComplexityTool_TraversalBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= DefaultComplexityEntries; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("%05d.txt", i)), "")
	}
	tool := &AnalyzeComplexityTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"path": "."})
	if err != nil || result.Success || !strings.Contains(result.Error, "traversal exceeds") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnalyzeComplexityTool_LongSourceLineAndDefaultThreshold(t *testing.T) {
	dir := t.TempDir()
	source := "#" + strings.Repeat(" if for while", 10000) + "\ndef simple(): return 1\n"
	writeFile(t, filepath.Join(dir, "source.py"), source)
	tool := &AnalyzeComplexityTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"path": "source.py", "threshold": nil, "language": nil})
	if err != nil || !result.Success || result.Data["total_functions"] != 1 || result.Data["complex_functions"] != 0 || result.Data["truncated"] != false {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnalyzeComplexityTool_BoundedParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "broken.go"), "package p\n"+strings.Repeat("identifier", 100000))
	tool := &AnalyzeComplexityTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"path": "broken.go"})
	if err != nil || result.Success || !strings.Contains(result.Error, "parse") {
		t.Fatalf("expected parse failure, success=%v err=%v", result.Success, err)
	}
	for _, useToon := range []bool{true, false} {
		encoded, err := toon.New(useToon).MarshalString(result)
		if err != nil || len(encoded) > DefaultComplexityOutputBytes {
			t.Fatalf("TOON=%v error payload bytes=%d budget=%d err=%v", useToon, len(encoded), DefaultComplexityOutputBytes, err)
		}
	}
}

func BenchmarkAnalyzeComplexityTool_LargeGo(b *testing.B) {
	b.Setenv("PATH", b.TempDir())
	dir := b.TempDir()
	var source strings.Builder
	source.WriteString("package p\n")
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&source, "func f%d(x bool) {\n if x { return }\n}\n", i)
	}
	path := filepath.Join(dir, "large.go")
	if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	tool := &AnalyzeComplexityTool{}
	params := map[string]any{"path": path, "threshold": float64(1)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := tool.Execute(params)
		if err != nil || !result.Success || result.Data["total_functions"] != 5000 {
			b.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestAnalyzeComplexityTool_DecisionsAndClosures(t *testing.T) {
	for _, tc := range []struct {
		file, source string
		want         map[string]int
	}{
		{"flow.go", `package p
type Service[T any] struct{}
func (s *Service[T]) Run(x bool, c chan int) {
 switch {case x: x=false; default:}
 select {case <-c: default:}
 f := func() { if x || !x { return } }
 _ = f
}
`, map[string]int{"Service.Run": 3, "<anonymous>": 3}},
		{"flow.py", `class Service:
    async def run(self, xs, x):
        assert x
        with open('x') as f:
            try:
                y = [n for n in xs if n > 0]
            except ValueError:
                return 0
        match x:
            case 1 | 2 if x:
                return 1
            case _:
                return 2
    def outer(self):
        def inner(a):
            return 1 if a else 0
        return inner
`, map[string]int{"Service.run": 8, "Service.outer": 1, "Service.outer.inner": 2}},
		{"flow.js", `function outer(x) {
 const cb = y => y ? 1 : 0;
 switch (x) {case 1: break; default: break;}
 try { while (x) { x--; } } catch (e) { return cb(x); }
 return x ?? 1;
}
function* generate(xs) { for (const x of xs) { if (x) yield x; } }
`, map[string]int{"outer": 5, "outer.cb": 2, "generate": 3}},
		{"view.tsx", `interface I { method(x: boolean): number; }
const View = (x: boolean) => x ? <div/> : <span/>;
`, map[string]int{"View": 2}},
		{"declare.ts", "declare function declared(x: number): number;\nabstract class A { abstract f(): number; }", map[string]int{}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, tc.file), tc.source)
			tool := &AnalyzeComplexityTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"path": tc.file, "threshold": 1})
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			got := map[string]int{}
			for _, row := range result.Data["functions"].([]map[string]any) {
				got[row["name"].(string)] = row["complexity"].(int)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("functions=%v want=%v", got, tc.want)
			}
		})
	}
}
