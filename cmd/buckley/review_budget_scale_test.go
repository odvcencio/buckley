package main

import (
	"math"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestCountChangedLines(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n context\n-old\n+new\n+added\n\\ No newline at end of file\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-x\n+y\n"
	if got := countChangedLines(diff); got != 5 {
		t.Fatalf("countChangedLines = %d, want 5 (headers excluded)", got)
	}
	if got := countChangedLines(""); got != 0 {
		t.Fatalf("empty diff = %d", got)
	}
}

func TestScaledReviewBudget(t *testing.T) {
	cases := []struct {
		name                string
		base, perKloc, ceil float64
		lines               int
		want                float64
	}{
		{"scaling off keeps base", 0.5, 0, 0, 5000, 0.5},
		{"scaling off, no base, uncapped", 0, 0, 0, 5000, 0},
		{"base plus per kloc", 0.5, 0.2, 0, 2500, 1.0},
		{"no base cap stays uncapped", 0, 0.2, 0, 1000, 0},
		{"ceiling clamps", 0.5, 1, 2, 10000, 2},
		{"ceiling below base still lets base stand", 3, 1, 2, 100, 3},
		{"tiny diff stays near base", 0.5, 0.2, 0, 10, 0.502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scaledReviewBudgetUSD(tc.base, tc.perKloc, tc.ceil, tc.lines)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("scaledReviewBudgetUSD = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWithSizeScaledBudgetRespectsOverrides(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Buckbot.PerReviewBudgetUSD = 0.5
	cfg.Buckbot.BudgetPerKlocUSD = 0.2
	cfg.Buckbot.MaxReviewBudgetUSD = 3
	base := defaultAutomatedReviewOptions(cfg)

	if got := base.withSizeScaledBudget(5000).maxCostUSD; math.Abs(got-1.5) > 1e-9 {
		t.Fatalf("scaled budget = %v, want 1.5", got)
	}
	flag := base.withOverrides(automatedReviewOptions{maxCostUSD: 0.75})
	if got := flag.withSizeScaledBudget(5000).maxCostUSD; got != 0.75 {
		t.Fatalf("--budget was rescaled to %v, want 0.75", got)
	}
	none := base.withOverrides(automatedReviewOptions{clearCostBudget: true})
	if got := none.withSizeScaledBudget(5000).maxCostUSD; got != 0 {
		t.Fatalf("--no-budget was rescaled to %v, want 0", got)
	}
	cfg.Buckbot.BudgetPerKlocUSD = 0
	if got := defaultAutomatedReviewOptions(cfg).withSizeScaledBudget(5000).maxCostUSD; got != 0.5 {
		t.Fatalf("without per-kloc the base %v must stand, got %v", 0.5, got)
	}
}

func TestBuckbotBudgetScalingDefaults(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Buckbot.BudgetPerKlocUSD != 0.10 || cfg.Buckbot.MaxReviewBudgetUSD != 0 {
		t.Fatalf("defaults per_kloc=%v ceiling=%v, want 0.10 and 0 (no ceiling)",
			cfg.Buckbot.BudgetPerKlocUSD, cfg.Buckbot.MaxReviewBudgetUSD)
	}
}

func TestBuckbotBudgetScalingConfigValidation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Buckbot.BudgetPerKlocUSD = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative budget_per_kloc_usd accepted")
	}
	cfg = config.DefaultConfig()
	cfg.Buckbot.MaxReviewBudgetUSD = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative max_review_budget_usd accepted")
	}
}
