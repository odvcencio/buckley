package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestCommitOutput_ChunkingAndUTF8(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 24, 25, 128, 2048} {
		for _, input := range []string{"", "short", strings.Repeat("界", 1000) + "final", strings.Repeat("\xff\xfe", 1000) + "final"} {
			t.Run(fmt.Sprintf("limit_%d/bytes_%d", limit, len(input)), func(t *testing.T) {
				var reference string
				for _, chunk := range []int{1, 7, 8192} {
					capture := &commitOutput{max: limit}
					for offset := 0; offset < len(input); offset += chunk {
						part := input[offset:min(offset+chunk, len(input))]
						if n, err := capture.Write([]byte(part)); n != len(part) || err != nil {
							t.Fatalf("write=%d, %v", n, err)
						}
					}
					text, truncated := capture.finish()
					if len(text) > limit || !utf8.ValidString(text) || capture.total != int64(len(input)) || (len(input) > limit && !truncated) {
						t.Fatalf("limit=%d bytes=%d valid=%t total=%d truncated=%t", limit, len(text), utf8.ValidString(text), capture.total, truncated)
					}
					if len(input) <= limit && utf8.ValidString(input) && (text != input || truncated) {
						t.Fatalf("small output changed: %q", text)
					}
					if chunk == 1 {
						reference = text
					} else if text != reference {
						t.Fatal("output depends on write chunking")
					}
				}
			})
		}
	}
}

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
					t.Fatalf("invalid previews: stdout=%d stderr=%d data=%v", len(stdout), len(stderr), result.Data)
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
