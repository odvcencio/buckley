package execprogram

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestProgramOutputTruncation(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	ctx := context.Background()
	ev, err := evidence.New(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "output-limit", AgentID: "test", Backend: "code-mode"})
	if err != nil {
		t.Fatal(err)
	}
	program, err := NewProgramTool(t.TempDir(), ledger, ev, run.RunID, "output-limit", execmode.ReadOnlySet)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 128 * 1024
	for _, tc := range []struct {
		name                 string
		stdout, stderr, exit int
	}{
		{"small", 2, 0, 0},
		{"exact", limit, limit, 0},
		{"stdout", limit + 1, 0, 0},
		{"stderr", 0, limit + 1, 0},
		{"both_with_exit_error", limit + 1, limit + 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := fmt.Sprintf(`package main
import("fmt";"os";"strings")
func main(){fmt.Print(strings.Repeat("x",%d));fmt.Fprint(os.Stderr,strings.Repeat("y",%d));os.Exit(%d)}`, tc.stdout, tc.stderr, tc.exit)
			result, err := program.ExecuteWithContext(ctx, map[string]any{"source": source})
			if err != nil || result == nil {
				t.Fatalf("execution err=%v", err)
			}
			stdoutTruncated, stderrTruncated := tc.stdout > limit, tc.stderr > limit
			wantSuccess := !stdoutTruncated && !stderrTruncated && tc.exit == 0
			if result.Success != wantSuccess {
				t.Fatalf("success=%v want=%v error=%q", result.Success, wantSuccess, result.Error)
			}
			if result.Data["exit_code"] != tc.exit {
				t.Fatalf("exit=%v want=%d", result.Data["exit_code"], tc.exit)
			}
			if result.Data["stdout_truncated"] != stdoutTruncated || result.Data["stderr_truncated"] != stderrTruncated {
				t.Fatalf("flags stdout=%v stderr=%v", result.Data["stdout_truncated"], result.Data["stderr_truncated"])
			}
			if (stdoutTruncated || stderrTruncated) && !strings.Contains(result.Error, "truncated") {
				t.Fatalf("missing truncation error: %q", result.Error)
			}
			if tc.exit != 0 && !strings.Contains(result.Error, "program exited") {
				t.Fatalf("lost process error: %q", result.Error)
			}
			stdout, _ := result.Data["stdout"].(string)
			stderr, _ := result.Data["stderr"].(string)
			if len(stdout) != min(tc.stdout, limit) || len(stderr) != min(tc.stderr, limit) {
				t.Fatalf("captured lengths=%d/%d", len(stdout), len(stderr))
			}
			programID, _ := result.Data["program_evidence"].(string)
			outputID, _ := result.Data["output_evidence"].(string)
			if programID == "" || outputID == "" {
				t.Fatal("missing evidence identifiers")
			}
			output, err := ev.Get(ctx, outputID)
			if err != nil {
				t.Fatal(err)
			}
			if output.Metadata["stdout_truncated"] != stdoutTruncated || output.Metadata["stderr_truncated"] != stderrTruncated {
				t.Fatalf("evidence flags=%v/%v", output.Metadata["stdout_truncated"], output.Metadata["stderr_truncated"])
			}
			for _, marker := range []string{fmt.Sprintf("stdout_truncated=%t", stdoutTruncated), fmt.Sprintf("stderr_truncated=%t", stderrTruncated)} {
				if !strings.Contains(string(output.InlineBody), marker) {
					t.Fatalf("evidence missing %q", marker)
				}
			}
		})
	}
	t.Run("runtime_error", func(t *testing.T) {
		result, err := program.ExecuteWithContext(ctx, map[string]any{"source": "not a complete Go program"})
		if err != nil || result == nil || result.Success || !strings.Contains(result.Error, "package main") {
			t.Fatalf("runtime failure err=%v result-present=%v", err, result != nil)
		}
		if result.Data["stdout_truncated"] != false || result.Data["stderr_truncated"] != false {
			t.Fatal("runtime failure incorrectly reported truncation")
		}
		for _, field := range []string{"program_evidence", "output_evidence"} {
			id, _ := result.Data[field].(string)
			if id == "" {
				t.Fatalf("runtime failure missing %s", field)
			}
			if _, err := ev.Get(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
	})
}
