package builtin

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExplainCodeTool_Selectors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"native integers", map[string]any{"start_line": 2, "end_line": 3}, "second\nthird"},
		{"JSON numbers", map[string]any{"start_line": float64(2), "end_line": float64(3)}, "second\nthird"},
		{"json.Number", map[string]any{"start_line": json.Number("2")}, "second"},
		{"int64", map[string]any{"start_line": int64(2)}, "second"},
		{"end alone", map[string]any{"end_line": 2}, "first\nsecond"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.txt")
			if err := os.WriteFile(path, []byte("first\nsecond\nthird\nfourth"), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &ExplainCodeTool{}
			tool.SetWorkDir(dir)
			for _, field := range []string{"file", "code"} {
				p := map[string]any{}
				for k, v := range tc.params {
					p[k] = v
				}
				if field == "file" {
					p[field] = "source.txt"
				} else {
					p[field] = "first\nsecond\nthird\nfourth"
				}
				result, err := tool.Execute(p)
				if err != nil || !result.Success || result.Data["code"] != tc.want || result.Data["code_truncated"] != false {
					t.Fatalf("%s: %+v %v", field, result, err)
				}
			}
		})
	}
}

func TestExplainCodeTool_InvalidSelectors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"zero", map[string]any{"start_line": float64(0)}},
		{"negative", map[string]any{"start_line": -1}},
		{"reversed", map[string]any{"start_line": 2, "end_line": 1}},
		{"fraction", map[string]any{"start_line": 1.5}},
		{"NaN", map[string]any{"start_line": math.NaN()}},
		{"infinite", map[string]any{"end_line": math.Inf(1)}},
		{"overflow", map[string]any{"end_line": math.Ldexp(1, 63)}},
		{"outside", map[string]any{"end_line": 100}},
		{"wrong type", map[string]any{"start_line": "2"}},
		{"wrong function type", map[string]any{"function_name": true}},
		{"mixed selectors", map[string]any{"function_name": "Add", "start_line": 2}},
		{"two inputs", map[string]any{"code": "second input"}},
		{"malformed literal name", map[string]any{"function_name": "("}},
		{"regex wildcard", map[string]any{"function_name": ".*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.go")
			source := "package fixture\nfunc Add(a,b int)int{return a+b}\n"
			if err := os.WriteFile(path, []byte(source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &ExplainCodeTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": "source.go"}
			for k, v := range tc.params {
				params[k] = v
			}
			result, err := tool.Execute(params)
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("accepted invalid selector: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != source {
				t.Fatal("read-only operation changed source")
			}
		})
	}
}

func TestExplainCodeTool_FunctionSelection(t *testing.T) {
	for _, tc := range []struct{ name, file, source, target, want string }{
		{"Go one line", "source.go", "package p\nfunc Add(a,b int)int{return a+b}\n", "Add", "func Add(a,b int)int{return a+b}"},
		{"Go fake declaration and nested braces", "source.go", "package p\n// func Add(a,b int)int{return 0}\nfunc Add(a,b int)int {if a>0 {return a+b};return b}\nfunc Other(){}\n", "Add", "func Add(a,b int)int {if a>0 {return a+b};return b}"},
		{"Go generic receiver", "source.go", "package p\ntype Box[T any] struct{}\nfunc (box *Box[T]) Add(a,b int)int{return a+b}\n", "Box.Add", "func (box *Box[T]) Add(a,b int)int{return a+b}"},
		{"Python method", "source.py", "class Service:\n    async def add(\n        self,a,b,\n    ):\n        return a+b\n\nclass Other:\n    def add(self,a,b): return 0\n", "Service.add", "async def add(\n        self,a,b,\n    ):\n        return a+b"},
		{"Python nested", "source.py", "def outer():\n    def add(a,b): return a+b\n    return add\n", "outer.add", "def add(a,b): return a+b"},
		{"JavaScript declaration", "source.js", "const fake='function add(){return 0}';\nexport function add(a,b){ if (a) {return a+b;} return b; }\nfunction other(){}\n", "add", "function add(a,b){ if (a) {return a+b;} return b; }"},
		{"JavaScript class", "source.js", "class Service { async add(a,b){return a+b;} }\nclass Other { add(){return 0;} }\n", "Service.add", "async add(a,b){return a+b;}"},
		{"JavaScript object", "source.js", "const service={add(a,b){return a+b}};\n", "service.add", "add(a,b){return a+b}"},
		{"JavaScript arrow", "source.js", "export const add=(a,b)=>a+b;\n", "add", "(a,b)=>a+b"},
		{"TypeScript arrow", "source.ts", "export const add=(a:number,b:number):number=>a+b;\n", "add", "(a:number,b:number):number=>a+b"},
		{"TSX declaration", "source.tsx", "export function Add(){return <div>hello</div>}\n", "Add", "function Add(){return <div>hello</div>}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &ExplainCodeTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": tc.file, "function_name": tc.target})
			if err != nil || !result.Success || result.Data["code"] != tc.want || result.Data["code_truncated"] != false {
				t.Fatalf("selection: %+v %v", result, err)
			}
			start := strings.Index(tc.source, tc.want)
			wantLine := strings.Count(tc.source[:start], "\n") + 1
			if result.Data["start_line"] != wantLine || result.Data["end_line"] != wantLine+strings.Count(tc.want, "\n") {
				t.Fatalf("wrong absolute line range: %+v", result.Data)
			}
		})
	}
}

func TestExplainCodeTool_AmbiguousFunctionSelection(t *testing.T) {
	for _, tc := range []struct{ file, source string }{
		{"source.go", "package p\ntype A struct{}\ntype B struct{}\nfunc (A) Add(){}\nfunc (B) Add(){}\n"},
		{"source.js", "class A { Add(){} }\nclass B { Add(){} }\n"},
		{"source.py", "class A:\n    def Add(self): pass\nclass B:\n    def Add(self): pass\n"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &ExplainCodeTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": tc.file, "function_name": "Add"})
			if err != nil || result.Success || !strings.Contains(result.Error, "ambiguous") {
				t.Fatalf("ambiguous: %+v %v", result, err)
			}
		})
	}
}

func TestExplainCodeTool_Budgets(t *testing.T) {
	for _, code := range []string{strings.Repeat("x\n", 10000), strings.Repeat("数字\"\x00\n", 2000)} {
		t.Run(code[:1], func(t *testing.T) {
			tool := &ExplainCodeTool{}
			for _, limit := range []int{0, 1024, 2048} {
				tool.SetMaxOutputBytes(limit)
				result, err := tool.Execute(map[string]any{"code": code})
				if err != nil || !result.Success {
					t.Fatalf("budget %d: %+v %v", limit, result, err)
				}
				excerpt := result.Data["code"].(string)
				if !utf8.ValidString(excerpt) || !strings.HasPrefix(code, excerpt) || result.Data["code_truncated"] != true || result.Data["selected_bytes"] != len(code) || result.Data["returned_bytes"] != len(excerpt) {
					t.Fatalf("invalid excerpt: %+v", result.Data)
				}
				encoded, err := json.Marshal(map[string]any{"success": true, "data": result.Data})
				if err != nil {
					t.Fatal(err)
				}
				budget := limit
				if budget == 0 {
					budget = DefaultExplainOutputBytes
				}
				if len(encoded) > budget {
					t.Fatalf("output %d exceeds %d", len(encoded), budget)
				}
				analysis := result.Data["explanation"].(map[string]any)
				if analysis["lines"] != strings.Count(code, "\n")+1 || analysis["analysis_kind"] != "keyword_heuristic" {
					t.Fatalf("analysis covered only excerpt: %+v", analysis)
				}
			}
			tool.SetMaxOutputBytes(10)
			result, err := tool.Execute(map[string]any{"code": code})
			if err != nil || result.Success {
				t.Fatalf("impossible budget accepted: %+v %v", result, err)
			}
			tool.SetMaxOutputBytes(0)
			tool.SetMaxFileSizeBytes(int64(len(code) - 1))
			result, err = tool.Execute(map[string]any{"code": code})
			if err != nil || result.Success || !strings.Contains(result.Error, "input exceeds") {
				t.Fatalf("input budget bypass: %+v %v", result, err)
			}
		})
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte("bounded source\n"), 0640); err != nil {
		t.Fatal(err)
	}
	tool := &ExplainCodeTool{}
	tool.SetWorkDir(dir)
	tool.SetMaxFileSizeBytes(8)
	result, err := tool.Execute(map[string]any{"file": "source.txt"})
	if err != nil || result.Success || !strings.Contains(result.Error, "input limit") {
		t.Fatalf("file budget bypass: %+v %v", result, err)
	}
}

func TestExplainCodeTool_FileAndSyntaxBoundaries(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.go")
	if err := os.WriteFile(outside, []byte("package p\nfunc Add(){}\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "linked.go")); err != nil {
		t.Fatal(err)
	}
	tool := &ExplainCodeTool{}
	tool.SetWorkDir(work)
	for _, file := range []string{"../outside.go", "linked.go", "."} {
		result, err := tool.Execute(map[string]any{"file": file})
		if err != nil || result.Success {
			t.Fatalf("invalid source %s accepted: %+v %v", file, result, err)
		}
	}
	for _, file := range []string{"source.go", "source.py", "source.js", "source.ts"} {
		if err := os.WriteFile(filepath.Join(work, file), []byte("invalid { incomplete source"), 0640); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(map[string]any{"file": file, "function_name": "Add"})
		if err != nil || result.Success {
			t.Fatalf("invalid syntax %s accepted: %+v %v", file, result, err)
		}
	}
}

func TestExplainCodeTool_NullableUnusedSelectors(t *testing.T) {
	dir := t.TempDir()
	source := "package fixture\nfunc Add(a,b int)int{return a+b}\n"
	if err := os.WriteFile(filepath.Join(dir, "source.go"), []byte(source), 0640); err != nil {
		t.Fatal(err)
	}
	tool := &ExplainCodeTool{}
	tool.SetWorkDir(dir)
	for _, params := range []map[string]any{
		{"file": "source.go", "code": nil, "function_name": "Add", "start_line": nil, "end_line": nil},
		{"file": nil, "code": "func Add(){}", "function_name": nil, "start_line": nil, "end_line": nil},
		{"file": "source.go", "code": nil, "function_name": nil, "start_line": 2, "end_line": nil},
	} {
		result, err := tool.Execute(params)
		if err != nil || !result.Success {
			t.Fatalf("null optional selectors rejected: %+v %v", result, err)
		}
	}
	encoded, err := json.Marshal(tool.Parameters())
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Type    []string `json:"type"`
			Default any      `json:"default"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "code", "function_name", "start_line", "end_line"} {
		property := schema.Properties[name]
		if len(property.Type) != 2 || property.Type[1] != "null" || property.Default != nil {
			t.Fatalf("%s does not advertise nullable optionality: %s", name, encoded)
		}
	}
}
