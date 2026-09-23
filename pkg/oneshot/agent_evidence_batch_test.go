package oneshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

// writeFakeWrapperScript writes an executable shell script that records
// every invocation's argv (one line per call, appended) and replays canned
// `go test -json` output, standing in for a remote wrapper such as
// buildbox-run.
func writeFakeWrapperScript(t *testing.T, invocationsFile, fixture string, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-batch-wrapper.sh")
	// Record exactly one line per invocation (tab-joined argv), not one line
	// per argument: `printf '%s\n' "$@"` repeats the format across every
	// positional argument, which would make a single invocation covering
	// several packages look like several invocations.
	script := fmt.Sprintf("#!/bin/sh\n{ printf '%%s\\t' \"$@\"; printf '\\n'; } >> %s\ncat %s\nexit %d\n", invocationsFile, fixture, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake wrapper: %v", err)
	}
	return path
}

func initGoEvidenceRepo(t *testing.T, module string, packages ...string) string {
	t.Helper()
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module "+module+"\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		dir := filepath.Join(repo, pkg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		src := fmt.Sprintf("package %s\n", filepath.Base(pkg))
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(pkg)+".go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runReviewRegistryGit(t, repo, "add", ".")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")
	return repo
}

// TestCollectAgentEvidenceBatchesGoTestRequestsThroughWrapper is the
// end-to-end TDD contract: with a wrapper configured, the harness runs the
// two changed packages' evidence requests through one remote `go test
// -json` wrapper invocation instead of two, and parses each package's
// PASS/FAIL from the replayed event stream. It also proves the immutable
// review snapshot buildbox-run receives (a real PrepareReviewWorkspace
// checkout, not a copy) is itself a valid git worktree, by having the fake
// wrapper run `git rev-parse --is-inside-work-tree` against the directory
// it was handed.
func TestCollectAgentEvidenceBatchesGoTestRequestsThroughWrapper(t *testing.T) {
	module := "example.test/batchevidence"
	repo := initGoEvidenceRepo(t, module, "pkgpass", "pkgfail")

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}

	fixture := filepath.Join(t.TempDir(), "events.jsonl")
	events := fmt.Sprintf(`{"Action":"pass","Package":"%[1]s/pkgpass","Elapsed":0.01}
{"Action":"output","Package":"%[1]s/pkgfail","Output":"    fake_test.go:1: boom\n"}
{"Action":"fail","Package":"%[1]s/pkgfail","Elapsed":0.02}
`, module)
	if err := os.WriteFile(fixture, []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	invocations := filepath.Join(t.TempDir(), "invocations.log")
	gitCheckFile := filepath.Join(t.TempDir(), "git-check.log")
	wrapperBody := fmt.Sprintf(
		"#!/bin/sh\n{ printf '%%s\\t' \"$@\"; printf '\\n'; } >> %s\ngit -C \"$1\" rev-parse --is-inside-work-tree > %s 2>&1\ncat %s\nexit 1\n",
		invocations, gitCheckFile, fixture)
	wrapperPath := filepath.Join(t.TempDir(), "fake-wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(wrapperBody), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := &AgentRunner{}
	calls, err := runner.CollectAgentEvidence(context.Background(), []AgentEvidenceRequest{
		{Tool: "run_verification", Parameters: map[string]any{"kind": "test", "language": "go", "path": "pkgpass"}},
		{Tool: "run_verification", Parameters: map[string]any{"kind": "test", "language": "go", "path": "pkgfail"}},
	}, AgentExecutionOpts{
		ReviewSnapshot:          snapshot,
		VerificationTimeout:     10 * time.Second,
		VerificationWrapper:     []string{wrapperPath},
		VerificationParallelism: 1,
	})
	if err != nil {
		t.Fatalf("CollectAgentEvidence: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v, want 2", calls)
	}
	if calls[0].ID != "host-evidence-1" || calls[1].ID != "host-evidence-2" {
		t.Fatalf("call IDs = %q, %q, want stable host-evidence-N ordering", calls[0].ID, calls[1].ID)
	}
	if status, _ := calls[0].Data["status"].(string); status != "PASS" {
		t.Fatalf("pkgpass status = %q, want PASS: %#v", status, calls[0])
	}
	if status, _ := calls[1].Data["status"].(string); status != "FAIL" {
		t.Fatalf("pkgfail status = %q, want FAIL: %#v", status, calls[1])
	}
	if calls[1].Success {
		t.Fatal("a failed package must not report Success")
	}

	recorded, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(recorded), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("wrapper invocation count = %d, want 1 (both packages batched into one remote call): %v", len(lines), lines)
	}
	argv := strings.Fields(lines[0])
	if len(argv) < 3 || argv[1] != "go" || argv[2] != "test" {
		t.Fatalf("batched wrapper argv = %v, want it to include a go test invocation", argv)
	}

	gitCheck, err := os.ReadFile(gitCheckFile)
	if err != nil {
		t.Fatalf("read git worktree check: %v", err)
	}
	if strings.TrimSpace(string(gitCheck)) != "true" {
		t.Fatalf("the immutable review snapshot the wrapper received did not qualify as a git worktree: %q", string(gitCheck))
	}
}

// TestCollectAgentEvidenceParallelismCapLimitsWrapperInvocations proves the
// configured parallelism cap holds for batched remote evidence: four
// packages with parallelism=2 must produce exactly two wrapper invocations
// (two chunks), never four and never one.
func TestCollectAgentEvidenceParallelismCapLimitsWrapperInvocations(t *testing.T) {
	module := "example.test/parallelismcap"
	repo := initGoEvidenceRepo(t, module, "pkg1", "pkg2", "pkg3", "pkg4")

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}

	fixture := filepath.Join(t.TempDir(), "events.jsonl")
	var events strings.Builder
	for _, pkg := range []string{"pkg1", "pkg2", "pkg3", "pkg4"} {
		fmt.Fprintf(&events, `{"Action":"pass","Package":"%s/%s","Elapsed":0.01}`+"\n", module, pkg)
	}
	if err := os.WriteFile(fixture, []byte(events.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	invocations := filepath.Join(t.TempDir(), "invocations.log")
	wrapperPath := writeFakeWrapperScript(t, invocations, fixture, 0)

	requests := make([]AgentEvidenceRequest, 0, 4)
	for _, pkg := range []string{"pkg1", "pkg2", "pkg3", "pkg4"} {
		requests = append(requests, AgentEvidenceRequest{
			Tool:       "run_verification",
			Parameters: map[string]any{"kind": "test", "language": "go", "path": pkg},
		})
	}

	runner := &AgentRunner{}
	calls, err := runner.CollectAgentEvidence(context.Background(), requests, AgentExecutionOpts{
		ReviewSnapshot:          snapshot,
		VerificationTimeout:     10 * time.Second,
		VerificationWrapper:     []string{wrapperPath},
		VerificationParallelism: 2,
	})
	if err != nil {
		t.Fatalf("CollectAgentEvidence: %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(calls))
	}
	for i, call := range calls {
		if status, _ := call.Data["status"].(string); status != "PASS" {
			t.Fatalf("call %d status = %q, want PASS: %#v", i, status, call)
		}
	}

	recorded, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(recorded), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrapper invocation count = %d, want exactly 2 (parallelism cap): %v", len(lines), lines)
	}
}
