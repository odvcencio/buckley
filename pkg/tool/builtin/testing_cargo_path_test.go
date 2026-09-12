package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunTestsTool_CargoManifestArguments(t *testing.T) {
	for _, path := range []string{".", "nested crate", filepath.Join(t.TempDir(), "absolute crate")} {
		t.Run(path, func(t *testing.T) {
			t.Cleanup(func() { execCommandContext = exec.CommandContext })
			execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				want := []string{"test", "--manifest-path", filepath.Join(path, "Cargo.toml"), "specific_case"}
				if name != "cargo" || !reflect.DeepEqual(args, want) {
					t.Fatalf("command=%s %q want cargo %q", name, args, want)
				}
				return exec.CommandContext(ctx, "sh", "-c", "exit 0")
			}
			_, exit, _, _, err := (&RunTestsTool{}).runTestsForFramework(context.Background(), "cargo", path, "specific_case", false, false)
			if err != nil || exit != 0 {
				t.Fatalf("exit=%d err=%v", exit, err)
			}
		})
	}
}

func TestRunTestsTool_RealCargoRequestedProject(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed")
	}
	root := t.TempDir()
	for path, content := range map[string]string{
		"Cargo.toml":               "[package]\nname = \"root_sentinel\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
		"src/lib.rs":               "#[test]\nfn root_only() { assert_eq!(2+2, 4); }\n",
		"passing crate/Cargo.toml": "[package]\nname = \"requested_pass\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[workspace]\n",
		"passing crate/src/lib.rs": "#[test]\nfn requested_pass() { assert_eq!(2+2, 4); }\n",
		"failing crate/Cargo.toml": "[package]\nname = \"requested_failure\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[workspace]\n",
		"failing crate/src/lib.rs": "#[test]\nfn requested_failure() { panic!(\"requested project failure\"); }\n",
	} {
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	for _, tc := range []struct {
		path, pattern, marker string
		passed, failed        int
		success               bool
	}{
		{path: "passing crate", marker: "requested_pass", passed: 1, success: true},
		{path: "failing crate", marker: "requested_failure", failed: 1},
		{path: filepath.Join(root, "passing crate"), marker: "requested_pass", passed: 1, success: true},
		{path: filepath.Join(root, "failing crate"), marker: "requested_failure", failed: 1},
		{path: "passing crate", pattern: "no_such_test", marker: "requested_pass"},
		{path: ".", marker: "root_only", passed: 1, success: true},
	} {
		t.Run(tc.path+"/"+tc.pattern, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"path": tc.path, "pattern": tc.pattern, "coverage": false, "timeout_seconds": float64(60)})
			if err != nil || result == nil || result.Success != tc.success || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed || result.Data["framework"] != "cargo" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			output, _ := result.Data["output"].(string)
			if !strings.Contains(output, tc.marker) || (tc.path != "." && strings.Contains(output, "root_only")) {
				t.Fatalf("wrong project output: %s", output)
			}
		})
	}
	t.Run("workspace member overrides defaults", func(t *testing.T) {
		for _, dir := range []string{".", "passing crate", "failing crate"} {
			manifest := filepath.Join(root, dir, "Cargo.toml")
			content, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			updated := strings.TrimSuffix(string(content), "[workspace]\n")
			if dir == "." {
				updated += "[workspace]\nmembers = [\"passing crate\", \"failing crate\"]\ndefault-members = [\"failing crate\"]\n"
			}
			if err := os.WriteFile(manifest, []byte(updated), 0600); err != nil {
				t.Fatal(err)
			}
		}
		result, err := tool.Execute(map[string]any{"path": "passing crate", "coverage": false, "timeout_seconds": float64(60)})
		if err != nil || result == nil || !result.Success || result.Data["passed"] != 1 || result.Data["failed"] != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		output, _ := result.Data["output"].(string)
		if !strings.Contains(output, "requested_pass") || strings.Contains(output, "requested_failure") || strings.Contains(output, "root_only") {
			t.Fatalf("workspace default members ran instead of requested member: %s", output)
		}
	})
}
