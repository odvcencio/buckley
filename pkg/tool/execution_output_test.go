package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestToModelOutput_ExecutionEvidence(t *testing.T) {
	SetResultEncoding(false)
	t.Cleanup(func() { SetResultEncoding(true) })
	for _, operation := range []string{"run_shell", "run_code"} {
		for _, tc := range []struct {
			name    string
			ending  string
			flag    string
			success bool
		}{
			{name: "clipped success", success: true},
			{name: "clipped failure", ending: "; exit 7"},
			{name: "timeout", ending: "; sleep 10", flag: "timed_out"},
			{name: "cancellation", ending: "; sleep 10", flag: "canceled"},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				if tc.flag == "canceled" {
					ctx = builtin.WithShellOutputSink(ctx, func(stream, text string) {
						if stream == "stderr" && strings.Contains(text, "diagnostic") {
							cancel()
						}
					})
				}
				const outputLimit = 64
				command := "printf 'answer=42\\n'; printf '%01024d' 0; printf 'diagnostic: incomplete input\\n' >&2" + tc.ending
				params := map[string]any{"command": command, "timeout_seconds": 1}
				var executor interface {
					ExecuteWithContext(context.Context, map[string]any) (*builtin.Result, error)
				}
				if operation == "run_code" {
					code := &builtin.DynamicCodeTool{}
					code.SetMaxOutputBytes(outputLimit)
					executor = code
					params = map[string]any{"language": "bash", "code": command, "timeout_seconds": 1}
				} else {
					shell := &builtin.ShellCommandTool{}
					shell.SetMaxOutputBytes(outputLimit)
					executor = shell
				}
				result, err := executor.ExecuteWithContext(ctx, params)
				if err != nil {
					t.Fatal(err)
				}
				if result.Success != tc.success {
					t.Fatalf("success = %v, error = %q", result.Success, result.Error)
				}
				encoded, err := ToModelOutput(result)
				if err != nil {
					t.Fatal(err)
				}
				var payload struct {
					Success bool           `json:"success"`
					Error   string         `json:"error"`
					Data    map[string]any `json:"data"`
				}
				if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
					t.Fatal(err)
				}
				stdout, _ := payload.Data["stdout"].(string)
				stderr, _ := payload.Data["stderr"].(string)
				if !strings.HasPrefix(stdout, "answer=42\n") || len(stdout) > outputLimit || stderr != "diagnostic: incomplete input" {
					t.Fatalf("model lost bounded execution evidence: %s", encoded)
				}
				if payload.Success != tc.success || (!tc.success && payload.Error == "") || payload.Data["stdout_truncated"] != true {
					t.Fatalf("model lost execution status: %s", encoded)
				}
				if tc.flag != "" && (payload.Data[tc.flag] != true || payload.Data["exit_code"] == float64(0)) {
					t.Fatalf("interrupted execution looks complete: %s", encoded)
				}
				if tc.ending == "; exit 7" && payload.Data["exit_code"] != float64(7) {
					t.Fatalf("model lost exit code: %s", encoded)
				}
				if operation == "run_code" {
					if payload.Data["language"] != "bash" || payload.Data["code_bytes"] != float64(len(command)) {
						t.Fatalf("model lost snippet metadata: %s", encoded)
					}
					if _, ok := payload.Data["command"]; ok {
						t.Fatalf("model received the encoded shell wrapper: %s", encoded)
					}
				}
			})
		}
	}
}
