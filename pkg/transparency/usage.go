package transparency

import (
	"strings"
	"sync"

	"github.com/odvcencio/buckley/pkg/rules"
)

// UsageSnapshot is the cumulative state at any point.
type UsageSnapshot struct {
	TotalInputTokens  uint64
	TotalOutputTokens uint64
	TotalCostUSD      float64
	Turns             uint32
	CostByModel       map[string]float64
}

// UsageTracker accumulates usage across turns with per-model pricing.
type UsageTracker struct {
	mu           sync.Mutex
	cumulative   UsageSnapshot
	pricingTable map[string]ModelPricing
}

func NewUsageTracker(pricingTable map[string]ModelPricing) *UsageTracker {
	return &UsageTracker{
		cumulative:   UsageSnapshot{CostByModel: make(map[string]float64)},
		pricingTable: pricingTable,
	}
}

// DefaultPricingTable returns per-model pricing for known Claude models.
func DefaultPricingTable() map[string]ModelPricing {
	return map[string]ModelPricing{
		"haiku": {
			InputPerMillion:       0.25,
			OutputPerMillion:      1.25,
			CachedInputPerMillion: 0.03,
		},
		"sonnet": {
			InputPerMillion:       3.00,
			OutputPerMillion:      15.00,
			CachedInputPerMillion: 0.30,
		},
		"opus": {
			InputPerMillion:       15.00,
			OutputPerMillion:      75.00,
			CachedInputPerMillion: 1.50,
		},
	}
}

// Record adds a turn's usage with the correct model pricing applied.
func (u *UsageTracker) Record(modelID string, usage TokenUsage) {
	u.mu.Lock()
	defer u.mu.Unlock()

	pricing := u.pricingFor(modelID)
	cost := pricing.Calculate(usage)

	u.cumulative.TotalInputTokens += uint64(usage.Input)
	u.cumulative.TotalOutputTokens += uint64(usage.Output)
	u.cumulative.TotalCostUSD += cost
	u.cumulative.Turns++
	u.cumulative.CostByModel[modelID] += cost
}

// Snapshot returns the current cumulative usage.
func (u *UsageTracker) Snapshot() UsageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	snap := u.cumulative
	snap.CostByModel = make(map[string]float64, len(u.cumulative.CostByModel))
	for k, v := range u.cumulative.CostByModel {
		snap.CostByModel[k] = v
	}
	return snap
}

// BudgetUtilization returns session spend / session budget.
func (u *UsageTracker) BudgetUtilization(sessionBudget float64) float64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if sessionBudget <= 0 {
		return 0
	}
	return u.cumulative.TotalCostUSD / sessionBudget
}

// BuildCostFacts creates CostFacts for arbiter evaluation.
func (u *UsageTracker) BuildCostFacts(sessionBudget, dailyBudget, monthlyBudget float64) rules.CostFacts {
	u.mu.Lock()
	defer u.mu.Unlock()
	return rules.CostFacts{
		SessionSpendUSD:   u.cumulative.TotalCostUSD,
		SessionBudgetUSD:  sessionBudget,
		DailyBudgetUSD:    dailyBudget,
		MonthlyBudgetUSD:  monthlyBudget,
		BudgetUtilization: u.cumulative.TotalCostUSD / sessionBudget,
		TurnCount:         int(u.cumulative.Turns),
	}
}

func (u *UsageTracker) pricingFor(modelID string) ModelPricing {
	lower := strings.ToLower(modelID)
	// Try exact match first
	if p, ok := u.pricingTable[lower]; ok {
		return p
	}
	// Try substring match
	for key, p := range u.pricingTable {
		if strings.Contains(lower, key) {
			return p
		}
	}
	// Default to sonnet pricing
	if p, ok := u.pricingTable["sonnet"]; ok {
		return p
	}
	return ModelPricing{}
}
