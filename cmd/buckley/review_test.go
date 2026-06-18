package main

import (
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
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
