//go:build integration

package oneshot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// reviewSmokeText is a schema-complete, non-approved review (see
// pkg/oneshot/commands/review_findings.go ValidateParsedReview and
// review_test.go's completeReviewWithCoverage fixture for the required
// section shape). Grade C plus a REQUEST CHANGES verdict and a PROVED
// falsification conclusion skips the approval-only execution-evidence gate
// (no run_verification call is required), keeping this smoke test to one
// model round while still exercising every schema requirement a real review
// hits.
const reviewSmokeText = `## Grade: C

## Summary
The branch adds a small greet helper without a corresponding test.

## CI Status
- Build: PASS
- Tests: PASS

## Coverage
- **File**: ` + "`main.go`" + ` — reviewed the added greet helper against the diff.
- **Feedback disposition**: ` + "`NONE_SUPPLIED`" + ` — no prior feedback was supplied.
- **Verification**: reviewed source diff only.

## Invariant Audit
- Checked the new function's exported signature; no invariant changed.

## Falsification
- **Strongest plausible failure**: greet mishandles an empty name argument.
- **Evidence**: no test exercises the empty-name case.
- **Conclusion**: PROVED

## Findings
None.

## Verdict
- **Recommendation**: REQUEST CHANGES
- **Blockers**: None
- **Suggestions**: None`

// TestReviewPipeline_BranchReviewCompletesThroughMigratedControllerPath is
// the wave-2 loop-migration smoke test: it builds the real buckley binary
// from this worktree, runs `buckley review -scope branch` non-interactively
// in a temp git repo against a trivial synthetic diff, and points every
// provider at a mock OpenAI-compatible server. buckley review always routes
// through oneshot.AgentRunner -> rlm.SubAgent.Execute (see
// cmd/buckley/review.go newReviewCommandRuntime), so a clean exit with the
// mock's review text on stdout is direct evidence that the
// agentloop.Controller-based SubAgent.Execute migration drives the command
// end to end: real request assembly, real HTTP round trip, real
// synthesis-mode request shaping under a tight timeout, and real content
// extraction back out to the CLI.
//
// Gated behind the integration build tag (matching tests/one_shot_commit_test.go)
// since it shells out to `go build` and a subprocess; run with
// `go test -tags integration ./pkg/oneshot/... -run ReviewPipeline`.
func TestReviewPipeline_BranchReviewCompletesThroughMigratedControllerPath(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":    "chatcmpl-smoke",
			"model": "gpt-4o",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": reviewSmokeText},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 50, "completion_tokens": 40, "total_tokens": 90},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	binPath := filepath.Join(t.TempDir(), "buckley")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/buckley")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build buckley: %v\n%s", err, out)
	}

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	mainGo := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	runGit(t, repoDir, "add", "main.go")
	runGit(t, repoDir, "commit", "-q", "-m", "initial commit")
	runGit(t, repoDir, "checkout", "-q", "-b", "feature")
	greet := mainGo + "\nfunc greet(name string) string {\n\treturn \"hello, \" + name\n}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte(greet), 0o644); err != nil {
		t.Fatalf("update main.go: %v", err)
	}
	runGit(t, repoDir, "add", "main.go")
	runGit(t, repoDir, "commit", "-q", "-m", "add greet helper")

	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".buckley"), 0o755); err != nil {
		t.Fatalf("mkdir .buckley: %v", err)
	}
	configYAML := "providers:\n" +
		"  openai:\n" +
		"    enabled: true\n" +
		"    api_key: \"test-key\"\n" +
		"    base_url: \"" + server.URL + "\"\n" +
		"models:\n" +
		"  default_provider: openai\n" +
		"  review: \"gpt-4o\"\n" +
		"  execution: \"gpt-4o\"\n"
	if err := os.WriteFile(filepath.Join(homeDir, ".buckley", "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "review", "-scope", "branch", "-base", "main", "-no-interactive", "-timeout", "30s")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"BUCKLEY_MODEL_REVIEW=gpt-4o",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("buckley review failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if got := atomic.LoadInt32(&requestCount); got == 0 {
		t.Fatal("mock model server never received a request; the migrated SubAgent.Execute path never ran")
	}
	out := stdout.String()
	if !strings.Contains(out, "## Grade: C") {
		t.Fatalf("expected the mock review text on stdout, got:\n%s", out)
	}
	if !strings.Contains(out, "REQUEST CHANGES") {
		t.Fatalf("expected the mock review verdict on stdout, got:\n%s", out)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		t.Fatal("not running inside a Go module")
	}
	return filepath.Dir(gomod)
}
