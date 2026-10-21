package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestCommitChangesTool_OutputBudget(t *testing.T) {
	helper := newFakeCommitHelper(t)
	for _, mode := range []string{"spam", "spam-fail", "spam-invalid"} {
		for _, configured := range []int{0, 128, 1000000} {
			t.Run(fmt.Sprintf("%s/limit_%d", mode, configured), func(t *testing.T) {
				tool := &CommitChangesTool{Executable: helper}
				tool.SetWorkDir(t.TempDir())
				tool.SetMaxOutputBytes(configured)
				tool.SetEnv(map[string]string{"BUCKLEY_FAKE_COMMIT_MODE": mode})
				result, err := tool.Execute(map[string]any{"paths": []any{"selected.txt"}})
				if err != nil || result == nil {
					t.Fatalf("execute: %v", err)
				}
				limit := 2048
				if configured > 0 {
					limit = min(limit, configured)
				}
				stdout := result.Data["stdout"].(string)
				stderr := result.Data["stderr"].(string)
				if len(stdout) > limit || len(stderr) > limit || !utf8.ValidString(stdout+stderr) || result.Data["stdout_truncated"] != true || result.Data["stderr_truncated"] != true {
					t.Fatalf("invalid previews: stdout=%d stderr=%d stdout_truncated=%v stderr_truncated=%v", len(stdout), len(stderr), result.Data["stdout_truncated"], result.Data["stderr_truncated"])
				}
				if !strings.HasPrefix(stdout, "generated commit subject\n") || !strings.HasSuffix(stdout, "COMMIT_COMPLETE\n") || !strings.HasPrefix(stderr, "hook diagnostics begin\n") || !strings.HasSuffix(stderr, "fatal: fixture hook rejected commit\n") {
					t.Fatalf("lost initial or final diagnostic: stdout=%q stderr=%q", stdout, stderr)
				}
				if result.Data["stdout_bytes"].(int64) < 1<<20 || result.Data["stderr_bytes"].(int64) < 1<<20 || len(result.DisplayData) != 0 {
					t.Fatal("missing byte counts or duplicated display payload")
				}
				if result.Success != (mode != "spam-fail") || len(result.Error) > 512 || (mode == "spam-fail" && !strings.Contains(result.Error, "see stderr") && !strings.Contains(result.Error, "fatal: fixture hook rejected commit")) {
					t.Fatalf("status/error lost: success=%t error=%q", result.Success, result.Error)
				}
				jsonBytes := 0
				for _, useToon := range []bool{false, true} {
					encoded, err := toon.New(useToon).MarshalString(result)
					if err != nil || len(encoded) > 6<<10 {
						t.Fatalf("encoded result: bytes=%d error=%v", len(encoded), err)
					}
					if !useToon {
						jsonBytes = len(encoded)
						var decoded Result
						if err := json.Unmarshal([]byte(encoded), &decoded); err != nil || decoded.Success != result.Success || decoded.Data["stderr"] != stderr {
							t.Fatal("JSON lost evidence")
						}
					}
				}
				encoded, _ := toon.New(true).MarshalString(result)
				t.Logf("fixture=subprocess/%s configured=%d stdout_bytes=%d stderr_bytes=%d retained_stdout=%d retained_stderr=%d json_bytes=%d toon_bytes=%d final_diagnostic=true duplicate_display=false", mode, configured, result.Data["stdout_bytes"], result.Data["stderr_bytes"], len(stdout), len(stderr), jsonBytes, len(encoded))
			})
		}
	}
}
