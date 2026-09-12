package builtin

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestRunTestsBytecodeEnvironment(t *testing.T) {
	t.Setenv("PYTHONDONTWRITEBYTECODE", "")
	t.Setenv("BUCKLEY_TEST_INHERITED", "inherited")
	t.Setenv("BUCKLEY_TEST_OVERRIDE", "ambient")
	original := execCommandContext
	t.Cleanup(func() { execCommandContext = original })
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s|%s|%s' "$BUCKLEY_TEST_INHERITED" "$BUCKLEY_TEST_OVERRIDE" "$PYTHONDONTWRITEBYTECODE"`)
	}
	for _, tc := range []struct {
		name, framework, want string
		env                   map[string]string
	}{
		{name: "pytest inherited", framework: "pytest", want: "inherited|ambient|1"},
		{name: "pytest configured", framework: "pytest", env: map[string]string{"BUCKLEY_TEST_OVERRIDE": "configured", "PYTHONDONTWRITEBYTECODE": ""}, want: "inherited|configured|1"},
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
		})
	}
	if os.Getenv("PYTHONDONTWRITEBYTECODE") != "" || os.Getenv("BUCKLEY_TEST_OVERRIDE") != "ambient" {
		t.Fatal("test subprocess environment leaked into parent process")
	}
}
