package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateDocstringTool_PythonRuntimeDocumentation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	description := "Add two numbers, including \"\"\"quoted\"\"\" text.\nKeep \\paths and a literal \\n safe."
	for _, tc := range []struct{ name, source, target, reference, call string }{
		{"function", "# A comment is not a docstring.\ndef add(a,b):\n    return a+b\n", "add", "m.add", "m.add(3,4)"},
		{"inline", "def add(a,b): x=a+b; return x # keep comment\n", "add", "m.add", "m.add(3,4)"},
		{"CRLF inline", "def add(a,b): return a+b\r\n", "add", "m.add", "m.add(3,4)"},
		{"decorated async multiline", "class Service:\n    @staticmethod\n    async def add(\n        a: int,\n        b: int = (3, 4)[1],\n    ) -> int:\n        # retain this comment\n        return a+b\n", "Service.add", "m.Service.add", "asyncio.run(m.Service.add(3,4))"},
		{"tabs", "class Service:\n\tdef add(self,a,b):\n\t\treturn a+b\n", "Service.add", "m.Service.add", "m.Service().add(3,4)"},
		{"nested", "def outer():\n    def inner(a,b):\n        return a+b\n    return inner\n", "outer.inner", "m.outer()", "m.outer()(3,4)"},
		{"fake definition in string", "fake = '''\ndef add(a,b):\n    return 0\n'''\ndef add(a,b):\n    return a+b\n", "add", "m.add", "m.add(3,4)"},
		{"byte expression", "def add(a,b):\n    b'not documentation'\n    return a+b\n", "add", "m.add", "m.add(3,4)"},
		{"fstring expression", "def add(a,b):\n    f'not documentation {a}'\n    return a+b\n", "add", "m.add", "m.add(3,4)"},
		{"no final newline", "def add(a,b): return a+b", "add", "m.add", "m.add(3,4)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "subject.py")
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": "subject.py", "function_name": tc.target, "description": description})
			if err != nil || !result.Success {
				t.Fatalf("generate: %+v %v", result, err)
			}
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tc.source, "\r\n") && strings.Contains(strings.ReplaceAll(string(source), "\r\n", ""), "\n") {
				t.Fatal("introduced bare LF into CRLF source")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatal("permissions changed")
			}
			script := "import ast,asyncio,importlib.util,inspect,pathlib,sys\np=pathlib.Path(sys.argv[1]);source=p.read_text();ast.parse(source)\nspec=importlib.util.spec_from_file_location('subject',p);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)\nfn=" + tc.reference + "\nassert inspect.getdoc(fn).startswith(sys.argv[2]), repr(fn.__doc__)\nassert 'a: TODO' in fn.__doc__ and 'b: TODO' in fn.__doc__,fn.__doc__\nassert 'self: TODO' not in fn.__doc__\nassert " + tc.call + " == 7\n"
			cmd := exec.Command(python, "-B", "-c", script, path, description)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Python runtime: %v\n%s\n%s", err, out, source)
			}
			again, err := tool.Execute(map[string]any{"file": "subject.py", "function_name": tc.target, "description": "Do not overwrite existing documentation."})
			if err != nil || !again.Success || again.Data["message"] != "Function already has documentation" {
				t.Fatalf("idempotence: %+v %v", again, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(source) {
				t.Fatal("second call changed existing documentation")
			}
		})
	}
}

func TestGenerateDocstringTool_PythonExistingDocumentationPreserved(t *testing.T) {
	for _, literal := range []string{`"single line"`, `'''multi
    line'''`, `r"raw\path"`, `u'unicode'`, `("first" " second")`, `("first" # comment
     " second")`} {
		t.Run(literal, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "subject.py")
			source := "def add(a,b):\n    # before real doc\n    " + literal + "\n    return a+b\n"
			if err := os.WriteFile(path, []byte(source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(map[string]any{"file": "subject.py", "function_name": "add"})
			if err != nil || !result.Success || result.Data["message"] != "Function already has documentation" {
				t.Fatalf("existing: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != source {
				t.Fatal("existing docstring changed")
			}
		})
	}
}

func TestGenerateDocstringTool_PythonRejectionPreservesSource(t *testing.T) {
	for _, tc := range []struct {
		name, source, target, description string
		limit                             int64
	}{
		{"ambiguous", "class A:\n    def add(self): return 1\nclass B:\n    def add(self): return 2\n", "add", "", 0},
		{"invalid source", "def add(a,b)\n    return a+b\n", "add", "", 0},
		{"missing target", "def subtract(a,b): return a-b\n", "add", "", 0},
		{"output budget", "def add(a,b): return a+b\n", "add", "", 64},
		{"escaped description budget", "def add(a,b): return a+b\n", "add", strings.Repeat("\"", 200), 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "subject.py")
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			if tc.limit > 0 {
				tool.SetMaxFileSizeBytes(tc.limit)
			}
			result, err := tool.Execute(map[string]any{"file": "subject.py", "function_name": tc.target, "description": tc.description})
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("rejection: %+v %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.source {
				t.Fatal("rejection changed source")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatal("rejection changed permissions")
			}
		})
	}
}

func TestGenerateDocstringTool_PythonExactOutputBudget(t *testing.T) {
	for _, source := range []string{"def add(a,b): return a+b\n", "def add(a,b):\r\n    return a+b\r\n"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "subject.py")
			tool := &GenerateDocstringTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": "subject.py", "function_name": "add"}
			if err := os.WriteFile(path, []byte(source), 0640); err != nil {
				t.Fatal(err)
			}
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
				got, err := os.ReadFile(path)
				if err != nil || string(got) != expected {
					t.Fatalf("unexpected bytes for delta %d", delta)
				}
			}
		})
	}
}
