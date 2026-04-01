package transparency

import (
	"testing"
)

func TestUsageTracker_Record(t *testing.T) {
	tracker := NewUsageTracker(DefaultPricingTable())
	tracker.Record("claude-sonnet-4-20250514", TokenUsage{Input: 1000, Output: 500})

	snap := tracker.Snapshot()
	if snap.TotalInputTokens != 1000 {
		t.Errorf("input tokens = %d, want 1000", snap.TotalInputTokens)
	}
	if snap.TotalOutputTokens != 500 {
		t.Errorf("output tokens = %d, want 500", snap.TotalOutputTokens)
	}
	if snap.TotalCostUSD <= 0 {
		t.Error("expected non-zero cost")
	}
	if snap.Turns != 1 {
		t.Errorf("turns = %d, want 1", snap.Turns)
	}
	if snap.CostByModel["claude-sonnet-4-20250514"] <= 0 {
		t.Error("expected per-model cost entry")
	}
}

func TestUsageTracker_MultipleTurns(t *testing.T) {
	tracker := NewUsageTracker(DefaultPricingTable())
	tracker.Record("claude-sonnet-4-20250514", TokenUsage{Input: 1000, Output: 500})
	tracker.Record("claude-haiku-4-20250514", TokenUsage{Input: 2000, Output: 1000})

	snap := tracker.Snapshot()
	if snap.Turns != 2 {
		t.Errorf("turns = %d, want 2", snap.Turns)
	}
	if snap.TotalInputTokens != 3000 {
		t.Errorf("input = %d, want 3000", snap.TotalInputTokens)
	}
	if len(snap.CostByModel) != 2 {
		t.Errorf("cost by model entries = %d, want 2", len(snap.CostByModel))
	}
}

func TestUsageTracker_BudgetUtilization(t *testing.T) {
	tracker := NewUsageTracker(DefaultPricingTable())
	tracker.Record("claude-sonnet-4-20250514", TokenUsage{Input: 1000000, Output: 500000})

	util := tracker.BudgetUtilization(10.0)
	if util <= 0 || util > 10.0 {
		t.Errorf("budget utilization = %f, expected positive", util)
	}
}

func TestUsageTracker_BuildCostFacts(t *testing.T) {
	tracker := NewUsageTracker(DefaultPricingTable())
	tracker.Record("claude-sonnet-4-20250514", TokenUsage{Input: 1000, Output: 500})

	facts := tracker.BuildCostFacts(10.0, 20.0, 200.0)
	if facts.SessionBudgetUSD != 10.0 {
		t.Errorf("session_budget = %f, want 10", facts.SessionBudgetUSD)
	}
	if facts.SessionSpendUSD <= 0 {
		t.Error("expected non-zero session spend")
	}
	if facts.TurnCount != 1 {
		t.Errorf("turn_count = %d, want 1", facts.TurnCount)
	}
}

func TestUsageTracker_PricingLookup(t *testing.T) {
	tracker := NewUsageTracker(DefaultPricingTable())

	// Record with different model IDs to test substring matching
	tracker.Record("claude-opus-4-20250514", TokenUsage{Input: 1000, Output: 500})
	tracker.Record("claude-haiku-4-20250514", TokenUsage{Input: 1000, Output: 500})

	snap := tracker.Snapshot()
	// Opus should cost more than haiku for same tokens
	if snap.CostByModel["claude-opus-4-20250514"] <= snap.CostByModel["claude-haiku-4-20250514"] {
		t.Error("opus should cost more than haiku")
	}
}
