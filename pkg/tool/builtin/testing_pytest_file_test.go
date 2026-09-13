package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsDetectPythonFile(t *testing.T) {
	root := t.TempDir()
	tool := &RunTestsTool{}
	for _, tc := range []struct{ name, want string }{
		{"test_sample.py", "pytest"},
		{"sample_test.py", "pytest"},
		{"explicit checks.py", "pytest"},
		{"sample.pyc", "unknown"},
		{"sample.txt", "unknown"},
	} {
		path := filepath.Join(root, tc.name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if got := tool.detectTestFramework(path); got != tc.want {
			t.Errorf("detect(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, name := range []string{"missing.py", "directory.py"} {
		path := filepath.Join(root, name)
		if name == "directory.py" {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if got := tool.detectTestFramework(path); got != "unknown" {
			t.Errorf("detect(%q) = %q, want unknown", name, got)
		}
	}
}

func TestRunTestsPythonFileScope(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest not installed")
	}
	t.Setenv("PYTEST_ADDOPTS", "")
	root := t.TempDir()
	dir := filepath.Join(root, "selected tests")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"selected tests/checks.py":         "import pytest\ndef test_pass():\n    assert True\ndef test_fail():\n    assert False\n@pytest.mark.skip\ndef test_skip():\n    pass\n",
		"selected tests/test_unrelated.py": "raise RuntimeError('unrequested sibling collected')\n",
		"test_unrelated.py":                "raise RuntimeError('unrequested root collected')\n",
		"empty.py":                         "value = 42\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	for _, tc := range []struct {
		name, path, pattern     string
		passed, failed, skipped int
		success                 bool
	}{
		{name: "relative passing file", path: "selected tests/checks.py", pattern: "test_pass", passed: 1, success: true},
		{name: "absolute passing file", path: filepath.Join(dir, "checks.py"), pattern: "test_pass", passed: 1, success: true},
		{name: "failed test", path: "selected tests/checks.py", pattern: "test_fail", failed: 1},
		{name: "all skipped", path: "selected tests/checks.py", pattern: "test_skip", skipped: 1},
		{name: "empty selection", path: "selected tests/checks.py", pattern: "test_missing"},
		{name: "empty file", path: "empty.py"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"path": tc.path, "pattern": tc.pattern, "timeout_seconds": float64(30)})
			if err != nil || result == nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Success != tc.success || result.Data["framework"] != "pytest" || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed || result.Data["skipped"] != tc.skipped {
				t.Fatalf("unexpected report: %+v", result)
			}
			if !tc.success && result.Error == "" {
				t.Fatal("missing verification error")
			}
			if strings.Contains(result.Data["output"].(string), "unrequested") {
				t.Fatalf("file request widened to unrelated tests: %+v", result)
			}
		})
	}
}
