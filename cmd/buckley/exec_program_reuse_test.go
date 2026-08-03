package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
)

// newReuseTestTool wires a real exec_program tool over real stores in a
// temporary workspace.
func newReuseTestTool(t *testing.T) (*execProgramTool, evidence.Store, string) {
	t.Helper()
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := writeWorkspaceFile(t, workspace, "greeting.txt", "hello reuse\n"); err != nil {
		t.Fatal(err)
	}
	ev, err := evidence.New(filepath.Join(dir, "store.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{SessionID: "reuse-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	tool, err := newExecProgramTool(workspace, ledger, ev, run.RunID, "reuse-test")
	if err != nil {
		t.Fatalf("newExecProgramTool: %v", err)
	}
	return tool, ev, run.RunID
}

// TestExecProgram_ReuseReplaysStoredProgram locks stabilized mode: a
// program's evidence ID re-runs it verbatim with no source, producing
// the same output. This is the zero-model-token path — a workflow that
// already worked never gets re-reasoned.
func TestExecProgram_ReuseReplaysStoredProgram(t *testing.T) {
	tool, _, _ := newReuseTestTool(t)
	ctx := context.Background()

	const program = `package main

import (
	"fmt"
	"strings"

	"execprogram/caps"
)

func main() {
	content, _, err := caps.ReadFile("greeting.txt")
	if err != nil {
		panic(err)
	}
	fmt.Printf("read=%q\n", strings.TrimSpace(content))
}
`
	first, err := tool.ExecuteWithContext(ctx, map[string]any{"source": program})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !first.Success {
		t.Fatalf("first run failed: %+v", first)
	}
	programEvidence, _ := first.Data["program_evidence"].(string)
	if programEvidence == "" {
		t.Fatal("first run stored no program evidence")
	}
	wantStdout, _ := first.Data["stdout"].(string)
	if !strings.Contains(wantStdout, `read="hello reuse"`) {
		t.Fatalf("first stdout = %q", wantStdout)
	}

	// Replay with no source at all.
	replay, err := tool.ExecuteWithContext(ctx, map[string]any{"reuse": programEvidence})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Success {
		t.Fatalf("replay failed: %+v", replay)
	}
	if got, _ := replay.Data["stdout"].(string); got != wantStdout {
		t.Fatalf("replay stdout = %q, want %q", got, wantStdout)
	}

	// A bad evidence ID fails cleanly rather than running anything.
	missing, err := tool.ExecuteWithContext(ctx, map[string]any{"reuse": "ev_DOES_NOT_EXIST"})
	if err != nil {
		t.Fatalf("missing reuse returned a hard error: %v", err)
	}
	if missing.Success || !strings.Contains(missing.Error, "reuse") {
		t.Fatalf("missing reuse = %+v, want a clean failure", missing)
	}

	// Neither source nor reuse is a usage error, not a panic.
	empty, err := tool.ExecuteWithContext(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("empty params returned a hard error: %v", err)
	}
	if empty.Success || !strings.Contains(empty.Error, "required") {
		t.Fatalf("empty params = %+v, want a usage error", empty)
	}
}

func writeWorkspaceFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
