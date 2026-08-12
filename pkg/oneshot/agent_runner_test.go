package oneshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestNativeCodexReviewRunsWithoutCatalogPricing(t *testing.T) {
	if got := effectiveAgentMaxCostUSD("codex", 0.15); got != 0 {
		t.Fatalf("Codex cost budget = %v, want zero", got)
	}
	if got := effectiveAgentMaxCostUSD("openrouter", 0.15); got != 0.15 {
		t.Fatalf("OpenRouter cost budget = %v, want 0.15", got)
	}
	pricing := transparency.ModelPricing{InputPerMillion: 1, OutputPerMillion: 2}
	tokens := transparency.TokenUsage{Input: 1_000_000, Output: 1_000_000}
	if got := effectiveAgentInvocationCost("codex", pricing, tokens); got != 0 {
		t.Fatalf("Codex invocation cost = %v, want zero", got)
	}
	if got := effectiveAgentInvocationCost("openrouter", pricing, tokens); got != 3 {
		t.Fatalf("OpenRouter invocation cost = %v, want 3", got)
	}
}

func TestReviewAgentOutputTokenLimitRequiresGovernedReasoning(t *testing.T) {
	if got := reviewAgentOutputTokenLimit(0); got != 0 {
		t.Fatalf("ungoverned output limit = %d, want zero", got)
	}
	if got := reviewAgentOutputTokenLimit(1024); got != 5120 {
		t.Fatalf("governed output limit = %d, want 5120", got)
	}
}

func TestClampAgentOutputTokenLimitUsesProviderCapability(t *testing.T) {
	tests := []struct {
		name        string
		configured  int
		providerMax int
		want        int
	}{
		{name: "provider ceiling", configured: 32768, providerMax: 131072, want: 32768},
		{name: "smaller provider", configured: 32768, providerMax: 8192, want: 8192},
		{name: "unknown provider ceiling", configured: 32768, providerMax: 0, want: 32768},
		{name: "unbounded request", configured: 0, providerMax: 8192, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampAgentOutputTokenLimit(tt.configured, tt.providerMax); got != tt.want {
				t.Fatalf("clampAgentOutputTokenLimit(%d, %d) = %d, want %d", tt.configured, tt.providerMax, got, tt.want)
			}
		})
	}
}

func TestFormatIncompleteAgentResponseRetainsCompletedEvidence(t *testing.T) {
	result := &rlm.SubAgentResult{
		Summary:      "Inspected the sharding contract.",
		InputTokens:  120,
		OutputTokens: 30,
		TokensUsed:   150,
		ToolCalls: []rlm.SubAgentToolCall{{
			Name:      "search_text",
			Arguments: `{"query":"race_root"}`,
			Result:    "found aggregate gate",
			Success:   true,
		}},
	}

	got := formatIncompleteAgentResponse(result, errors.Join(context.DeadlineExceeded, errors.New("provider still working")))
	for _, want := range []string{"Incomplete agent result", "not a completed or validated result", "Inspected the sharding contract", "search_text", "found aggregate gate", "120 input", "1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("salvage output missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("salvage output must end with newline")
	}
}

func TestReviewSnapshotRegistryReadsOnlyMaterializedState(t *testing.T) {
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	tracked := filepath.Join(repo, "behavior.txt")
	if err := os.WriteFile(tracked, []byte("captured behavior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReviewRegistryGit(t, repo, "add", "behavior.txt")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	if err := os.WriteFile(tracked, []byte("newer live behavior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(repo, "untracked-secret.txt")
	if err := os.WriteFile(untracked, []byte("untracked secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workDir, cleanup, err := model.PrepareReviewWorkspace(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("PrepareReviewWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	root, err := model.ReviewWorkspaceRepositoryRoot(context.Background(), workDir)
	if err != nil {
		t.Fatalf("ReviewWorkspaceRepositoryRoot: %v", err)
	}
	registry, err := newReviewSnapshotRegistry(root, []string{"read_file", "find_files", "search_text"})
	if err != nil {
		t.Fatalf("newReviewSnapshotRegistry: %v", err)
	}

	read, err := registry.Execute("read_file", map[string]any{"path": "behavior.txt"})
	if err != nil || !read.Success || !strings.Contains(read.Data["content"].(string), "captured behavior") {
		t.Fatalf("snapshot read = %#v, err=%v", read, err)
	}
	for _, path := range []string{"untracked-secret.txt", untracked} {
		outside, execErr := registry.Execute("read_file", map[string]any{"path": path})
		if execErr != nil {
			t.Fatalf("confined read %q: %v", path, execErr)
		}
		if outside.Success {
			t.Fatalf("confined read exposed %q: %#v", path, outside.Data)
		}
	}

	files, err := registry.Execute("find_files", map[string]any{"pattern": "*.txt", "base_path": "."})
	if err != nil || !files.Success {
		t.Fatalf("snapshot find_files = %#v, err=%v", files, err)
	}
	matches, _ := files.Data["matches"].([]string)
	if len(matches) != 1 || matches[0] != "behavior.txt" {
		t.Fatalf("snapshot file inventory = %#v, want only behavior.txt", matches)
	}

	search, err := registry.Execute("search_text", map[string]any{"query": "newer live|untracked secret", "path": "."})
	if err != nil || !search.Success {
		t.Fatalf("snapshot search_text = %#v, err=%v", search, err)
	}
	if count, _ := search.Data["count"].(int); count != 0 {
		t.Fatalf("snapshot search exposed excluded live state: %#v", search.Data)
	}
}

func TestReviewSnapshotRegistryRejectsNonReviewTools(t *testing.T) {
	if _, err := newReviewSnapshotRegistry(t.TempDir(), []string{"read_file", "run_shell"}); err == nil {
		t.Fatal("snapshot registry accepted an executable tool")
	}
}

func TestReviewSnapshotRegistryExplicitlyRegistersSealedVerification(t *testing.T) {
	root := t.TempDir()
	registry, err := newReviewSnapshotRegistry(root, []string{"read_file", "run_verification"}, "/usr/bin/true")
	if err != nil {
		t.Fatalf("newReviewSnapshotRegistry: %v", err)
	}
	verification, ok := registry.Get("run_verification")
	if !ok {
		t.Fatal("snapshot registry omitted explicitly allowed run_verification")
	}
	if _, mutable := verification.(interface{ SetWorkDir(string) }); mutable {
		t.Fatal("run_verification root can be rebound through generic SetWorkDir")
	}
	if got := registry.ToolKind("run_verification"); got != "execute" {
		t.Fatalf("run_verification kind = %q, want execute", got)
	}
}

func TestCollectAgentEvidenceRetainsSnapshotVerificationResult(t *testing.T) {
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	for path, content := range map[string]string{
		"go.mod":           "module example.test/evidence\n\ngo 1.24\n",
		"evidence.go":      "package evidence\n\nfunc Value() int { return 1 }\n",
		"evidence_test.go": "package evidence\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal(\"bad value\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runReviewRegistryGit(t, repo, "add", ".")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	runner := &AgentRunner{}
	calls, err := runner.CollectAgentEvidence(context.Background(), []AgentEvidenceRequest{{
		Tool: "run_verification",
		Parameters: map[string]any{
			"kind": "test", "language": "go", "path": ".",
		},
	}}, AgentExecutionOpts{ReviewSnapshot: snapshot, VerificationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("CollectAgentEvidence: %v", err)
	}
	if len(calls) != 1 || calls[0].ID != "host-evidence-1" || calls[0].Name != "run_verification" {
		t.Fatalf("calls = %#v, want one stable host evidence call", calls)
	}
	if status, _ := calls[0].Data["status"].(string); strings.TrimSpace(status) == "" {
		t.Fatalf("host evidence discarded verification status: %#v", calls[0])
	}
}

func TestCollectAgentEvidenceDoesNotReadUntrackedLiveSource(t *testing.T) {
	repo := t.TempDir()
	runReviewRegistryGit(t, repo, "init", "-q")
	runReviewRegistryGit(t, repo, "config", "user.email", "test@example.com")
	runReviewRegistryGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test/evidence\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(repo, "evidence.go")
	if err := os.WriteFile(basePath, []byte("package evidence\n\nfunc Value() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReviewRegistryGit(t, repo, "add", ".")
	runReviewRegistryGit(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(basePath, []byte("package evidence\n\nfunc Value() int { return missing() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "helper.go"), []byte("package evidence\n\nfunc missing() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := model.CaptureReviewSnapshot(context.Background(), repo, model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotTrackedWorktree})
	if err != nil {
		t.Fatalf("CaptureReviewSnapshot: %v", err)
	}
	calls, err := (&AgentRunner{}).CollectAgentEvidence(context.Background(), []AgentEvidenceRequest{{
		Tool: "run_verification",
		Parameters: map[string]any{
			"kind": "test", "language": "go", "path": ".",
		},
	}}, AgentExecutionOpts{ReviewSnapshot: snapshot, VerificationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("CollectAgentEvidence: %v", err)
	}
	if len(calls) != 1 || calls[0].Success {
		t.Fatalf("calls = %#v, want failed verification without untracked helper", calls)
	}
	if status, _ := calls[0].Data["status"].(string); status != "FAIL" {
		t.Fatalf("status = %q, want FAIL: %#v", status, calls[0])
	}
}

func runReviewRegistryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
