package tooloutcome

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestObservation_PytestWorkspace(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest not installed")
	}
	t.Setenv("PYTHONDONTWRITEBYTECODE", "")
	t.Setenv("PYTHONPYCACHEPREFIX", "")
	t.Setenv("PYTEST_ADDOPTS", "")
	for _, tc := range []struct {
		name, mutation string
		changed        bool
	}{
		{name: "fresh read-only tests"},
		{name: "tracked source edit", mutation: "    Path('tracked.txt').write_text('changed by test')\n", changed: true},
		{name: "explicit cache directory write", mutation: "    Path('__pycache__').mkdir(exist_ok=True)\n    Path('__pycache__/source.py').write_text('new source')\n", changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newToolOutcomeGitRepo(t)
			for name, content := range map[string]string{
				"math_ops.py":      "def add(a, b):\n    return a + b\n",
				"test_math_ops.py": "from pathlib import Path\nfrom math_ops import add\ndef test_add():\n    assert add(2, 3) == 5\n" + tc.mutation,
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			metadata := tool.ToolMetadata{Impact: tool.ImpactReadOnly, Verification: true}
			ctx := context.Background()
			observation := BeginWithMetadata(ctx, root, metadata)
			runner := &builtin.RunTestsTool{}
			runner.SetWorkDir(root)
			result, err := runner.ExecuteWithContext(ctx, map[string]any{"path": "test_math_ops.py", "timeout_seconds": float64(30)})
			if err != nil || result == nil || !result.Success || result.Data["passed"] != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			outcome := observation.Finish(ctx, agentloop.ToolOutcome{Content: "passed", Success: result.Success}, metadata, result, err)
			if outcome.StateObservationFailed || !outcome.StateObserved || !outcome.VerificationPassed || outcome.StateChanged != tc.changed {
				t.Fatalf("outcome=%+v, want changed=%v", outcome, tc.changed)
			}
			if strings.Contains(outcome.Content, "[Buckley verification]") != tc.changed {
				t.Fatalf("incorrect verification warning: %s", outcome.Content)
			}
			if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if filepath.Ext(path) == ".pyc" {
					t.Errorf("test run created bytecode: %s", path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !tc.changed {
				if _, err := os.Stat(filepath.Join(root, "__pycache__")); !os.IsNotExist(err) {
					t.Fatalf("unexpected bytecode cache directory: %v", err)
				}
			}
		})
	}
	if os.Getenv("PYTHONDONTWRITEBYTECODE") != "" {
		t.Fatal("test runner mutated process-wide bytecode setting")
	}
}
