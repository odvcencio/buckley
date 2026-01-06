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

	_, exitCode, duration, err := tool.runTestsForFramework(ctx, "go", ".", "", false, false)
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
	_, _, _, err := tool.runTestsForFramework(context.Background(), "unknown", ".", "", false, false)
	if err == nil {
		t.Fatalf("expected error for unsupported framework")
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
	tool := &RunTestsTool{}
	output := `--- PASS: TestOne
--- FAIL: TestTwo
--- SKIP: TestThree
--- PASS: TestFour`
	passed, failed, skipped := tool.parseGoTestResults(output)
	if passed != 2 || failed != 1 || skipped != 1 {
		t.Fatalf("unexpected counts: pass=%d fail=%d skip=%d", passed, failed, skipped)
	}
}
