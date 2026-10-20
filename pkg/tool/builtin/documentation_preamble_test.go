package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateDocstringTool_LargePreamblePreservesDocumentation(t *testing.T) {
	large := "/** " + strings.Repeat("Existing docs. ", 6000) + " */"
	for _, tc := range []struct {
		name, file, source, target string
		line                       int
	}{
		{"export", "source.js", large + "\nexport function add(){return 7;}\n", "add", 1},
		{"async default", "source.js", "// License\n" + large + "\n// Note\nexport default async function add(){return 7;}\n", "add", 2},
		{"multiple comments", "source.ts", large + "\n/* ordinary */\n/** Closest doc. */\nexport function add<T>(value:T):T{return value;}\n", "add", 3},
		{"CRLF", "source.ts", "\r\n" + large + "\r\n \texport function add():number{return 7;}\r\n", "add", 2},
		{"unicode", "source.js", large + "\nfunction café(){return 7;}\n", "café", 1},
		{"overload", "source.ts", large + "\nexport function add(value:number):number;\nexport function add(value:number){return value;}\n", "add", 1},
		{"arrow fallback", "source.ts", large + "\nexport const add=(value:number)=>value;\n", "add", 1},
		{"method fallback", "source.js", large + "\nclass Service{/** Method doc. */add(){return 7;}}\n", "Service.add", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": tc.file, "function_name": tc.target, "description": "Preserve existing docs."})
			if err != nil || !result.Success || result.Data["message"] != "Function already has documentation" || result.Data["line"] != tc.line {
				t.Fatalf("existing docs: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.source {
				t.Fatal("existing docs changed")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatalf("mode changed: %v %v", info, err)
			}
		})
	}
}

func TestGenerateDocstringTool_LargePreambleDoesNotHideInvalidCode(t *testing.T) {
	large := "/** " + strings.Repeat("Existing docs. ", 6000) + " */\n"
	for _, tc := range []struct{ name, source, target string }{
		{"broken suffix", large + "function add(){return 7;}\nconst broken = ;\n", "add"},
		{"ambiguous", large + "function add(){}\nclass Service{add(){}}\n", "add"},
		{"missing target", large + "function added(){}\n", "add"},
		{"line separator", "// " + strings.Repeat("License ", 10000) + "\u2028const broken = ;\n/** Doc. */\nfunction add(){}\n", "add"},
		{"unterminated comment", large + "/* function add(){}\n", "add"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.js")
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": "source.js", "function_name": tc.target})
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("invalid source accepted: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.source {
				t.Fatal("failure changed source")
			}
		})
	}
}
