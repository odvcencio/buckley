package transparency

import (
	"sync"
	"time"
)

// TokenUsage tracks token consumption for an invocation.
type TokenUsage struct {
	// Input tokens sent to the model
	Input int `json:"input"`

	// Output tokens received from the model
	Output int `json:"output"`

	// Reasoning tokens (for thinking models like kimi-k2)
	Reasoning int `json:"reasoning,omitempty"`

	// CachedInput tokens that were cache hits (reduced cost)
	CachedInput int `json:"cached_input,omitempty"`
}

// Total returns the total token count.
func (tu TokenUsage) Total() int {
	return tu.Input + tu.Output + tu.Reasoning
}

// CostEntry represents the cost of a single LLM invocation.
type CostEntry struct {
	// Timestamp when the invocation occurred
	Timestamp time.Time `json:"timestamp"`

	// Model identifier
	Model string `json:"model"`

	// Tokens consumed
	Tokens TokenUsage `json:"tokens"`

	// Cost in USD
	Cost float64 `json:"cost"`

	// Latency of the request
	Latency time.Duration `json:"latency"`

	// InvocationID links to the full trace
	InvocationID string `json:"invocation_id,omitempty"`
}

// CostLedger tracks costs across a session.
// It provides running totals and historical data for transparency.
type CostLedger struct {
	mu      sync.Mutex
	entries []CostEntry
}

// NewCostLedger creates an empty cost ledger.
func NewCostLedger() *CostLedger {
	return &CostLedger{
		entries: make([]CostEntry, 0),
	}
}

// Record adds a cost entry to the ledger.
func (cl *CostLedger) Record(entry CostEntry) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	cl.entries = append(cl.entries, entry)
}

// SessionTotal returns the total cost for the current session.
func (cl *CostLedger) SessionTotal() float64 {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	var total float64
	for _, e := range cl.entries {
		total += e.Cost
	}
	return total
}

// SessionTokens returns total tokens for the current session.
func (cl *CostLedger) SessionTokens() TokenUsage {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	var total TokenUsage
	for _, e := range cl.entries {
		total.Input += e.Tokens.Input
		total.Output += e.Tokens.Output
		total.Reasoning += e.Tokens.Reasoning
		total.CachedInput += e.Tokens.CachedInput
	}
	return total
}

// InvocationCount returns the number of LLM invocations.
func (cl *CostLedger) InvocationCount() int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return len(cl.entries)
}

// Entries returns all cost entries.
func (cl *CostLedger) Entries() []CostEntry {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	entries := make([]CostEntry, len(cl.entries))
	copy(entries, cl.entries)
	return entries
}

// TodayTotal returns total cost for today (UTC).
func (cl *CostLedger) TodayTotal() float64 {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	today := time.Now().UTC().Truncate(24 * time.Hour)
	var total float64
	for _, e := range cl.entries {
		if e.Timestamp.UTC().Truncate(24 * time.Hour).Equal(today) {
			total += e.Cost
		}
	}
	return total
}

// Summary returns a human-readable cost summary.
type CostSummary struct {
	SessionCost     float64    `json:"session_cost"`
	TodayCost       float64    `json:"today_cost"`
	SessionTokens   TokenUsage `json:"session_tokens"`
	InvocationCount int        `json:"invocation_count"`
}

// Summary returns aggregated cost data.
func (cl *CostLedger) Summary() CostSummary {
	return CostSummary{
		SessionCost:     cl.SessionTotal(),
		TodayCost:       cl.TodayTotal(),
		SessionTokens:   cl.SessionTokens(),
		InvocationCount: cl.InvocationCount(),
	}
}

// ModelPricing contains per-model pricing information.
type ModelPricing struct {
	// InputPerMillion is the cost per million input tokens
	InputPerMillion float64

	// OutputPerMillion is the cost per million output tokens
	OutputPerMillion float64

	// ReasoningPerMillion is the cost per million reasoning tokens (if separate)
	ReasoningPerMillion float64

	// CachedInputPerMillion is the cost for cached input tokens
	CachedInputPerMillion float64
}

// Calculate computes the cost for given token usage.
func (mp ModelPricing) Calculate(usage TokenUsage) float64 {
	inputCost := float64(usage.Input-usage.CachedInput) * mp.InputPerMillion / 1_000_000
	cachedCost := float64(usage.CachedInput) * mp.CachedInputPerMillion / 1_000_000
	outputCost := float64(usage.Output) * mp.OutputPerMillion / 1_000_000

	reasoningCost := float64(0)
	if mp.ReasoningPerMillion > 0 {
		reasoningCost = float64(usage.Reasoning) * mp.ReasoningPerMillion / 1_000_000
	} else {
		// If no separate reasoning price, treat as output
		outputCost += float64(usage.Reasoning) * mp.OutputPerMillion / 1_000_000
	}

	return inputCost + cachedCost + outputCost + reasoningCost
}
