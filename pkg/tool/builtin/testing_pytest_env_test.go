package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunTestsBytecodeEnvironment(t *testing.T) {
	t.Setenv("PYTHONDONTWRITEBYTECODE", "")
	t.Setenv("PYTHONPYCACHEPREFIX", "ambient-prefix")
	t.Setenv("BUCKLEY_TEST_INHERITED", "inherited")
	t.Setenv("BUCKLEY_TEST_OVERRIDE", "ambient")
	original := execCommandContext
	t.Cleanup(func() { execCommandContext = original })
	var expectedPrefix string
	seen := map[string]bool{}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		expectedPrefix = "ambient-prefix"
		if name == "pytest" {
			if len(args) < 2 || args[0] != "--junitxml" {
				t.Fatalf("pytest args=%v", args)
			}
			expectedPrefix = filepath.Join(filepath.Dir(args[1]), "pycache")
			if seen[expectedPrefix] {
				t.Fatalf("reused bytecode prefix: %s", expectedPrefix)
			}
			seen[expectedPrefix] = true
			if info, err := os.Stat(filepath.Dir(expectedPrefix)); err != nil || !info.IsDir() {
				t.Fatalf("missing report directory: %v", err)
			}
		}
		return exec.CommandContext(ctx, "sh", "-c", `test "$PYTHONPYCACHEPREFIX" = "$1" || exit 42; printf '%s|%s|%s' "$BUCKLEY_TEST_INHERITED" "$BUCKLEY_TEST_OVERRIDE" "$PYTHONDONTWRITEBYTECODE"`, "env-check", expectedPrefix)
	}
	for _, tc := range []struct {
		name, framework, want string
		env                   map[string]string
	}{
		{name: "pytest inherited", framework: "pytest", want: "inherited|ambient|1"},
		{name: "pytest configured", framework: "pytest", env: map[string]string{"BUCKLEY_TEST_OVERRIDE": "configured", "PYTHONDONTWRITEBYTECODE": "", "PYTHONPYCACHEPREFIX": "configured-prefix"}, want: "inherited|configured|1"},
		{name: "go unchanged", framework: "go", env: map[string]string{"BUCKLEY_TEST_OVERRIDE": "configured"}, want: "inherited|configured|"},
		{name: "cargo unchanged", framework: "cargo", want: "inherited|ambient|"},
		{name: "jest unchanged", framework: "jest", want: "inherited|ambient|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &RunTestsTool{}
			runner.SetWorkDir(t.TempDir())
			runner.SetEnv(tc.env)
			output, exitCode, _, _, err := runner.runTestsForFramework(context.Background(), tc.framework, ".", "", false, false)
			if err != nil || exitCode != 0 || output != tc.want {
				t.Fatalf("output=%q exit=%d err=%v, want %q", output, exitCode, err, tc.want)
			}
			if tc.framework == "pytest" {
				if _, err := os.Stat(filepath.Dir(expectedPrefix)); !os.IsNotExist(err) {
					t.Fatalf("report directory not cleaned: %v", err)
				}
			}
		})
	}
	if os.Getenv("PYTHONDONTWRITEBYTECODE") != "" || os.Getenv("PYTHONPYCACHEPREFIX") != "ambient-prefix" || os.Getenv("BUCKLEY_TEST_OVERRIDE") != "ambient" {
		t.Fatal("test subprocess environment leaked into parent process")
	}
}
