package builtin

import (
	"bytes"
	"encoding/json"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateDocstringTool_GoCommentsPreserveCode(t *testing.T) {
	for _, tc := range []struct{ name, source, target, call string }{
		{"multiline description", "package fixture\nfunc Add(a,b int)int{return a+b}\n", "Add", "Add(3,4)"},
		{"generic receiver callback", "package fixture\ntype Box[T any] struct{}\nfunc (box *Box[T]) Add(\n a,b int,\n combine func(int,int)int,\n)int{return combine(a,b)}\n", "Box.Add", "new(Box[int]).Add(3,4,func(a,b int)int{return a+b})"},
		{"void return", "package fixture\nvar total int\nfunc Add(a,b int){total=a+b}\nfunc Value()int{Add(3,4);return total}\n", "Add", "Value()"},
		{"inline declaration", "package fixture;func Add(a,b int)int{return a+b}\n", "Add", "Add(3,4)"},
		{"fake declaration", "package fixture\nvar Fake=`\nfunc Add(a,b int)int{return 0}\n`\nfunc Add(a,b int)int{return a+b}\n", "Add", "Add(3,4)"},
		{"CRLF", "package fixture\r\nfunc Add(a,b int)int{return a+b}\r\n", "Add", "Add(3,4)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.go")
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": "source.go", "function_name": tc.target, "description": "Add returns the sum.\nUse both inputs; */ stays comment text."}
			result, err := tool.Execute(params)
			if err != nil || !result.Success {
				t.Fatalf("generate: %+v %v", result, err)
			}
			generated, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tc.source, "\r\n") && strings.Contains(strings.ReplaceAll(string(generated), "\r\n", ""), "\n") {
				t.Fatal("introduced bare LF")
			}
			fset := token.NewFileSet()
			source, err := parser.ParseFile(fset, path, generated, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			fn, err := findNamedGoFunction(source, tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if fn.Doc == nil || !strings.Contains(fn.Doc.Text(), "Use both inputs") || !strings.Contains(fn.Doc.Text(), "a: TODO") || !strings.Contains(fn.Doc.Text(), "b: TODO") {
				t.Fatalf("documentation not attached to target: %+v", fn.Doc)
			}
			if tc.name == "void return" && strings.Contains(fn.Doc.Text(), "Returns:") {
				t.Fatal("invented a return value for a void function")
			}
			if tc.name == "generic receiver callback" && (!strings.Contains(fn.Doc.Text(), "combine: TODO") || strings.Contains(fn.Doc.Text(), "box: TODO")) {
				t.Fatal("receiver or callback parameters confused")
			}
			normalize := func(code string) string {
				fset := token.NewFileSet()
				node, err := parser.ParseFile(fset, "", code, parser.SkipObjectResolution)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := format.Node(&out, fset, node); err != nil {
					t.Fatal(err)
				}
				return out.String()
			}
			if normalize(tc.source) != normalize(string(generated)) {
				t.Fatal("non-comment Go code changed")
			}
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/comments\n\ngo 1.25\n"), 0600); err != nil {
				t.Fatal(err)
			}
			test := "package fixture\nimport \"testing\"\nfunc TestBehavior(t *testing.T){if got:=" + tc.call + ";got!=7{t.Fatalf(\"got %d, want 7\",got)}}\n"
			if err := os.WriteFile(filepath.Join(dir, "source_test.go"), []byte(test), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "test", "-count=1", "./...")
			cmd.Dir = dir
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Go runtime: %v\n%s", err, output)
			}
			again, err := tool.Execute(params)
			if err != nil || !again.Success || again.Data["message"] != "Function already has documentation" {
				t.Fatalf("idempotence: %+v %v", again, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(generated) {
				t.Fatal("second call changed documentation")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatal("permissions changed")
			}
		})
	}
}

func TestGenerateDocstringTool_JSCommentsPreserveSyntax(t *testing.T) {
	for _, tc := range []struct{ name, file, source, target string }{
		{"default callback commas", "source.js", "export function add(a=((x,y)=>x+y)(-1,4),b=({x:3,y:4}).y){return a+b;}\n", "add"},
		{"overload signatures", "source.ts", "export function add(a:number,b:number):number;\nexport function add(a:string,b:string):string;\nexport function add(input:any,other:any){return input+other;}\n", "add"},
		{"method overloads", "source.ts", "export class Service {\nadd(a:number,b:number):number;\nadd(a:string,b:string):string;\nadd(input:any,other:any){return input+other;}\n}\n", "Service.add"},
		{"ambient declaration", "source.d.ts", "export declare function add(a:number,b:number):number;\n", "add"},
		{"abstract method", "source.ts", "export abstract class Service { abstract add(a:number,b:number):number; }\n", "Service.add"},
		{"TypeScript arrow", "source.ts", "export const add=(a:number,b:number=4):number=>a+b;\n", "add"},
		{"single arrow parameter", "source.js", "export const add=a=>a+4;\n", "add"},
		{"inline method", "source.js", "export class Service { add(a,b){return a+b;} }\n", "Service.add"},
		{"object property", "source.js", "export const service={add:(a,b)=>a+b};\n", "service.add"},
		{"multiple bindings", "source.js", "export const other=99, add=(a,b)=>a+b;\n", "add"},
		{"property assignment", "source.js", "const service={}; service.add=(a,b)=>a+b; export {service};\n", "service.add"},
		{"nested function", "source.js", "export function outer(){ function add(a,b){return a+b;} return add; }\n", "outer.add"},
		{"CRLF", "source.js", "export function add(a,b){return a+b;}\r\n", "add"},
		{"module extension", "source.mjs", "export function add(a,b){return a+b;}\n", "add"},
		{"TSX", "source.tsx", "export function Add(){return <div>text</div>}\n", "Add"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": tc.file, "function_name": tc.target, "description": "Add values.\nLiteral */ is documentation, not code.\n@param {any} a\n@returns {any}\n@ts-nocheck"}
			result, err := tool.Execute(params)
			if err != nil || !result.Success {
				t.Fatalf("generate: %+v %v", result, err)
			}
			generated, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tc.source, "\r\n") && strings.Contains(strings.ReplaceAll(string(generated), "\r\n", ""), "\n") {
				t.Fatal("introduced bare LF")
			}
			doc := result.Data["docstring"].(string)
			if strings.Contains(doc, "@param {*}") || strings.Contains(doc, "@returns {*}") {
				t.Fatal("invented types alter JavaScript inference")
			}
			if !strings.Contains(doc, "&#64;param {any} a") || !strings.Contains(doc, "&#64;ts-nocheck") {
				t.Fatal("description became active JSDoc annotations")
			}
			if !strings.Contains(doc, "Literal * / is documentation") {
				t.Fatal("comment terminator not encoded")
			}
			if strings.Contains(tc.name, "overload") && (!strings.Contains(doc, "@param a ") || strings.Contains(doc, "@param input ") || strings.Index(string(generated), "/**") > strings.Index(string(generated), "add(a:number")) {
				t.Fatalf("overload docs not on public signature: %s", generated)
			}
			if tc.name == "default callback commas" && (!strings.Contains(doc, "@param a ") || !strings.Contains(doc, "@param b ") || strings.Contains(doc, "@param x ") || strings.Contains(doc, "@param y ")) {
				t.Fatalf("default expressions mistaken for parameters: %s", doc)
			}
			again, err := tool.Execute(params)
			if err != nil || !again.Success || again.Data["message"] != "Function already has documentation" {
				t.Fatalf("idempotence: %+v %v\n%s", again, err, generated)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(generated) {
				t.Fatal("second call changed source")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatal("permissions changed")
			}
		})
	}
}

func TestGenerateDocstringTool_ExistingCommentDocumentation(t *testing.T) {
	for _, tc := range []struct{ file, source, target string }{
		{"source.go", "package p\n/* Add returns a value. */\nfunc Add()int{return 7}\n", "Add"},
		{"source.js", "/** Add returns a value.\n * @returns {number} The value.\n */\nexport function add(){return 7;}\n", "add"},
		{"source.js", "/** Add values. */\n// Keep this note.\nexport function add(){return 7;}\n", "add"},
		{"source.ts", "/** Add values. */\nexport const add=(a:number,b:number)=>a+b;\n", "add"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": tc.file, "function_name": tc.target, "description": "Do not replace."})
			if err != nil || !result.Success || result.Data["message"] != "Function already has documentation" {
				t.Fatalf("existing: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.source {
				t.Fatal("existing docs changed")
			}
		})
	}
}

func TestGenerateDocstringTool_CommentFailuresPreserveSource(t *testing.T) {
	for _, tc := range []struct{ file, source, target, description string }{
		{"source.go", "package p\ntype A struct{}\ntype B struct{}\nfunc (A) Add(){}\nfunc (B) Add(){}\n", "Add", "Ambiguous"},
		{"source.js", "class A{add(){}}\nclass B{add(){}}\n", "add", "Ambiguous"},
		{"source.js", "export function add( {\n", "add", "Invalid"},
		{"source.js", "export function outer(){return function add(a,b){return a+b;};}\n", "outer.add", "Unsafe expression placement"},
		{"source.go", "package p\nfunc Add(){}\n", "Add", "+build linux"},
		{"source.go", "package p\nfunc Add(){}\n", "Add", "NUL \x00 invalid Go source"},
	} {
		t.Run(tc.file+tc.description, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": tc.file, "function_name": tc.target, "description": tc.description})
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("unsafe result: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.source {
				t.Fatal("failure changed source")
			}
		})
	}
}

func TestGenerateDocstringTool_JSExactOutputAndEscapingBudget(t *testing.T) {
	for _, source := range []string{"export const add=(a,b)=>a+b;\n", "export class Service { add(a,b){return a+b;} }\r\n"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.js")
			if err := os.WriteFile(path, []byte(source), 0640); err != nil {
				t.Fatal(err)
			}
			name := "add"
			if strings.Contains(source, "class") {
				name = "Service.add"
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": "source.js", "function_name": name, "description": "Add values.\nLiteral */ and @param are prose."}
			result, err := tool.Execute(params)
			if err != nil || !result.Success {
				t.Fatalf("baseline: %+v %v", result, err)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, delta := range []int{0, -1} {
				if err := os.WriteFile(path, []byte(source), 0640); err != nil {
					t.Fatal(err)
				}
				tool.SetMaxFileSizeBytes(int64(len(want) + delta))
				result, err := tool.Execute(params)
				if err != nil || result.Success != (delta == 0) {
					t.Fatalf("delta %d: %+v %v", delta, result, err)
				}
				expected := string(want)
				if delta < 0 {
					expected = source
					if !strings.Contains(result.Error, "too large") {
						t.Fatalf("missing budget error: %+v", result)
					}
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != expected {
					t.Fatal("budget produced wrong source")
				}
			}
			if err := os.WriteFile(path, []byte(source), 0640); err != nil {
				t.Fatal(err)
			}
			tool.SetMaxFileSizeBytes(512)
			params["description"] = strings.Repeat("@", 128)
			result, err = tool.Execute(params)
			if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
				t.Fatalf("escaping amplification bypassed budget: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != source {
				t.Fatal("escaping failure changed source")
			}
		})
	}
}

func TestGenerateDocstringTool_LargeExistingCommentsStayCompact(t *testing.T) {
	for _, language := range []string{"go", "js"} {
		t.Run(language, func(t *testing.T) {
			source := "package fixture\n// " + strings.Repeat("existing docs ", 10000) + "\nfunc Add()int{return 7}\n"
			if language == "js" {
				source = "/** " + strings.Repeat("existing docs ", 10000) + " */\nexport function Add(){return 7;}\n"
			}
			dir := t.TempDir()
			file := "source." + language
			path := filepath.Join(dir, file)
			if err := os.WriteFile(path, []byte(source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": file, "function_name": "Add", "description": "Preserve existing documentation."})
			if err != nil || !result.Success || result.Data["message"] != "Function already has documentation" {
				t.Fatalf("existing docs: %+v %v", result, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > 1024 {
				t.Fatalf("no-op retains %d-byte payload", len(encoded))
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != source {
				t.Fatal("no-op changed source")
			}
		})
	}
}
