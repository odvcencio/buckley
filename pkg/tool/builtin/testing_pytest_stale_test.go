package builtin

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunTestsPytestStaleBytecode(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest not installed")
	}
	t.Setenv("PYTHONDONTWRITEBYTECODE", "")
	t.Setenv("PYTHONPYCACHEPREFIX", "")
	t.Setenv("PYTEST_ADDOPTS", "")
	for _, tc := range []struct {
		name, target, before, after string
		primePass, wantPass         bool
	}{
		{name: "module cached pass current failure", target: "math_ops.py", before: "def add(a, b):\n    return a + b\n", after: "def add(a, b):\n    return a - b\n", primePass: true},
		{name: "module cached failure current pass", target: "math_ops.py", before: "def add(a, b):\n    return a - b\n", after: "def add(a, b):\n    return a + b\n", wantPass: true},
		{name: "test cached pass current failure", target: "test_math_ops.py", before: "def test_add():\n    assert 1 == 1\n", after: "def test_add():\n    assert 1 == 2\n", primePass: true},
		{name: "test cached failure current pass", target: "test_math_ops.py", before: "def test_add():\n    assert 1 == 2\n", after: "def test_add():\n    assert 1 == 1\n", wantPass: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			testPath := filepath.Join(root, "test_math_ops.py")
			if err := os.WriteFile(testPath, []byte("from math_ops import add\ndef test_add():\n    assert add(2, 3) == 5\n"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, tc.target)
			stamp := time.Unix(1700000000, 0)
			if len(tc.before) != len(tc.after) {
				t.Fatal("fixture must preserve source length")
			}
			if err := os.WriteFile(target, []byte(tc.before), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(target, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			prime := exec.Command("pytest", "-q", "-p", "no:cacheprovider", testPath)
			prime.Dir = root
			output, primeErr := prime.CombinedOutput()
			if (primeErr == nil) != tc.primePass {
				t.Fatalf("prime err=%v output=%s", primeErr, output)
			}
			if primeErr != nil {
				if exit, ok := primeErr.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
					t.Fatalf("prime was not a test failure: %v\n%s", primeErr, output)
				}
			}
			cached := map[string][]byte{}
			if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if filepath.Ext(path) == ".pyc" {
					body, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					cached[path] = body
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(cached) == 0 {
				t.Fatal("priming did not create bytecode")
			}
			if err := os.WriteFile(target, []byte(tc.after), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(target, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			runner := &RunTestsTool{}
			runner.SetWorkDir(root)
			result, err := runner.Execute(map[string]any{"path": "test_math_ops.py", "timeout_seconds": float64(30)})
			if err != nil || result == nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			passed, failed := 0, 1
			if tc.wantPass {
				passed, failed = 1, 0
			}
			if result.Success != tc.wantPass || result.Data["passed"] != passed || result.Data["failed"] != failed {
				t.Errorf("verification used stale code: %+v, want pass=%v", result, tc.wantPass)
			}
			for path, before := range cached {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("existing cache changed: %s err=%v", path, err)
				}
			}
		})
	}
}
