package reviewsandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPermissionArgsEnforceReadOnlySnapshotPrivateTempAndNoNetwork(t *testing.T) {
	runtimeDir := t.TempDir()
	extraRead := t.TempDir()
	args := PermissionArgsWithReadRoots("/usr/bin/true", runtimeDir, extraRead)
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		`default_permissions="` + PermissionProfileName + `"`,
		`":workspace_roots" = { "." = "read" }`,
		`":tmpdir" = "write"`,
		`network={ enabled = false }`,
		`shell_environment_policy={ inherit = "none"`,
		`allow_login_shell=false`,
		filepath.Clean(extraRead),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("permission args omitted %q:\n%s", want, joined)
		}
	}
}

func TestTrustedExecutableResolutionIgnoresHostilePath(t *testing.T) {
	hostile := t.TempDir()
	fakeGo := filepath.Join(hostile, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", hostile)

	resolved, err := trustedLookPath("go")
	if err != nil {
		t.Skipf("trusted Go toolchain is not installed: %v", err)
	}
	if filepath.Clean(resolved) == filepath.Clean(fakeGo) {
		t.Fatalf("trusted lookup selected hostile PATH executable %q", resolved)
	}
	if strings.Contains(ToolEnvironment(t.TempDir())["PATH"], hostile) {
		t.Fatal("sandbox child PATH inherited hostile ambient PATH")
	}
	if strings.Contains(strings.Join(PermissionArgs("/usr/bin/true", t.TempDir()), "\n"), hostile) {
		t.Fatal("permission profile granted read access to hostile ambient PATH")
	}
}

func TestExecutorReportsPassWithExactArgvAndNormalizedScope(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "go.mod"), []byte("module example.test/review\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	executor := testExecutor(t)
	var invocations []commandInvocation
	executor.run = func(_ context.Context, invocation commandInvocation, _ int) (commandOutput, error) {
		invocations = append(invocations, invocation)
		return commandOutput{ExitCode: 0, Stdout: "ok"}, nil
	}
	result := executor.Verify(context.Background(), Request{
		SnapshotRoot: root,
		Kind:         KindTest,
		Language:     LanguageGo,
		Path:         "pkg/.",
		Pattern:      "  TestFocused  ",
		Timeout:      time.Second,
	})

	if result.Status != StatusPass || result.ExitCode != 0 {
		t.Fatalf("verification = %#v", result)
	}
	if result.Path != "pkg" || result.Pattern != "TestFocused" {
		t.Fatalf("normalized scope = path %q pattern %q", result.Path, result.Pattern)
	}
	wantArgv := []string{"/usr/local/go/bin/go", "test", "-count=1", "-v", "-run", "TestFocused", "."}
	if !reflect.DeepEqual(result.Argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", result.Argv, wantArgv)
	}
	if len(invocations) != 2 {
		t.Fatalf("invocations = %d, want sandbox preflight + verification", len(invocations))
	}
	actual := invocations[1].Args
	separator := indexOf(actual, "--")
	if separator < 0 || !reflect.DeepEqual(actual[separator+1:], wantArgv) {
		t.Fatalf("sandbox did not receive exact verification argv: %#v", actual)
	}
	if strings.Contains(strings.Join(invocations[1].Env, "\n"), "OPENAI_API_KEY") {
		t.Fatal("restricted command environment leaked credentials")
	}
}

func TestExecutorClassifiesCommandFailureAndSandboxUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/review\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("command failure", func(t *testing.T) {
		executor := testExecutor(t)
		calls := 0
		executor.run = func(context.Context, commandInvocation, int) (commandOutput, error) {
			calls++
			if calls == 1 {
				return commandOutput{ExitCode: 0}, nil
			}
			return commandOutput{ExitCode: 2, Stderr: "compile failed"}, nil
		}
		result := executor.Verify(context.Background(), Request{SnapshotRoot: root, Kind: KindBuild, Language: LanguageGo})
		if result.Status != StatusFail || result.ExitCode != 2 {
			t.Fatalf("failure = %#v", result)
		}
	})

	t.Run("sandbox unavailable", func(t *testing.T) {
		executor := testExecutor(t)
		executor.run = func(context.Context, commandInvocation, int) (commandOutput, error) {
			return commandOutput{ExitCode: -1}, errors.New("sandbox helper missing")
		}
		result := executor.Verify(context.Background(), Request{SnapshotRoot: root, Kind: KindBuild, Language: LanguageGo})
		if result.Status != StatusUnavailable || result.ExitCode != -1 {
			t.Fatalf("unavailable = %#v", result)
		}
	})

	t.Run("successful command with no tests", func(t *testing.T) {
		executor := testExecutor(t)
		calls := 0
		executor.run = func(context.Context, commandInvocation, int) (commandOutput, error) {
			calls++
			if calls == 1 {
				return commandOutput{ExitCode: 0}, nil
			}
			return commandOutput{ExitCode: 0, Stdout: "? example.test/review [no test files]"}, nil
		}
		result := executor.Verify(context.Background(), Request{SnapshotRoot: root, Kind: KindTest, Language: LanguageGo})
		if result.Status != StatusFail || result.ExitCode != 0 || !strings.Contains(result.Error, "without executing tests") {
			t.Fatalf("no-test result = %#v", result)
		}
	})
}

func TestResolveSnapshotDirectoryRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveSnapshotDirectory(root, "escape"); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func testExecutor(t *testing.T) *Executor {
	t.Helper()
	executor := NewExecutorWithCodexCommand("/usr/bin/true")
	executor.lookPath = func(name string) (string, error) {
		switch name {
		case "go":
			return "/usr/local/go/bin/go", nil
		case "true":
			return "/usr/bin/true", nil
		default:
			return "", errors.New("unexpected executable: " + name)
		}
	}
	return executor
}

func indexOf(items []string, target string) int {
	for index, item := range items {
		if item == target {
			return index
		}
	}
	return -1
}
