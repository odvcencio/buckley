// pkg/ralph/cost_estimator.go
package ralph

import (
	"sort"
	"time"
)

// defaultPrices contains OpenRouter pricing per 1M tokens [input, output].
var defaultPrices = map[string][2]float64{
	// Anthropic models
	"claude-opus-4":   {15.0, 75.0},
	"claude-sonnet-4": {3.0, 15.0},
	"claude-haiku":    {0.25, 1.25},
	"opus":            {15.0, 75.0}, // alias
	"sonnet":          {3.0, 15.0},  // alias
	"haiku":           {0.25, 1.25}, // alias

	// OpenAI models
	"gpt-4":       {30.0, 60.0},
	"gpt-4-turbo": {10.0, 30.0},
	"gpt-4o":      {2.5, 10.0},
	"o3":          {10.0, 40.0}, // estimate

	// Default fallback
	"default": {5.0, 15.0},
}

// CostEstimator provides cost estimates based on OpenRouter pricing.
type CostEstimator struct {
	prices map[string][2]float64
}

// CostEstimatorOption configures a CostEstimator.
type CostEstimatorOption func(*CostEstimator)

// WithCustomPrices sets custom prices for specific models.
// Prices are in dollars per 1M tokens: [inputPrice, outputPrice].
func WithCustomPrices(prices map[string][2]float64) CostEstimatorOption {
	return func(e *CostEstimator) {
		for k, v := range prices {
			e.prices[k] = v
		}
	}
}

// WithDefaultPrice sets the fallback price for unknown models.
func WithDefaultPrice(inputPerMillion, outputPerMillion float64) CostEstimatorOption {
	return func(e *CostEstimator) {
		e.prices["default"] = [2]float64{inputPerMillion, outputPerMillion}
	}
}

// NewCostEstimator creates a new cost estimator with default OpenRouter prices.
// Options can be used to customize or override prices.
func NewCostEstimator(opts ...CostEstimatorOption) *CostEstimator {
	prices := make(map[string][2]float64, len(defaultPrices))
	for k, v := range defaultPrices {
		prices[k] = v
	}
	e := &CostEstimator{prices: prices}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// SetPrice sets or updates the price for a specific model.
// Prices are in dollars per 1M tokens.
func (e *CostEstimator) SetPrice(model string, inputPerMillion, outputPerMillion float64) {
	if e == nil {
		return
	}
	e.prices[model] = [2]float64{inputPerMillion, outputPerMillion}
}

// GetPrice returns the price for a model. Returns false if the model is not found.
func (e *CostEstimator) GetPrice(model string) (inputPerMillion, outputPerMillion float64, ok bool) {
	if e == nil {
		return 0, 0, false
	}
	prices, ok := e.prices[model]
	if !ok {
		return 0, 0, false
	}
	return prices[0], prices[1], true
}

// ListModels returns all models with configured prices.
func (e *CostEstimator) ListModels() []string {
	if e == nil {
		return nil
	}
	models := make([]string, 0, len(e.prices))
	for k := range e.prices {
		models = append(models, k)
	}
	sort.Strings(models)
	return models
}

// Estimate calculates the estimated cost for the given model and token counts.
func (e *CostEstimator) Estimate(model string, tokensIn, tokensOut int) float64 {
	if e == nil {
		return 0.0
	}

	prices, ok := e.prices[model]
	if !ok {
		prices = e.prices["default"]
	}

	inputCost := float64(tokensIn) / 1_000_000 * prices[0]
	outputCost := float64(tokensOut) / 1_000_000 * prices[1]
	return inputCost + outputCost
}

// BackendStats holds aggregated statistics for a backend across multiple executions.
// It provides summary metrics useful for comparing backend performance.
type BackendStats struct {
	// Backend is the unique identifier for the backend.
	Backend string
	// Iterations is the number of executions completed by this backend.
	Iterations int
	// TotalTime is the cumulative duration of all executions.
	TotalTime time.Duration
	// AvgTime is the average duration per execution.
	AvgTime time.Duration
	// TotalCost is the actual cost in dollars (0 if subscription-based).
	TotalCost float64
	// CostEstimate is the OpenRouter-based cost estimate.
	CostEstimate float64
	// TestPassRate is the ratio of passed tests to total tests (0-1).
	TestPassRate float64
}

// ComputeBackendStats computes stats from a slice of results.
func ComputeBackendStats(results []*BackendResult) []BackendStats {
	if len(results) == 0 {
		return nil
	}

	// Aggregate by backend
	type aggregated struct {
		iterations   int
		totalTime    time.Duration
		totalCost    float64
		costEstimate float64
		testsPassed  int
		testsFailed  int
	}

	agg := make(map[string]*aggregated)

	for _, r := range results {
		if r == nil {
			continue
		}

		a, ok := agg[r.Backend]
		if !ok {
			a = &aggregated{}
			agg[r.Backend] = a
		}

		a.iterations++
		a.totalTime += r.Duration
		a.totalCost += r.Cost
		a.costEstimate += r.CostEstimate
		a.testsPassed += r.TestsPassed
		a.testsFailed += r.TestsFailed
	}

	// Convert to stats slice
	stats := make([]BackendStats, 0, len(agg))
	for backend, a := range agg {
		var avgTime time.Duration
		if a.iterations > 0 {
			avgTime = a.totalTime / time.Duration(a.iterations)
		}

		var passRate float64
		totalTests := a.testsPassed + a.testsFailed
		if totalTests > 0 {
			passRate = float64(a.testsPassed) / float64(totalTests)
		}

		stats = append(stats, BackendStats{
			Backend:      backend,
			Iterations:   a.iterations,
			TotalTime:    a.totalTime,
			AvgTime:      avgTime,
			TotalCost:    a.totalCost,
			CostEstimate: a.costEstimate,
			TestPassRate: passRate,
		})
	}

	// Sort by backend name for deterministic output
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Backend < stats[j].Backend
	})

	return stats
}
