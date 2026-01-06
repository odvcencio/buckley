package transparency

import (
	"testing"
	"time"
)

func TestTokenUsage(t *testing.T) {
	usage := TokenUsage{
		Input:     1000,
		Output:    200,
		Reasoning: 500,
	}

	if total := usage.Total(); total != 1700 {
		t.Errorf("expected total 1700, got %d", total)
	}
}

func TestCostLedger(t *testing.T) {
	ledger := NewCostLedger()

	ledger.Record(CostEntry{
		Model: "model-a",
		Cost:  0.01,
		Tokens: TokenUsage{
			Input:  100,
			Output: 50,
		},
	})

	ledger.Record(CostEntry{
		Model: "model-b",
		Cost:  0.02,
		Tokens: TokenUsage{
			Input:  200,
			Output: 100,
		},
	})

	// Test session total
	if total := ledger.SessionTotal(); total != 0.03 {
		t.Errorf("expected session total 0.03, got %f", total)
	}

	// Test invocation count
	if count := ledger.InvocationCount(); count != 2 {
		t.Errorf("expected 2 invocations, got %d", count)
	}

	// Test session tokens
	tokens := ledger.SessionTokens()
	if tokens.Input != 300 {
		t.Errorf("expected 300 input tokens, got %d", tokens.Input)
	}
	if tokens.Output != 150 {
		t.Errorf("expected 150 output tokens, got %d", tokens.Output)
	}
}

func TestCostLedgerTodayTotal(t *testing.T) {
	ledger := NewCostLedger()

	// Entry from today
	ledger.Record(CostEntry{
		Timestamp: time.Now(),
		Cost:      0.05,
	})

	// Entry from yesterday (manually set)
	yesterday := time.Now().Add(-24 * time.Hour)
	ledger.mu.Lock()
	ledger.entries = append(ledger.entries, CostEntry{
		Timestamp: yesterday,
		Cost:      0.10,
	})
	ledger.mu.Unlock()

	today := ledger.TodayTotal()
	if today != 0.05 {
		t.Errorf("expected today total 0.05, got %f", today)
	}
}

func TestCostLedgerSummary(t *testing.T) {
	ledger := NewCostLedger()

	ledger.Record(CostEntry{
		Cost: 0.01,
		Tokens: TokenUsage{
			Input:  100,
			Output: 50,
		},
	})

	summary := ledger.Summary()

	if summary.SessionCost != 0.01 {
		t.Errorf("expected session cost 0.01, got %f", summary.SessionCost)
	}
	if summary.InvocationCount != 1 {
		t.Errorf("expected 1 invocation, got %d", summary.InvocationCount)
	}
}

func TestModelPricing(t *testing.T) {
	pricing := ModelPricing{
		InputPerMillion:  3.00,  // $3 per million input
		OutputPerMillion: 15.00, // $15 per million output
	}

	usage := TokenUsage{
		Input:  1000,
		Output: 500,
	}

	cost := pricing.Calculate(usage)

	// 1000 input tokens = $0.003
	// 500 output tokens = $0.0075
	// Total = $0.0105
	expected := 0.0105
	if cost < expected-0.0001 || cost > expected+0.0001 {
		t.Errorf("expected cost ~%f, got %f", expected, cost)
	}
}

func TestModelPricingWithCached(t *testing.T) {
	pricing := ModelPricing{
		InputPerMillion:       3.00,
		OutputPerMillion:      15.00,
		CachedInputPerMillion: 0.30, // 10x cheaper for cached
	}

	usage := TokenUsage{
		Input:       1000,
		CachedInput: 800, // 800 of 1000 were cached
		Output:      500,
	}

	cost := pricing.Calculate(usage)

	// 200 fresh input tokens = $0.0006
	// 800 cached input tokens = $0.00024
	// 500 output tokens = $0.0075
	// Total = $0.00834
	expected := 0.00834
	if cost < expected-0.0001 || cost > expected+0.0001 {
		t.Errorf("expected cost ~%f, got %f", expected, cost)
	}
}
