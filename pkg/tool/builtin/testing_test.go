package builtin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunTestsToolTimeoutHonored(t *testing.T) {
	t.Cleanup(func() { execCommandContext = exec.CommandContext })

	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "sleep 2")
	}

	tool := &RunTestsTool{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, exitCode, duration, _, err := tool.runTestsForFramework(ctx, "go", ".", "", false, false)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code on timeout")
	}
	if duration > 0.5 {
		t.Fatalf("context cancellation should return quickly, got duration %.2f", duration)
	}
}

func TestRunTestsToolUnsupportedFramework(t *testing.T) {
	tool := &RunTestsTool{}
	_, _, _, _, err := tool.runTestsForFramework(context.Background(), "unknown", ".", "", false, false)
	if err == nil {
		t.Fatalf("expected error for unsupported framework")
	}
}

func TestGenerateTestToolUsesConfiguredWorkDir(t *testing.T) {
	project := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sample.go"), []byte("package sample\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(other); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	tool := &GenerateTestTool{}
	tool.SetWorkDir(project)
	result, err := tool.Execute(map[string]any{"source_file": "sample.go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("result = %+v, want success", result)
	}
	if _, err := os.Stat(filepath.Join(project, "sample_test.go")); err != nil {
		t.Fatalf("project test file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "sample_test.go")); !os.IsNotExist(err) {
		t.Fatalf("test file written in process cwd, stat err=%v", err)
	}
}

func TestLocalGoTestPath(t *testing.T) {
	tests := map[string]string{
		".":          ".",
		"./server":   "./server",
		"server":     "./server",
		"server/...": "./server/...",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			if got := localGoTestPath(input); got != want {
				t.Fatalf("localGoTestPath(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestDetectTestFrameworkGoMod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.25"), 0o644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}
	tool := &RunTestsTool{}
	if got := tool.detectTestFramework(dir); got != "go" {
		t.Fatalf("expected go framework, got %s", got)
	}
}

func TestParseGoTestResults(t *testing.T) {
	output := `{"Action":"pass","Package":"p","Test":"TestOne"}
{"Action":"fail","Package":"p","Test":"TestTwo"}
{"Action":"skip","Package":"p","Test":"TestThree"}
{"Action":"pass","Package":"p","Test":"TestFour"}
{"Action":"fail","Package":"p"}`
	report := parseGoTestOutput(output)
	if !report.complete || report.passed != 2 || report.failed != 1 || report.skipped != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}
