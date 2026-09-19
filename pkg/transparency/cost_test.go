package transparency

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"
)

func TestTokenUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage TokenUsage
		want  int
	}{
		{
			name:  "split plus reasoning",
			usage: TokenUsage{Input: 1000, Output: 200, Reasoning: 500},
			want:  1700,
		},
		{
			name:  "unclassified additive",
			usage: TokenUsage{Input: 1000, Output: 200, Reasoning: 500, Unclassified: 42},
			want:  1742,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if total := tt.usage.Total(); total != tt.want {
				t.Errorf("expected total %d, got %d", tt.want, total)
			}
		})
	}
}

func TestTokenUsageUnclassifiedJSONRoundTrip(t *testing.T) {
	reasoning := 0
	cached := 0
	usage := TokenUsage{
		Input:                     10,
		Output:                    20,
		Reasoning:                 3,
		Unclassified:              42,
		CachedInput:               4,
		ReportedTotal:             75,
		ReportedReasoning:         &reasoning,
		ReportedCachedInput:       &cached,
		ReportedCacheWrite:        5,
		ReportedUsageInconsistent: true,
		Estimated:                 true,
	}
	data, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded TokenUsage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Input != usage.Input ||
		decoded.Output != usage.Output ||
		decoded.Reasoning != usage.Reasoning ||
		decoded.Unclassified != usage.Unclassified ||
		decoded.CachedInput != usage.CachedInput ||
		decoded.ReportedTotal != usage.ReportedTotal ||
		decoded.ReportedCacheWrite != usage.ReportedCacheWrite ||
		decoded.ReportedUsageInconsistent != usage.ReportedUsageInconsistent ||
		decoded.Estimated != usage.Estimated ||
		decoded.ReportedReasoning == nil ||
		*decoded.ReportedReasoning != reasoning ||
		decoded.ReportedCachedInput == nil ||
		*decoded.ReportedCachedInput != cached {
		t.Fatalf("decoded = %+v, want %+v", decoded, usage)
	}
	if decoded.Total() != 75 {
		t.Fatalf("decoded total = %d, want 75", decoded.Total())
	}
}

func TestAddAndCloneTokenUsagePreserveReportedDetails(t *testing.T) {
	reasoningA := 2
	cachedA := 3
	reasoningB := 0
	cachedB := 4
	first := TokenUsage{Input: 10, Output: 5, ReportedTotal: 15, ReportedReasoning: &reasoningA, ReportedCachedInput: &cachedA}
	second := TokenUsage{Unclassified: 7, ReportedTotal: 7, ReportedReasoning: &reasoningB, ReportedCachedInput: &cachedB, ReportedCacheWrite: 6, ReportedUsageInconsistent: true, Estimated: true}

	total := AddTokenUsage(first, second)
	reasoningA = 99
	cachedB = 99
	if total.Total() != 22 || total.ReportedTotal != 22 || total.ReportedReasoning == nil || *total.ReportedReasoning != 2 ||
		total.ReportedCachedInput == nil || *total.ReportedCachedInput != 7 || total.ReportedCacheWrite != 6 || !total.ReportedUsageInconsistent || !total.Estimated {
		t.Fatalf("total usage = %+v, want additive totals and cloned reported details", total)
	}

	cloned := CloneTokenUsage(total)
	*total.ReportedReasoning = 42
	*total.ReportedCachedInput = 43
	if *cloned.ReportedReasoning != 2 || *cloned.ReportedCachedInput != 7 {
		t.Fatalf("clone aliased reported details: cloned=%+v mutated=%+v", cloned, total)
	}
}

func TestCostLedger(t *testing.T) {
	ledger := NewCostLedger()

	ledger.Record(CostEntry{
		Model: "model-a",
		Cost:  0.01,
		Tokens: TokenUsage{
			Input:                     100,
			Output:                    50,
			Unclassified:              7,
			ReportedTotal:             157,
			ReportedCacheWrite:        2,
			ReportedUsageInconsistent: true,
		},
	})

	reportedReasoning := 3
	reportedCached := 4
	ledger.Record(CostEntry{
		Model: "model-b",
		Cost:  0.02,
		Tokens: TokenUsage{
			Input:               200,
			Output:              100,
			Unclassified:        8,
			ReportedTotal:       308,
			ReportedReasoning:   &reportedReasoning,
			ReportedCachedInput: &reportedCached,
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
	if tokens.Unclassified != 15 {
		t.Errorf("expected 15 unclassified tokens, got %d", tokens.Unclassified)
	}
	if tokens.ReportedTotal != 465 || tokens.ReportedReasoning == nil || *tokens.ReportedReasoning != 3 ||
		tokens.ReportedCachedInput == nil || *tokens.ReportedCachedInput != 4 || tokens.ReportedCacheWrite != 2 {
		t.Fatalf("reported token evidence = %+v, want summed reported fields", tokens)
	}
	if !tokens.ReportedUsageInconsistent {
		t.Fatalf("reported usage inconsistent = false, want OR-preserved inconsistent evidence")
	}
	if tokens.Total() != 465 {
		t.Errorf("expected 465 total tokens, got %d", tokens.Total())
	}
	reportedReasoning = 99
	entries := ledger.Entries()
	if entries[1].Tokens.ReportedReasoning == nil || *entries[1].Tokens.ReportedReasoning != 3 {
		t.Fatalf("ledger entry aliased caller reported reasoning: %+v", entries[1].Tokens)
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

func TestCostLedgerSummaryTracksUnknownCost(t *testing.T) {
	ledger := NewCostLedger()
	ledger.Record(CostEntry{
		Cost:        0.01,
		CostUnknown: false,
		Tokens:      TokenUsage{Input: 100},
	})
	ledger.Record(CostEntry{
		Cost:        0,
		CostUnknown: true,
		Tokens:      TokenUsage{Unclassified: 42},
	})
	yesterday := time.Now().Add(-24 * time.Hour)
	ledger.mu.Lock()
	ledger.entries = append(ledger.entries, CostEntry{
		Timestamp:   yesterday,
		CostUnknown: true,
		Tokens:      TokenUsage{Output: 1},
	})
	ledger.mu.Unlock()

	summary := ledger.Summary()
	if summary.SessionCost != 0.01 || !summary.SessionCostUnknown {
		t.Fatalf("session cost/unknown = %f/%v, want known subtotal plus unknown", summary.SessionCost, summary.SessionCostUnknown)
	}
	if summary.TodayCost != 0.01 || !summary.TodayCostUnknown {
		t.Fatalf("today cost/unknown = %f/%v, want known subtotal plus today's unknown", summary.TodayCost, summary.TodayCostUnknown)
	}
	if summary.SessionTokens.Total() != 143 {
		t.Fatalf("session tokens = %+v, want unknown-cost tokens retained", summary.SessionTokens)
	}
}

func TestCostLedgerSummaryIsSingleSnapshotUnderConcurrentRecord(t *testing.T) {
	ledger := NewCostLedger()
	const entries = 200
	var wg sync.WaitGroup
	done := make(chan struct{})
	inconsistent := make(chan CostSummary, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < entries; i++ {
			ledger.Record(CostEntry{
				Cost:        0.01,
				CostUnknown: true,
				Tokens:      TokenUsage{Input: 1, Unclassified: 1},
			})
		}
		close(done)
	}()

	for {
		summary := ledger.Summary()
		count := summary.InvocationCount
		if count > 0 {
			wantCost := float64(count) * 0.01
			if summary.SessionTokens.Input != count ||
				summary.SessionTokens.Unclassified != count ||
				summary.SessionTokens.Total() != count*2 ||
				math.Abs(summary.SessionCost-wantCost) > 1e-9 ||
				!summary.SessionCostUnknown ||
				!summary.TodayCostUnknown {
				select {
				case inconsistent <- summary:
				default:
				}
				<-done
				wg.Wait()
				t.Fatalf("observed inconsistent summary: %+v", <-inconsistent)
			}
		}
		select {
		case <-done:
			wg.Wait()
			if len(inconsistent) > 0 {
				t.Fatalf("observed inconsistent summary: %+v", <-inconsistent)
			}
			final := ledger.Summary()
			if final.InvocationCount != entries || final.SessionTokens.Input != entries || final.SessionTokens.Unclassified != entries {
				t.Fatalf("final summary = %+v, want %d complete entries", final, entries)
			}
			return
		default:
		}
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

func TestModelPricingDoesNotGuessUnclassifiedSplit(t *testing.T) {
	pricing := ModelPricing{
		InputPerMillion:     3.00,
		OutputPerMillion:    15.00,
		ReasoningPerMillion: 6.00,
	}

	if cost := pricing.Calculate(TokenUsage{Unclassified: 42}); cost != 0 {
		t.Fatalf("cost = %f, want 0 for unclassified-only usage", cost)
	}
}
