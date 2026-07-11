package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestParsePRCommandOptions(t *testing.T) {
	opts, err := parsePRCommandOptions([]string{
		"-dry-run",
		"-yes",
		"-push=false",
		"-verbose",
		"-cost=false",
		"-base=develop",
		"-model=openai/test-pr",
		"-backend=codex",
		"-timeout=15s",
	})
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}

	if !opts.dryRun {
		t.Fatal("dryRun = false, want true")
	}
	if !opts.yes {
		t.Fatal("yes = false, want true")
	}
	if opts.push {
		t.Fatal("push = true, want false")
	}
	if !opts.verbose {
		t.Fatal("verbose = false, want true")
	}
	if opts.showCost {
		t.Fatal("showCost = true, want false")
	}
	if opts.base != "develop" {
		t.Fatalf("base = %q, want develop", opts.base)
	}
	if opts.model != "openai/test-pr" {
		t.Fatalf("model = %q, want openai/test-pr", opts.model)
	}
	if opts.backend != oneshot.CLIBackendCodex {
		t.Fatalf("backend = %q, want codex", opts.backend)
	}
	if opts.timeout != 15*time.Second {
		t.Fatalf("timeout = %s, want 15s", opts.timeout)
	}
}

func TestParsePRCommandOptionsHonorsEnvironment(t *testing.T) {
	t.Setenv(envPRBackend, "claude")

	opts, err := parsePRCommandOptions(nil)
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}

	if opts.backend != oneshot.CLIBackendClaude {
		t.Fatalf("backend = %q, want claude", opts.backend)
	}
	if opts.push != true {
		t.Fatal("push = false, want true")
	}
	if opts.showCost != true {
		t.Fatal("showCost = false, want true")
	}
	if opts.timeout != 2*time.Minute {
		t.Fatalf("timeout = %s, want 2m", opts.timeout)
	}
}

func TestPRRunResultFromFramework(t *testing.T) {
	pr := &commands.PRResult{
		Title:   "tighten pr command",
		Summary: "Split command wiring into smaller helpers.",
		Changes: []string{
			"Parse flags separately",
		},
		Testing: []string{
			"go test ./cmd/buckley",
		},
	}
	trace := &transparency.Trace{Reasoning: "reasoning"}
	audit := transparency.NewContextAudit()

	result := prRunResultFromFramework(&oneshot.RunResult{
		Value:        pr,
		Trace:        trace,
		ContextAudit: audit,
	})

	if result.PR != pr {
		t.Fatal("PR result was not preserved")
	}
	if result.Trace != trace {
		t.Fatal("trace was not preserved")
	}
	if result.ContextAudit != audit {
		t.Fatal("context audit was not preserved")
	}
	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
}

func TestPRRunResultFromFrameworkRejectsUnexpectedValue(t *testing.T) {
	result := prRunResultFromFramework(&oneshot.RunResult{Value: "not a pr"})

	if result.PR != nil {
		t.Fatal("PR result should be nil for unexpected value")
	}
	if result.Error == nil {
		t.Fatal("expected unexpected result type error")
	}
	if !strings.Contains(result.Error.Error(), "unexpected result type") {
		t.Fatalf("error = %q, want unexpected result type", result.Error)
	}
}

func TestResolvePRBaseRangeFetchesStackedBaseAndExcludesPriorHistory(t *testing.T) {
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", ".")

	seed := initTempGitRepo(t)
	runGit(t, seed, "branch", "-M", "main")
	runGit(t, seed, "remote", "add", "origin", remote)
	runGit(t, seed, "push", "-u", "origin", "main")
	runGit(t, seed, "switch", "-c", "codex/poppler-versioned-external-relex")
	if err := os.WriteFile(filepath.Join(seed, "prior.txt"), []byte("prior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "prior.txt")
	runGit(t, seed, "commit", "-m", "land prior roadmap", "-m", "Closes #219")
	runGit(t, seed, "push", "-u", "origin", "codex/poppler-versioned-external-relex")
	stackBase := runGitOutputForPushTest(t, seed, "rev-parse", "HEAD")

	feature := filepath.Join(t.TempDir(), "feature")
	runGit(t, filepath.Dir(feature), "clone", "-b", "codex/poppler-versioned-external-relex", remote, feature)
	runGit(t, feature, "config", "user.email", "test@example.com")
	runGit(t, feature, "config", "user.name", "Test User")
	runGit(t, feature, "switch", "-c", "codex/javascript-trailing-extra-parity")
	if err := os.WriteFile(filepath.Join(feature, "focused.go"), []byte("package focused\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, feature, "add", "focused.go")
	runGit(t, feature, "commit", "-m", "fix trailing extra ownership")

	// Advance the remote base after the feature clone. Resolution must fetch
	// this new commit while retaining the original stack point as merge-base.
	if err := os.WriteFile(filepath.Join(seed, "advanced.txt"), []byte("advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "advanced.txt")
	runGit(t, seed, "commit", "-m", "advance stacked base")
	runGit(t, seed, "push", "origin", "codex/poppler-versioned-external-relex")
	wantBase := runGitOutputForPushTest(t, seed, "rev-parse", "HEAD")

	t.Chdir(feature)
	resolved, err := resolvePRBaseRange(context.Background(), "codex/poppler-versioned-external-relex")
	if err != nil {
		t.Fatalf("resolvePRBaseRange: %v", err)
	}
	if resolved.Commit != wantBase {
		t.Fatalf("base commit = %s, want freshly fetched %s", resolved.Commit, wantBase)
	}
	if resolved.MergeBase != stackBase {
		t.Fatalf("merge base = %s, want stack point %s", resolved.MergeBase, stackBase)
	}
	if resolved.Range != stackBase+"..HEAD" {
		t.Fatalf("range = %q, want %q", resolved.Range, stackBase+"..HEAD")
	}

	def := commands.PRDefinition{
		BaseBranch:  resolved.Branch,
		BaseCommit:  resolved.Commit,
		CommitRange: resolved.Range,
	}
	ctx, err := oneshot.BuildContext(def.ContextSources(), oneshot.ContextOpts{})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	prompt := def.BuildPrompt(ctx)
	if !strings.Contains(prompt, "fix trailing extra ownership") || !strings.Contains(prompt, "focused.go") {
		t.Fatalf("prompt missing focused stack change:\n%s", prompt)
	}
	if strings.Contains(prompt, "land prior roadmap") || strings.Contains(prompt, "Closes #219") || strings.Contains(prompt, "advance stacked base") {
		t.Fatalf("prompt leaked base history:\n%s", prompt)
	}
}
