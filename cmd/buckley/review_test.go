package main

import (
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestParseReviewCommandOptions(t *testing.T) {
	opts, err := parseReviewCommandOptions([]string{
		"-project",
		"-scope", "branch",
		"-base", "main",
		"-unstaged=false",
		"-verbose",
		"-cost=false",
		"-model", "test/reviewer",
		"-timeout", "12s",
		"-output", "review.md",
		"-no-interactive",
	})
	if err != nil {
		t.Fatalf("parseReviewCommandOptions() error = %v", err)
	}

	if !opts.projectMode {
		t.Fatal("projectMode = false, want true")
	}
	if opts.scope != "branch" {
		t.Fatalf("scope = %q, want branch", opts.scope)
	}
	if opts.baseBranch != "main" {
		t.Fatalf("baseBranch = %q, want main", opts.baseBranch)
	}
	if opts.includeUnstaged {
		t.Fatal("includeUnstaged = true, want false")
	}
	if len(opts.untrackedPaths) != 0 {
		t.Fatalf("untrackedPaths = %v, want none by default", opts.untrackedPaths)
	}
	if !opts.verbose {
		t.Fatal("verbose = false, want true")
	}
	if opts.showCost {
		t.Fatal("showCost = true, want false")
	}
	if opts.model != "test/reviewer" {
		t.Fatalf("model = %q, want test/reviewer", opts.model)
	}
	if opts.timeout != 12*time.Second {
		t.Fatalf("timeout = %v, want 12s", opts.timeout)
	}
	if opts.outputFile != "review.md" {
		t.Fatalf("outputFile = %q, want review.md", opts.outputFile)
	}
	if opts.interactive {
		t.Fatal("interactive = true, want false when -no-interactive is set")
	}
}

func TestResolveReviewModelPrecedence(t *testing.T) {
	previous := modelOverrideFlag
	modelOverrideFlag = ""
	t.Cleanup(func() {
		modelOverrideFlag = previous
	})

	t.Setenv("BUCKLEY_MODEL_REVIEW", "env/reviewer")
	cfg := config.DefaultConfig()
	cfg.Models.Review = "config/reviewer"
	cfg.Models.Execution = "config/executor"

	if got := resolveReviewModel(cfg); got != "env/reviewer" {
		t.Fatalf("resolveReviewModel() = %q, want env/reviewer", got)
	}

	modelOverrideFlag = "override/reviewer"
	if got := resolveReviewModel(cfg); got != "override/reviewer" {
		t.Fatalf("resolveReviewModel() with override = %q, want override/reviewer", got)
	}
}

func TestResolveReviewModelAppliesCommandReasoningSuffix(t *testing.T) {
	previous := modelOverrideFlag
	modelOverrideFlag = "codex/gpt-5.6-terra-high"
	t.Cleanup(func() { modelOverrideFlag = previous })

	cfg := config.DefaultConfig()
	cfg.Models.Reasoning = ""
	if got := resolveReviewModel(cfg); got != "codex/gpt-5.6-terra" {
		t.Fatalf("resolveReviewModel() = %q, want codex/gpt-5.6-terra", got)
	}
	if cfg.Models.Reasoning != "high" {
		t.Fatalf("reasoning = %q, want high", cfg.Models.Reasoning)
	}
}

func TestNormalizeReviewCommandScope(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		want  string
	}{
		{name: "empty", scope: "", want: commands.ReviewScopeWorktree},
		{name: "worktree", scope: "worktree", want: commands.ReviewScopeWorktree},
		{name: "commits alias", scope: "commits", want: commands.ReviewScopeBranch},
		{name: "local alias", scope: "local", want: commands.ReviewScopeChanges},
		{name: "unknown", scope: "surprise", want: commands.ReviewScopeWorktree},
	}

	for _, tt := range tests {
		if got := normalizeReviewCommandScope(tt.scope); got != tt.want {
			t.Fatalf("%s: normalizeReviewCommandScope(%q) = %q, want %q", tt.name, tt.scope, got, tt.want)
		}
	}
}

func TestBranchReviewSnapshotPolicyMatchesReviewScope(t *testing.T) {
	tests := []struct {
		name            string
		scope           string
		includeUnstaged bool
		untrackedPaths  []string
		want            model.ReviewSnapshotMode
	}{
		{name: "branch ignores local state", scope: commands.ReviewScopeBranch, includeUnstaged: true, want: model.ReviewSnapshotHead},
		{name: "worktree staged only", scope: commands.ReviewScopeWorktree, includeUnstaged: false, want: model.ReviewSnapshotIndex},
		{name: "worktree excludes untracked state by default", scope: commands.ReviewScopeWorktree, includeUnstaged: true, want: model.ReviewSnapshotTrackedWorktree},
		{name: "worktree explicitly includes reviewable untracked state", scope: commands.ReviewScopeWorktree, includeUnstaged: true, untrackedPaths: []string{"new.go"}, want: model.ReviewSnapshotWorktree},
		{name: "local changes staged only", scope: commands.ReviewScopeChanges, includeUnstaged: false, want: model.ReviewSnapshotIndex},
		{name: "local changes include unstaged", scope: commands.ReviewScopeChanges, includeUnstaged: true, want: model.ReviewSnapshotTrackedWorktree},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := branchReviewSnapshotPolicy(tt.scope, tt.includeUnstaged, tt.untrackedPaths)
			if got := policy.Mode; got != tt.want {
				t.Fatalf("snapshot mode = %q, want %q", got, tt.want)
			}
			if tt.want == model.ReviewSnapshotWorktree && len(policy.UntrackedPaths) != 1 {
				t.Fatalf("snapshot untracked allowlist = %v, want one path", policy.UntrackedPaths)
			}
		})
	}
}

func TestParseReviewCommandOptionsRequiresExplicitSafeUntrackedMode(t *testing.T) {
	opts, err := parseReviewCommandOptions([]string{"--scope", "worktree", "--include-untracked", "helper.go", "--include-untracked", "pkg/new.go", "--no-interactive"})
	if err != nil {
		t.Fatalf("parseReviewCommandOptions() error = %v", err)
	}
	if len(opts.untrackedPaths) != 2 || opts.untrackedPaths[0] != "helper.go" || opts.untrackedPaths[1] != "pkg/new.go" {
		t.Fatalf("untrackedPaths = %v, want explicit path allowlist", opts.untrackedPaths)
	}

	for _, args := range [][]string{
		{"--scope", "branch", "--include-untracked", "helper.go"},
		{"--scope", "changes", "--include-untracked", "helper.go"},
		{"--scope", "worktree", "--unstaged=false", "--include-untracked", "helper.go"},
		{"--project", "--include-untracked", "helper.go"},
	} {
		if _, err := parseReviewCommandOptions(args); err == nil {
			t.Fatalf("parseReviewCommandOptions(%v) succeeded, want unsafe-mode error", args)
		}
	}
}

func TestReviewResultFromRLMExposesPrimaryAndCriticAttempts(t *testing.T) {
	got := reviewResultFromRLM(&oneshot.RunResult{
		Attempts:        3,
		PrimaryAttempts: 1,
		CriticAttempts:  2,
	}, nil)

	if got.attempts != 3 || got.primary != 1 || got.criticAttempts != 2 {
		t.Fatalf("attempt counts = total:%d primary:%d critic:%d, want 3/1/2",
			got.attempts, got.primary, got.criticAttempts)
	}
}
