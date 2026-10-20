package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistry_FileSizePolicyBoundsDispatchedMutations(t *testing.T) {
	for _, name := range []string{"edit_file", "search_replace", "apply_patch"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fixture.txt")
			if err := os.WriteFile(path, []byte("aaaa"), 0o600); err != nil {
				t.Fatal(err)
			}
			registry := NewRegistry()
			registry.SetWorkDir(root)
			registry.SetMaxFileSizeBytes(4)
			params := map[string]any{"path": "fixture.txt"}
			if name == "edit_file" {
				params["old_string"], params["new_string"], params["replace_all"] = "a", "bb", true
			} else if name == "apply_patch" {
				params = map[string]any{"patch": "--- fixture.txt\n+++ fixture.txt\n@@ -1 +1 @@\n-aaaa\n\\ No newline at end of file\n+bbbbbbbb\n\\ No newline at end of file\n"}
			} else {
				params["search"], params["replace"] = "a", "bb"
			}
			result, err := registry.Execute(name, params)
			if result == nil || result.Success || !strings.Contains(result.Error, "too large") {
				t.Fatalf("registry budget result: %+v error=%v", result, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "aaaa" {
				t.Fatalf("rejected dispatch changed file: %q %v", got, err)
			}
			registry.SetMaxFileSizeBytes(8)
			result, err = registry.Execute(name, params)
			if err != nil || !result.Success {
				t.Fatalf("raised policy rejected valid dispatch: %+v %v", result, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "bbbbbbbb" {
				t.Fatalf("valid dispatch output: %q %v", got, err)
			}
		})
	}
}
