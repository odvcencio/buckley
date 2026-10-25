package builtin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsTool_MissingPathDoesNotLaunchCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/missing\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := execCommandContext
	t.Cleanup(func() { execCommandContext = previous })
	execCommandContext = func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("missing target launched a test command")
		return nil
	}
	for _, path := range []string{"./examples/turbofan", "missing_test.go", "pkg/missing/..."} {
		t.Run(path, func(t *testing.T) {
			tool := &RunTestsTool{}
			tool.SetWorkDir(root)
			result, err := tool.ExecuteWithContext(t.Context(), map[string]any{"path": path})
			if err != nil || result == nil || result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Data[VerificationStatusKey] != VerificationStatusUnavailable || !strings.Contains(result.Error, "does not exist") || !strings.Contains(result.Error, "Nothing ran") {
				t.Fatalf("missing-path diagnostic=%+v", result)
			}
			if VerificationUnavailableReason(result) == "" {
				t.Fatal("missing target received failure evidence")
			}
			if _, reported := result.Data["exit_code"]; reported {
				t.Fatal("unstarted command received an exit code")
			}
		})
	}
}

func TestRunTestsTool_GoPackagePatternsRunRealTests(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace...literal")
	for path, content := range map[string]string{
		"go.mod":                 "module example.invalid/patterns\n\ngo 1.26\n",
		"pkg/a/api/real_test.go": "package api\nimport \"testing\"\nfunc TestPattern(t *testing.T) {}\n",
		"pkg/b/api/real_test.go": "package api\nimport \"testing\"\nfunc TestPattern(t *testing.T) {}\n",
	} {
		name := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"./...", "pkg/...", "pkg/.../api", "pkg/a..."} {
		t.Run(path, func(t *testing.T) {
			tool := &RunTestsTool{}
			tool.SetWorkDir(root)
			result, err := tool.ExecuteWithContext(t.Context(), map[string]any{"path": path, "pattern": "^TestPattern$"})
			if err != nil || result == nil || !result.Success {
				t.Fatalf("selector=%s result=%+v err=%v", path, result, err)
			}
			want := 2
			if path == "pkg/a..." {
				want = 1
			}
			if result.Data["framework"] != "go" || result.Data["passed"] != want || result.Data["exit_code"] != 0 {
				t.Fatalf("selector report=%+v want %d actual passes", result, want)
			}
		})
	}
}

func TestRunTestsTool_PathDiagnosticRetainsUnavailableWithinBudget(t *testing.T) {
	tool := &RunTestsTool{}
	tool.SetWorkDir(t.TempDir())
	tool.maxOutputBytes = 512
	result, err := tool.ExecuteWithContext(t.Context(), map[string]any{"path": strings.Repeat("x", 4000)})
	if err != nil || result == nil || result.Success || result.Data[VerificationStatusKey] != VerificationStatusUnavailable {
		t.Fatalf("bounded unavailable result=%+v err=%v", result, err)
	}
	body, err := json.Marshal(result)
	if err != nil || len(body) > 512 {
		t.Fatalf("diagnostic bytes=%d err=%v", len(body), err)
	}
}
