package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const generatorSource = "package fixture\nfunc Add(a,b int)int{return a+b}\n"

type budgetedGenerator interface {
	Execute(map[string]any) (*Result, error)
	SetWorkDir(string)
	SetMaxFileSizeBytes(int64)
}

func TestGeneratorTools_ExactOutputBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		makeTool     func() budgetedGenerator
		params       map[string]any
	}{
		{"docstring", "source.go", func() budgetedGenerator { return &GenerateDocstringTool{} }, map[string]any{"file": "source.go", "function_name": "Add", "description": "Add returns a sum."}},
		{"test scaffold", "source_test.go", func() budgetedGenerator { return &GenerateTestTool{} }, map[string]any{"source_file": "source.go", "function_name": "Add", "test_file": "source_test.go"}},
		{"skill", ".buckley/skills/probe/SKILL.md", func() budgetedGenerator { return &CreateSkillTool{} }, map[string]any{"name": "probe", "description": "Use to check a fixture", "body": strings.Repeat("Run checks.\n", 20), "scope": "project", "overwrite": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.go")
			if err := os.WriteFile(source, []byte(generatorSource), 0640); err != nil {
				t.Fatal(err)
			}
			tool := tc.makeTool()
			tool.SetWorkDir(dir)
			result, err := tool.Execute(tc.params)
			if err != nil || !result.Success {
				t.Fatalf("baseline: %+v %v", result, err)
			}
			path := filepath.Join(dir, tc.target)
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, delta := range []int{0, -1} {
				before := []byte("existing output must survive\n")
				if tc.name == "docstring" {
					before = []byte(generatorSource)
				}
				if err := os.WriteFile(path, before, 0640); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0640); err != nil {
					t.Fatal(err)
				}
				tool.SetMaxFileSizeBytes(int64(len(want) + delta))
				result, err := tool.Execute(tc.params)
				if err != nil || result.Success != (delta == 0) {
					t.Fatalf("delta=%d result=%+v err=%v", delta, result, err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				expected := before
				if delta == 0 {
					expected = want
				} else if !strings.Contains(result.Error, "too large") {
					t.Fatalf("missing limit error: %+v", result)
				}
				if string(got) != string(expected) {
					t.Fatalf("delta=%d changed unexpected bytes", delta)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0640 {
					t.Fatal("permissions changed")
				}
			}
		})
	}
}

func TestGeneratorTools_InputAndConstructionBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, source, target string
		tool                 budgetedGenerator
		params               map[string]any
		limit                int64
	}{
		{"doc input", generatorSource, "source.go", &GenerateDocstringTool{}, map[string]any{"file": "source.go", "function_name": "Add"}, 16},
		{"test Go input", strings.Repeat("// padding\n", 100) + generatorSource, "source_test.go", &GenerateTestTool{}, map[string]any{"source_file": "source.go", "function_name": "Add"}, 512},
		{"test Python input", strings.Repeat("# padding\n", 100) + "def add(a,b):return a+b\n", "source_test.py", &GenerateTestTool{}, map[string]any{"source_file": "source.py", "function_name": "add", "test_file": "source_test.py"}, 512},
		{"doc parameter expansion", "package fixture\nfunc Add(" + strings.Repeat("a int,", 500) + "z int){}\n", "source.go", &GenerateDocstringTool{}, map[string]any{"file": "source.go", "function_name": "Add"}, 4096},
		{"doc description", generatorSource, "source.go", &GenerateDocstringTool{}, map[string]any{"file": "source.go", "function_name": "Add", "description": strings.Repeat("x", 2048)}, 512},
		{"test repeated name", generatorSource, "source_test.go", &GenerateTestTool{}, map[string]any{"source_file": "source.go", "function_name": strings.Repeat("x", 256)}, 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sourceName := "source.go"
			if strings.Contains(tc.name, "Python") {
				sourceName = "source.py"
			}
			path := filepath.Join(dir, sourceName)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tc.tool.SetWorkDir(dir)
			tc.tool.SetMaxFileSizeBytes(tc.limit)
			result, err := tc.tool.Execute(tc.params)
			if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
				t.Fatalf("budget bypassed: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.source {
				t.Fatal("source changed on rejection")
			}
			if tc.target != sourceName {
				if _, err := os.Stat(filepath.Join(dir, tc.target)); !os.IsNotExist(err) {
					t.Fatal("rejection created an output")
				}
			}
		})
	}
}

func TestCreateSkillTool_BudgetDoesNotCreateDirectories(t *testing.T) {
	for _, body := range []string{"short body", strings.Repeat("Run tests.\n", 1000)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			dir := t.TempDir()
			tool := &CreateSkillTool{}
			tool.SetWorkDir(dir)
			tool.SetMaxFileSizeBytes(32)
			result, err := tool.Execute(map[string]any{"name": "probe", "description": "Use for a fixture", "body": body, "scope": "project"})
			if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
				t.Fatalf("skill budget bypassed: %+v %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".buckley")); !os.IsNotExist(err) {
				t.Fatal("budget failure created skill directories")
			}
		})
	}
}

func TestGenerateDocstringTool_LiteralFunctionName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	if err := os.WriteFile(path, []byte(generatorSource), 0640); err != nil {
		t.Fatal(err)
	}
	tool := &GenerateDocstringTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"file": "source.go", "function_name": "["})
	if err != nil || result.Success || result.Error == "" {
		t.Fatalf("invalid name accepted: %+v %v", result, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != generatorSource {
		t.Fatal("invalid function name changed source")
	}
}

func TestGenerateTestTool_AutoPathConfinedToSourceDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tc := range []struct{ source, target, content string }{{"source.go", "source_test.go", generatorSource}, {"source.py", "test_source.py", "def Add(a,b):return a+b\n"}, {"source.js", "source.test.js", "export function Add(a,b){return a+b;}\n"}} {
		t.Run(tc.source, func(t *testing.T) {
			dir := t.TempDir()
			nested := filepath.Join(dir, "nested")
			if err := os.MkdirAll(nested, 0700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(nested, tc.source)
			if err := os.WriteFile(source, []byte(tc.content), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateTestTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"source_file": filepath.Join("nested", tc.source), "function_name": "Add"})
			want := filepath.Join(nested, tc.target)
			if err != nil || !result.Success || result.Data["test_file"] != want {
				t.Fatalf("wrong generated path: %+v %v", result, err)
			}
			if _, err := os.Stat(want); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(tc.target); !os.IsNotExist(err) {
				t.Fatal("generated file escaped into process directory")
			}
		})
	}
}

func TestGenerateTestTool_AutoPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.go"), []byte(generatorSource), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "source_test.go")); err != nil {
		t.Fatal(err)
	}
	tool := &GenerateTestTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"source_file": "source.go", "function_name": "Add"})
	if err != nil || result.Success || !strings.Contains(result.Error, "escapes") {
		t.Fatalf("symlink escape accepted: %+v %v", result, err)
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "unchanged" {
		t.Fatal("outside target changed")
	}
}
