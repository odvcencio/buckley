package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateTestTool_GoScaffoldsRequireImplementation(t *testing.T) {
	for _, label := range []string{"", "add", "(*Service).Add", "bad\"\nlabel"} {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{
				"go.mod": "module example.com/scaffold\n\ngo 1.25\n",
				"sum.go": "// package misleading\npackage 数学\nfunc Add(a,b int)int{return a-b}\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			generator := &GenerateTestTool{}
			generator.SetWorkDir(dir)
			result, err := generator.Execute(map[string]any{"source_file": "sum.go", "function_name": label})
			if err != nil || !result.Success {
				t.Fatalf("generate: %+v %v", result, err)
			}
			for _, data := range []map[string]any{result.Data, result.DisplayData} {
				if data["status"] != "scaffold" || data["requires_implementation"] != true {
					t.Fatalf("missing implementation warning: %+v", data)
				}
			}
			runner := &RunTestsTool{}
			runner.SetWorkDir(dir)
			result, err = runner.Execute(map[string]any{})
			if err != nil || result.Success || result.Data["passed"] != 0 || result.Data["skipped"] != 1 || !strings.Contains(result.Error, "skipped every test") {
				t.Fatalf("scaffold verified behavior: %+v %v", result, err)
			}
			if label != "" {
				return
			}
			path := filepath.Join(dir, "sum_test.go")
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			implemented := strings.Replace(string(content), "t.Skip(\""+scaffoldSkipReason+"\")", "if got := Add(3, 4); got != 7 { t.Fatalf(\"Add(3,4)=%d, want 7\", got) }", 1)
			if err := os.WriteFile(path, []byte(implemented), 0600); err != nil {
				t.Fatal(err)
			}
			result, err = runner.Execute(map[string]any{})
			if err != nil || result.Success || result.Data["failed"] != 1 {
				t.Fatalf("assertion missed bug: %+v %v", result, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sum.go"), []byte("package 数学\nfunc Add(a,b int)int{return a+b}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err = runner.Execute(map[string]any{})
			if err != nil || !result.Success || result.Data["passed"] != 1 || result.Data["skipped"] != 0 {
				t.Fatalf("fixed implementation not verified: %+v %v", result, err)
			}
		})
	}
}

func TestGenerateTestTool_InvalidPackagePreservesOutput(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"sum.go": "// package fake\nfunc Add() {}\n", "sum_test.go": "existing output\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &GenerateTestTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"source_file": "sum.go"})
	if err != nil || result.Success || !strings.Contains(result.Error, "read source package") {
		t.Fatalf("invalid package accepted: %+v %v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sum_test.go"))
	if err != nil || string(got) != "existing output\n" {
		t.Fatal("rejection changed existing output")
	}
}

func TestGenerateTestTool_PythonScaffoldsCollectAndSkip(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest is not installed")
	}
	for _, label := range []string{"", "add", "Service.add\"\n", "数字"} {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "sum.py"), []byte("raise RuntimeError('unfinished scaffold must not import subject')\n"), 0600); err != nil {
				t.Fatal(err)
			}
			generator := &GenerateTestTool{}
			generator.SetWorkDir(dir)
			result, err := generator.Execute(map[string]any{"source_file": "sum.py", "function_name": label})
			if err != nil || !result.Success {
				t.Fatalf("generate: %+v %v", result, err)
			}
			cmd := exec.Command("pytest", "-q", "-p", "no:cacheprovider", "test_sum.py")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "1 skipped") {
				t.Fatalf("pytest: %v\n%s", err, out)
			}
		})
	}
}

func TestGenerateTestTool_JavascriptScaffoldsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	for _, extension := range []string{"js", "ts"} {
		for _, label := range []string{"", "add", "Service.add\"\n'); throw Error('injected'); //"} {
			t.Run(extension+label, func(t *testing.T) {
				dir := t.TempDir()
				source := "sum'\"." + extension
				if err := os.WriteFile(filepath.Join(dir, source), []byte("throw Error('unfinished scaffold must not import subject');\n"), 0600); err != nil {
					t.Fatal(err)
				}
				generator := &GenerateTestTool{}
				generator.SetWorkDir(dir)
				result, err := generator.Execute(map[string]any{"source_file": source, "function_name": label, "test_file": "sum.test.js"})
				if err != nil || !result.Success {
					t.Fatalf("generate: %+v %v", result, err)
				}
				cmd := exec.Command(node, "--check", filepath.Join(dir, "sum.test.js"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("node: %v\n%s", err, out)
				}
			})
		}
	}
}
