package config

import (
	"math"
	"strings"
	"testing"
)

// TestValidateReportsFirstViolationInOriginalOrder locks in the
// decomposition's ordering contract: Validate walks configValidators in
// the same order the original monolithic function checked each section,
// so a config with more than one violation still reports the same first
// error it did before the decomposition.
func TestValidateReportsFirstViolationInOriginalOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Orchestrator.TrustLevel = "chaotic"
	cfg.Execution.Mode = "bad-mode"
	cfg.Approval.Mode = "bad-approval"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for the invalid trust level")
	}
	const want = "invalid trust level: chaotic (must be conservative, balanced, or autonomous)"
	if err.Error() != want {
		t.Fatalf("expected trust level to be checked before execution mode or approval mode, got: %v", err)
	}
}

// TestValidateSectionValidatorsRunInSequence asserts every section
// validator in configValidators runs, not just the first: clearing the
// trust-level violation from
// TestValidateReportsFirstViolationInOriginalOrder must surface the next
// section's violation, in order.
func TestValidateSectionValidatorsRunInSequence(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Mode = "bad-mode"
	cfg.Approval.Mode = "bad-approval"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for the invalid execution mode")
	}
	const want = "invalid execution mode: bad-mode (valid: classic, rlm)"
	if err.Error() != want {
		t.Fatalf("expected execution mode to be checked before approval mode, got: %v", err)
	}
}

// TestValidateBatchNormalizesRemoteBranchNameAsSideEffect asserts the
// decomposed validateBatch still performs the one normalizing mutation
// the original inline check made: defaulting remote_branch.remote_name to
// "origin" when remote branches are enabled but no name was given.
func TestValidateBatchNormalizesRemoteBranchNameAsSideEffect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Batch.Enabled = true
	cfg.Batch.JobTemplate.Image = "buckley:latest"
	cfg.Batch.JobTemplate.Command = []string{"buckley"}
	cfg.Batch.JobTemplate.Args = []string{"execute-task"}
	cfg.Batch.JobTemplate.WorkspaceMountPath = "/workspace"
	cfg.Batch.RemoteBranch.Enabled = true
	cfg.Batch.RemoteBranch.Prefix = "automation/"
	cfg.Batch.RemoteBranch.RemoteName = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid batch config, got: %v", err)
	}
	if cfg.Batch.RemoteBranch.RemoteName != "origin" {
		t.Fatalf("expected remote_branch.remote_name to default to origin, got %q", cfg.Batch.RemoteBranch.RemoteName)
	}
}

func TestValidateRejectsNonFinitePublicAgentCostLimits(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  func(*Config)
	}{
		{name: "experiment NaN", set: func(c *Config) { c.Experiment.MaxCostPerRun = math.NaN() }},
		{name: "experiment infinity", set: func(c *Config) { c.Experiment.MaxCostPerRun = math.Inf(1) }},
		{name: "buckbot review negative", set: func(c *Config) { c.Buckbot.PerReviewBudgetUSD = -1 }},
		{name: "buckbot review infinity", set: func(c *Config) { c.Buckbot.PerReviewBudgetUSD = math.Inf(1) }},
		{name: "buckbot monthly NaN", set: func(c *Config) { c.Buckbot.MonthlyBudgetUSD = math.NaN() }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.set(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "finite and non-negative") {
				t.Fatalf("Validate error = %v", err)
			}
		})
	}
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("zero/default budgets rejected: %v", err)
	}
}
