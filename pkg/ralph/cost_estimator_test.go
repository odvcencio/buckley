// pkg/ralph/cost_estimator_test.go
package ralph

import (
	"math"
	"testing"
)

func TestNewCostEstimator(t *testing.T) {
	estimator := NewCostEstimator()
	if estimator == nil {
		t.Fatal("NewCostEstimator returned nil")
	}
	if estimator.prices == nil {
		t.Error("prices map should not be nil")
	}
}

func TestCostEstimator_Estimate(t *testing.T) {
	estimator := NewCostEstimator()

	tests := []struct {
		name      string
		model     string
		tokensIn  int
		tokensOut int
		expected  float64
	}{
		{
			name:      "claude opus 4 pricing",
			model:     "claude-opus-4",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  90.0, // 15 + 75
		},
		{
			name:      "claude sonnet 4 pricing",
			model:     "claude-sonnet-4",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  18.0, // 3 + 15
		},
		{
			name:      "claude haiku pricing",
			model:     "claude-haiku",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  1.5, // 0.25 + 1.25
		},
		{
			name:      "opus alias",
			model:     "opus",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  90.0, // 15 + 75
		},
		{
			name:      "sonnet alias",
			model:     "sonnet",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  18.0, // 3 + 15
		},
		{
			name:      "haiku alias",
			model:     "haiku",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  1.5, // 0.25 + 1.25
		},
		{
			name:      "gpt-4 pricing",
			model:     "gpt-4",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  90.0, // 30 + 60
		},
		{
			name:      "gpt-4-turbo pricing",
			model:     "gpt-4-turbo",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  40.0, // 10 + 30
		},
		{
			name:      "gpt-4o pricing",
			model:     "gpt-4o",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  12.5, // 2.5 + 10
		},
		{
			name:      "o3 pricing",
			model:     "o3",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  50.0, // 10 + 40
		},
		{
			name:      "unknown model uses default",
			model:     "unknown-model-xyz",
			tokensIn:  1_000_000,
			tokensOut: 1_000_000,
			expected:  20.0, // 5 + 15
		},
		{
			name:      "small token count",
			model:     "claude-sonnet-4",
			tokensIn:  2500,
			tokensOut: 1800,
			expected:  0.0345, // (2500/1M * 3) + (1800/1M * 15) = 0.0075 + 0.027 = 0.0345
		},
		{
			name:      "zero tokens",
			model:     "claude-sonnet-4",
			tokensIn:  0,
			tokensOut: 0,
			expected:  0.0,
		},
		{
			name:      "only input tokens",
			model:     "claude-sonnet-4",
			tokensIn:  1_000_000,
			tokensOut: 0,
			expected:  3.0, // input only
		},
		{
			name:      "only output tokens",
			model:     "claude-sonnet-4",
			tokensIn:  0,
			tokensOut: 1_000_000,
			expected:  15.0, // output only
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimator.Estimate(tt.model, tt.tokensIn, tt.tokensOut)
			if math.Abs(result-tt.expected) > 0.0001 {
				t.Errorf("Estimate(%s, %d, %d) = %f, want %f",
					tt.model, tt.tokensIn, tt.tokensOut, result, tt.expected)
			}
		})
	}
}

func TestCostEstimator_NilSafe(t *testing.T) {
	var estimator *CostEstimator
	result := estimator.Estimate("claude-sonnet-4", 1000, 500)
	if result != 0.0 {
		t.Errorf("nil estimator should return 0.0, got %f", result)
	}
}

func TestComputeBackendStats(t *testing.T) {
	t.Run("empty results", func(t *testing.T) {
		stats := ComputeBackendStats(nil)
		if len(stats) != 0 {
			t.Errorf("expected empty stats, got %d", len(stats))
		}

		stats = ComputeBackendStats([]*BackendResult{})
		if len(stats) != 0 {
			t.Errorf("expected empty stats, got %d", len(stats))
		}
	})

	t.Run("single backend", func(t *testing.T) {
		results := []*BackendResult{
			{Backend: "claude", Duration: 10_000_000_000, Cost: 0.05, CostEstimate: 0.05, TestsPassed: 10, TestsFailed: 0},
			{Backend: "claude", Duration: 20_000_000_000, Cost: 0.10, CostEstimate: 0.10, TestsPassed: 8, TestsFailed: 2},
		}

		stats := ComputeBackendStats(results)
		if len(stats) != 1 {
			t.Fatalf("expected 1 backend stat, got %d", len(stats))
		}

		s := stats[0]
		if s.Backend != "claude" {
			t.Errorf("expected backend=claude, got %s", s.Backend)
		}
		if s.Iterations != 2 {
			t.Errorf("expected iterations=2, got %d", s.Iterations)
		}
		if s.TotalTime != 30_000_000_000 {
			t.Errorf("expected total time 30s, got %v", s.TotalTime)
		}
		if s.AvgTime != 15_000_000_000 {
			t.Errorf("expected avg time 15s, got %v", s.AvgTime)
		}
		if math.Abs(s.TotalCost-0.15) > 0.0001 {
			t.Errorf("expected total cost 0.15, got %f", s.TotalCost)
		}
		if math.Abs(s.CostEstimate-0.15) > 0.0001 {
			t.Errorf("expected cost estimate 0.15, got %f", s.CostEstimate)
		}
		// (10+8)/(10+8+2) = 18/20 = 0.9
		if math.Abs(s.TestPassRate-0.9) > 0.0001 {
			t.Errorf("expected test pass rate 0.9, got %f", s.TestPassRate)
		}
	})

	t.Run("multiple backends", func(t *testing.T) {
		results := []*BackendResult{
			{Backend: "claude", Duration: 10_000_000_000, Cost: 0.05, CostEstimate: 0.05, TestsPassed: 10, TestsFailed: 0},
			{Backend: "codex", Duration: 5_000_000_000, Cost: 0.02, CostEstimate: 0.02, TestsPassed: 5, TestsFailed: 5},
			{Backend: "claude", Duration: 15_000_000_000, Cost: 0.07, CostEstimate: 0.07, TestsPassed: 12, TestsFailed: 0},
		}

		stats := ComputeBackendStats(results)
		if len(stats) != 2 {
			t.Fatalf("expected 2 backend stats, got %d", len(stats))
		}

		// Stats should be sorted by backend name
		var claudeStats, codexStats *BackendStats
		for i := range stats {
			if stats[i].Backend == "claude" {
				claudeStats = &stats[i]
			} else if stats[i].Backend == "codex" {
				codexStats = &stats[i]
			}
		}

		if claudeStats == nil {
			t.Fatal("claude stats not found")
		}
		if codexStats == nil {
			t.Fatal("codex stats not found")
		}

		if claudeStats.Iterations != 2 {
			t.Errorf("claude iterations: expected 2, got %d", claudeStats.Iterations)
		}
		if codexStats.Iterations != 1 {
			t.Errorf("codex iterations: expected 1, got %d", codexStats.Iterations)
		}

		// Claude: (10+12)/(10+12+0) = 22/22 = 1.0
		if math.Abs(claudeStats.TestPassRate-1.0) > 0.0001 {
			t.Errorf("claude pass rate: expected 1.0, got %f", claudeStats.TestPassRate)
		}

		// Codex: 5/(5+5) = 0.5
		if math.Abs(codexStats.TestPassRate-0.5) > 0.0001 {
			t.Errorf("codex pass rate: expected 0.5, got %f", codexStats.TestPassRate)
		}
	})

	t.Run("no tests ran", func(t *testing.T) {
		results := []*BackendResult{
			{Backend: "claude", Duration: 10_000_000_000, Cost: 0.05, CostEstimate: 0.05, TestsPassed: 0, TestsFailed: 0},
		}

		stats := ComputeBackendStats(results)
		if len(stats) != 1 {
			t.Fatalf("expected 1 backend stat, got %d", len(stats))
		}

		// No tests run means pass rate should be 0 (or undefined - we use 0)
		if stats[0].TestPassRate != 0.0 {
			t.Errorf("expected 0.0 pass rate when no tests, got %f", stats[0].TestPassRate)
		}
	})
}
