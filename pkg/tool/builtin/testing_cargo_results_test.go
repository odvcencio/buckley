package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsTool_CargoEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, output                  string
		exit, passed, failed, skipped int
		success                       bool
	}{
		{name: "empty", output: "test result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\n"},
		{name: "all ignored", output: "test result: ok. 0 passed; 0 failed; 2 ignored; 0 measured; 0 filtered out\n", skipped: 2},
		{name: "no report", output: "Finished test profile\n"},
		{name: "first suite empty", output: "test result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\ntest result: ok. 2 passed; 0 failed; 1 ignored; 0 measured; 0 filtered out\ntest result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\n", passed: 2, skipped: 1, success: true},
		{name: "multiple suites", output: "test result: ok. 2 passed; 0 failed; 1 ignored; 0 measured; 0 filtered out\ntest result: ok. 3 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\n", passed: 5, skipped: 1, success: true},
		{name: "failure with zero exit", output: "test result: ok. 1 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\ntest result: FAILED. 0 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out\n", passed: 1, failed: 1},
		{name: "nonzero exit", output: "test result: ok. 1 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\n", exit: 1, passed: 1},
		{name: "unrelated counts", output: "log says 10 passed; 0 failed; 0 ignored\n"},
		{name: "abridged no tests", output: strings.Repeat("compiling dependency\n", 500) + "test result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { execCommandContext = exec.CommandContext })
			execCommandContext = func(ctx context.Context, name string, _ ...string) *exec.Cmd {
				if name != "cargo" {
					t.Fatalf("command=%s", name)
				}
				return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1"; exit "$2"`, "test", tc.output, fmt.Sprint(tc.exit))
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"evidence\"\nversion=\"0.1.0\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := (&RunTestsTool{}).Execute(map[string]any{"path": dir})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != tc.success || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed || result.Data["skipped"] != tc.skipped {
				t.Fatalf("result=%+v", result)
			}
			if !tc.success && result.Error == "" {
				t.Fatal("missing verification failure")
			}
			if len(tc.output) > 5000 && (!result.ShouldAbridge || result.DisplayData["error"] == "" || result.DisplayData["passed"] != tc.passed) {
				t.Fatalf("abridging lost evidence: %+v", result)
			}
		})
	}
}

func TestRunTestsTool_RealCargoEvidence(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed")
	}
	dir := t.TempDir()
	for path, content := range map[string]string{
		"Cargo.toml":        "[package]\nname = \"buckley_evidence\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/lib.rs":        "pub fn answer() -> u32 { 42 }\n",
		"tests/evidence.rs": "#[test]\nfn evidence_passes() { assert_eq!(buckley_evidence::answer(), 42); }\n#[test]\n#[ignore]\nfn intentional_skip() { panic!(\"not run\"); }\n",
	} {
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(dir)
	for _, tc := range []struct {
		pattern         string
		passed, skipped int
		success         bool
	}{
		{pattern: "evidence_passes", passed: 1, success: true},
		{pattern: "intentional_skip", skipped: 1},
		{pattern: "no_such_test"},
		{passed: 1, skipped: 1, success: true},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"path": ".", "pattern": tc.pattern, "timeout_seconds": float64(60)})
			if err != nil || result.Success != tc.success || result.Data["exit_code"] != 0 || result.Data["passed"] != tc.passed || result.Data["skipped"] != tc.skipped {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
