package main

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/terminal"
	"m31labs.dev/buckley/pkg/transparency"
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
		"-critic-model", "test/critic",
		"-timeout", "12s",
		"-output", "review.md",
		"-no-interactive",
		"-budget", "0.20",
		"-max-turns", "4",
		"-max-tool-calls", "17",
		"-max-diff-bytes", "64000",
		"-max-validation-attempts", "1",
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
	if opts.criticModel != "test/critic" {
		t.Fatalf("criticModel = %q, want test/critic", opts.criticModel)
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
	if opts.budgetUSD != 0.20 || opts.maxTurns != 4 || opts.maxToolCalls != 17 || opts.maxDiff != 64_000 || opts.maxRetries != 1 {
		t.Fatalf("budget controls = $%.2f/%d/%d/%d/%d, want $0.20/4/17/64000/1",
			opts.budgetUSD, opts.maxTurns, opts.maxToolCalls, opts.maxDiff, opts.maxRetries)
	}
}

func TestReviewCommandsReserveEnoughTimeByDefault(t *testing.T) {
	branch, err := parseReviewCommandOptions(nil)
	if err != nil {
		t.Fatalf("parseReviewCommandOptions() error = %v", err)
	}
	if branch.timeout != defaultReviewTimeout {
		t.Fatalf("branch timeout = %s, want %s", branch.timeout, defaultReviewTimeout)
	}

	project, err := parseReviewCommandOptions([]string{"--project"})
	if err != nil {
		t.Fatalf("parseReviewCommandOptions(--project) error = %v", err)
	}
	if project.timeout != defaultProjectReviewTimeout {
		t.Fatalf("project timeout = %s, want %s", project.timeout, defaultProjectReviewTimeout)
	}
	explicitProject, err := parseReviewCommandOptions([]string{"--project", "--timeout", "45s"})
	if err != nil {
		t.Fatalf("parseReviewCommandOptions(--project --timeout) error = %v", err)
	}
	if explicitProject.timeout != 45*time.Second {
		t.Fatalf("explicit project timeout = %s, want 45s", explicitProject.timeout)
	}

	pr, err := parseReviewPRCommandOptions([]string{"123"})
	if err != nil {
		t.Fatalf("parseReviewPRCommandOptions() error = %v", err)
	}
	if pr.timeout != defaultReviewTimeout {
		t.Fatalf("PR timeout = %s, want %s", pr.timeout, defaultReviewTimeout)
	}
}

func TestParseReviewCommandOptionsRejectsConflictingBudgetModes(t *testing.T) {
	_, err := parseReviewCommandOptions([]string{"--budget", "0.25", "--no-budget"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v, want conflicting budget modes", err)
	}

	_, err = parseReviewCommandOptions([]string{"--budget", "-0.25"})
	if err == nil || !strings.Contains(err.Error(), "must be zero or greater") {
		t.Fatalf("error = %v, want non-negative budget validation", err)
	}

	_, err = parseReviewCommandOptions([]string{"--max-tool-calls", "-1"})
	if err == nil || !strings.Contains(err.Error(), "--max-tool-calls must be zero or greater") {
		t.Fatalf("error = %v, want non-negative tool-call validation", err)
	}

	opts, err := parseReviewCommandOptions([]string{"--no-budget"})
	if err != nil {
		t.Fatalf("parseReviewCommandOptions(--no-budget) error = %v", err)
	}
	if !opts.noBudget {
		t.Fatal("noBudget = false, want true")
	}
}

func TestNewReviewProgressHonorsQuietMode(t *testing.T) {
	previous := quietMode
	t.Cleanup(func() { quietMode = previous })

	quietMode = true
	if _, ok := newReviewProgress("Reviewing").(silentReviewProgress); !ok {
		t.Fatal("quiet review should not create a spinner")
	}

	quietMode = false
	if _, ok := newReviewProgress("Reviewing").(*terminal.Spinner); !ok {
		t.Fatal("interactive review should create a spinner")
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

func TestResolveReviewModelDefaultsToBuckbot(t *testing.T) {
	previous := modelOverrideFlag
	modelOverrideFlag = ""
	t.Cleanup(func() { modelOverrideFlag = previous })
	t.Setenv("BUCKLEY_MODEL_REVIEW", "")

	cfg := config.DefaultConfig()
	cfg.Buckbot.Model = "buckbot/reviewer"
	cfg.Models.Review = "config/reviewer"
	cfg.Models.Execution = "config/executor"

	if got := resolveReviewModel(cfg); got != "buckbot/reviewer" {
		t.Fatalf("resolveReviewModel() = %q, want buckbot/reviewer", got)
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
	if got := reviewReasoningOverride(); got != "high" {
		t.Fatalf("review reasoning override = %q, want high", got)
	}
}

func TestResolveReviewModelUsesCodexAdaptiveBaseline(t *testing.T) {
	previous := modelOverrideFlag
	modelOverrideFlag = "codex/auto"
	t.Cleanup(func() { modelOverrideFlag = previous })

	if got := resolveReviewModel(config.DefaultConfig()); got != codexReviewModelStandard {
		t.Fatalf("resolveReviewModel() = %q, want Terra baseline", got)
	}
	if !isAdaptiveCodexReviewSelector(resolveReviewModelSelector(config.DefaultConfig())) {
		t.Fatal("codex/auto did not enable adaptive model selection")
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

func TestReviewResultFromAgentExposesPrimaryAndCriticAttempts(t *testing.T) {
	got := reviewResultFromAgent(&oneshot.RunResult{
		Attempts:        3,
		PrimaryAttempts: 1,
		CriticAttempts:  2,
		HostEvidence: []oneshot.AgentToolCall{
			{Name: "run_verification", Success: true},
			{Name: "run_verification", Success: false},
		},
	}, nil)

	if got.attempts != 3 || got.primary != 1 || got.criticAttempts != 2 {
		t.Fatalf("attempt counts = total:%d primary:%d critic:%d, want 3/1/2",
			got.attempts, got.primary, got.criticAttempts)
	}
	if got.hostEvidence != 2 || got.hostPasses != 1 {
		t.Fatalf("host evidence = %d total/%d passed, want 2/1", got.hostEvidence, got.hostPasses)
	}
}

func TestReviewResultFromAgentIgnoresTypedNilReview(t *testing.T) {
	var typedNil *commands.ReviewAgentResult
	got := reviewResultFromAgent(&oneshot.RunResult{Value: typedNil}, nil)

	if got == nil {
		t.Fatal("reviewResultFromAgent() = nil, want an empty result")
	}
	if got.reviewText != "" || got.parsed != nil {
		t.Fatalf("review result = %#v, want no review content", got)
	}
}

func TestReviewValidationRepairLinesExplainBoundedRetries(t *testing.T) {
	longReason := strings.Repeat("x", 220)
	trace := &transparency.Trace{Attempts: []transparency.TraceAttempt{
		{Phase: "primary", Attempt: 1, ValidationError: "missing coverage evidence"},
		{Phase: "primary", Attempt: 2},
		{Phase: "approval critic", Attempt: 1, ValidationError: longReason},
	}}

	got := reviewValidationRepairLines(trace)
	if len(got) != 2 {
		t.Fatalf("repair lines = %v, want two", got)
	}
	if got[0] != "Primary attempt 1: missing coverage evidence" {
		t.Fatalf("first repair line = %q", got[0])
	}
	if !strings.HasPrefix(got[1], "Approval critic attempt 1: ") || !strings.HasSuffix(got[1], "…") {
		t.Fatalf("second repair line = %q", got[1])
	}
}

func TestReviewResultFromAgentPreservesIncompleteState(t *testing.T) {
	exitCode := 0
	got := reviewResultFromAgent(&oneshot.RunResult{
		Value: &commands.ReviewAgentResult{Review: "partial review"},
		ToolEvidence: []oneshot.AgentToolCall{{
			ID: "host-evidence-1", Name: "run_verification", Arguments: `{"kind":"test"}`, Result: "status PASS", Success: true,
		}},
		CommandEvidence: []model.CommandExecutionEvidence{{
			Command: "go test ./pkg/oneshot", Status: "completed", ExitCode: &exitCode, AggregatedOutput: "ok pkg/oneshot",
		}},
		Trace: &transparency.Trace{Attempts: []transparency.TraceAttempt{{
			Phase:   "primary",
			Attempt: 1,
			Trace: &transparency.Trace{
				Content:  "raw rejected response",
				Duration: 2 * time.Second,
				Tokens:   transparency.TokenUsage{Input: 120, Output: 30},
				Request:  &transparency.RequestTrace{MaxTokens: 32768, ReasoningMaxTokens: 4096},
				Response: &transparency.ResponseTrace{FinishReason: "stop"},
			},
		}}},
		Incomplete:       true,
		IncompleteReason: "context deadline exceeded",
	}, nil)
	for _, want := range []string{
		"Incomplete review",
		"partial review",
		"Preserved execution evidence",
		"host-evidence-1",
		"go test ./pkg/oneshot",
		"Review attempt diagnostics",
		"Finish reason: `stop`",
		"120 input and 30 output",
		"Request limits: 32768 completion tokens (4096 reasoning tokens)",
		"raw rejected response",
	} {
		if !strings.Contains(got.reviewText, want) {
			t.Fatalf("incomplete review missing %q: %#v", want, got)
		}
	}
	if !got.incomplete || !strings.Contains(got.incompleteWhy, "deadline") {
		t.Fatalf("incomplete review result = %#v", got)
	}
	if got.parsed != nil {
		t.Fatal("incomplete review must not retain a parsed merge verdict")
	}
}
