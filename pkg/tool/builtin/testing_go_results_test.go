package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsGoStructuredCounts(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go is required")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":         "module example.com/test-counts\n\ngo 1.26.0\n",
		"counts_test.go": "package counts\nimport \"testing\"\nfunc TestPass(t *testing.T) { t.Log(\"--- FAIL: fake result\") }\nfunc TestSkip(t *testing.T) { t.Skip(\"deliberate\") }\nfunc TestFail(t *testing.T) { t.Fatal(\"real failure\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	for _, tc := range []struct {
		name, pattern           string
		passed, failed, skipped int
		success                 bool
	}{
		{"pass and skip", "^Test(Pass|Skip)$", 1, 0, 1, true},
		{"cached repeat", "^Test(Pass|Skip)$", 1, 0, 1, true},
		{"failed test", "^TestFail$", 0, 1, 0, false},
		{"all skipped", "^TestSkip$", 0, 0, 1, false},
		{"empty selection", "^TestMissing$", 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"path": ".", "pattern": tc.pattern})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != tc.success || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed || result.Data["skipped"] != tc.skipped {
				t.Fatalf("unexpected test report: %+v", result)
			}
			if tc.pattern == "^TestMissing$" && !strings.Contains(result.Error, "no tests") {
				t.Fatalf("missing selection diagnostic: %+v", result)
			}
			if strings.Contains(result.Data["output"].(string), "\"Action\":") {
				t.Fatal("raw JSON leaked into readable output")
			}
		})
	}
}

func TestParseGoTestOutputRequiresPackageCompletion(t *testing.T) {
	report := parseGoTestOutput("{\"Action\":\"start\",\"Package\":\"p\"}\n{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestA\"}\n")
	if report.complete || report.passed != 1 {
		t.Fatalf("incomplete stream = %+v", report)
	}
}

func TestRunTestsGoBuildFailure(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go is required")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":         "module example.com/build-failure\n\ngo 1.26.0\n",
		"broken_test.go": "package broken\nimport \"testing\"\nfunc TestBroken(t *testing.T) { missingSymbol() }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	result, err := tool.Execute(map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Data["exit_code"] == 0 || result.Error == "" {
		t.Fatalf("build failure incorrectly verified: %+v", result)
	}
	output := result.Data["output"].(string)
	if !strings.Contains(output, "missingSymbol") || strings.Contains(output, "\"Action\":") {
		t.Fatalf("build diagnostics not readable: %s", output)
	}
}

func TestRunTestsAbridgedFailureRetainsExitStatus(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/abridged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := execCommandContext
	t.Cleanup(func() { execCommandContext = original })
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf '%s' \"$1\"; exit 1", "test", strings.Repeat("compiler error\n", 500))
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	result, err := tool.Execute(map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !result.ShouldAbridge || result.DisplayData["exit_code"] != 1 || !strings.Contains(result.DisplayData["summary"].(string), "FAILED") {
		t.Fatalf("abridged output lost failure: %+v", result)
	}
}
