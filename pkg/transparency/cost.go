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

	// Unclassified tokens reported only as a total with no known input/output
	// split. These count toward totals but are not priced as a guessed split.
	Unclassified int `json:"unclassified,omitempty"`

	// CachedInput tokens that were cache hits (reduced cost)
	CachedInput int `json:"cached_input,omitempty"`

	// ReportedTotal preserves a provider-reported total separately from
	// Buckley's additive total so inconsistent or total-only usage survives
	// without double-counting.
	ReportedTotal int `json:"reported_total,omitempty"`

	// ReportedReasoning preserves provider reasoning-token details that are a
	// subset of completion/output tokens, not additional billable tokens.
	ReportedReasoning *int `json:"reported_reasoning,omitempty"`

	// ReportedCachedInput preserves provider cached-prompt details that are a
	// subset of input tokens. Pricing requires authoritative cache rates.
	ReportedCachedInput *int `json:"reported_cached_input,omitempty"`

	// ReportedCacheWrite preserves provider cache-write details. Pricing
	// requires authoritative cache-write rates.
	ReportedCacheWrite int `json:"reported_cache_write,omitempty"`

	// ReportedUsageInconsistent marks provider usage details that contradicted
	// themselves before aggregation, such as a reported total below the reported
	// split or invalid negative/subset counts.
	ReportedUsageInconsistent bool `json:"reported_usage_inconsistent,omitempty"`

	// Estimated marks locally derived usage that should not be treated as an
	// authoritative provider invoice.
	Estimated bool `json:"estimated,omitempty"`

	// UsageEvidencePresent records that a real response carried provider usage
	// evidence, even when the reported counts were explicitly zero.
	UsageEvidencePresent bool `json:"usage_evidence_present,omitempty"`

	// UsageEvidenceMissing records that a real model response was observed but
	// did not carry provider usage evidence. Counts remain as reported/absent;
	// cost inference must not treat the response as a known free invocation.
	UsageEvidenceMissing bool `json:"usage_evidence_missing,omitempty"`

	// ProviderCostUSD is the provider's own authoritative cost for this
	// invocation in USD (C7), already summing any BYOK upstream charge (see
	// ProviderCostIsBYOK). Meaningful only when ProviderCostKnown is true;
	// callers must fall back to catalog-pricing estimation otherwise. When
	// known, this takes precedence over catalog pricing -- it is a real
	// invoice line, not an estimate. Aggregation (AddTokenUsage) sums
	// whatever segments reported a known cost, the same permissive
	// convention as ReportedReasoning/ReportedCachedInput: a segment with no
	// cost evidence contributes 0 rather than invalidating the whole total.
	ProviderCostUSD float64 `json:"provider_cost_usd,omitempty"`

	// ProviderCostKnown marks ProviderCostUSD as a real provider-reported
	// value rather than an unset zero.
	ProviderCostKnown bool `json:"provider_cost_known,omitempty"`

	// ProviderCostIsBYOK marks ProviderCostUSD as including a bring-your-
	// own-key upstream charge the caller pays directly to the upstream
	// provider, not OpenRouter. Surfaced so cost output can attribute it
	// correctly instead of implying OpenRouter itself charged the full
	// amount.
	ProviderCostIsBYOK bool `json:"provider_cost_is_byok,omitempty"`
}

// Total returns the total token count.
func (tu TokenUsage) Total() int {
	return tu.Input + tu.Output + tu.Reasoning + tu.Unclassified
}

// CloneTokenUsage returns a deep copy of usage metadata, including optional
// provider detail pointers.
func CloneTokenUsage(usage TokenUsage) TokenUsage {
	if usage.ReportedReasoning != nil {
		value := *usage.ReportedReasoning
		usage.ReportedReasoning = &value
	}
	if usage.ReportedCachedInput != nil {
		value := *usage.ReportedCachedInput
		usage.ReportedCachedInput = &value
	}
	return usage
}

// AddTokenUsage combines usage while preserving non-additive provider details
// separately from Buckley's legacy additive total.
func AddTokenUsage(total, next TokenUsage) TokenUsage {
	total = CloneTokenUsage(total)
	total.Input += next.Input
	total.Output += next.Output
	total.Reasoning += next.Reasoning
	total.Unclassified += next.Unclassified
	total.CachedInput += next.CachedInput
	total.ReportedTotal += next.ReportedTotal
	total.ReportedCacheWrite += next.ReportedCacheWrite
	total.ReportedUsageInconsistent = total.ReportedUsageInconsistent || next.ReportedUsageInconsistent
	total.Estimated = total.Estimated || next.Estimated
	total.UsageEvidencePresent = total.UsageEvidencePresent || next.UsageEvidencePresent
	total.UsageEvidenceMissing = total.UsageEvidenceMissing || next.UsageEvidenceMissing
	if next.ProviderCostKnown {
		total.ProviderCostUSD += next.ProviderCostUSD
		total.ProviderCostKnown = true
	}
	total.ProviderCostIsBYOK = total.ProviderCostIsBYOK || next.ProviderCostIsBYOK
	if next.ReportedReasoning != nil {
		if total.ReportedReasoning == nil {
			total.ReportedReasoning = new(int)
		}
		*total.ReportedReasoning += *next.ReportedReasoning
	}
	if next.ReportedCachedInput != nil {
		if total.ReportedCachedInput == nil {
			total.ReportedCachedInput = new(int)
		}
		*total.ReportedCachedInput += *next.ReportedCachedInput
	}
	return total
}

// CostUnknownForUsage reports whether a token record cannot be priced as an
// authoritative subtotal with the supplied basic pricing table.
func CostUnknownForUsage(tokens TokenUsage, pricing ModelPricing) bool {
	if tokens.Estimated || tokens.Unclassified > 0 || tokens.ReportedUsageInconsistent {
		return true
	}
	if tokens.UsageEvidenceMissing && (pricing.InputPerMillion != 0 || pricing.OutputPerMillion != 0 ||
		pricing.ReasoningPerMillion != 0 || pricing.CachedInputPerMillion != 0) {
		return true
	}
	if tokens.Input < 0 || tokens.Output < 0 || tokens.Reasoning < 0 || tokens.Unclassified < 0 ||
		tokens.CachedInput < 0 || tokens.ReportedTotal < 0 || tokens.ReportedCacheWrite < 0 {
		return true
	}
	if tokens.ReportedReasoning != nil {
		if *tokens.ReportedReasoning < 0 || *tokens.ReportedReasoning > tokens.Output {
			return true
		}
		if *tokens.ReportedReasoning > 0 && pricing.ReasoningPerMillion > 0 {
			return true
		}
	}
	if tokens.ReportedCachedInput != nil {
		if *tokens.ReportedCachedInput < 0 || *tokens.ReportedCachedInput > tokens.Input {
			return true
		}
		// Mirrors the ReportedReasoning rule above: only mark unknown when a
		// distinct cached rate exists that Calculate cannot apply (it prices
		// off the additive CachedInput field, not ReportedCachedInput). When
		// no cached rate is configured for the model, Calculate already
		// prices every input token at the uniform input rate, which is a
		// safe (if imprecise, never negative) known cost.
		if *tokens.ReportedCachedInput > 0 && pricing.CachedInputPerMillion > 0 {
			return true
		}
	}
	if tokens.ReportedCacheWrite > 0 {
		return true
	}
	return false
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

	// CostUnknown reports that Cost is only a known subtotal because this
	// invocation had usage without authoritative pricing.
	CostUnknown bool `json:"cost_unknown,omitempty"`

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
	entry.Tokens = CloneTokenUsage(entry.Tokens)
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
		total = AddTokenUsage(total, e.Tokens)
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
	for i, entry := range cl.entries {
		entry.Tokens = CloneTokenUsage(entry.Tokens)
		entries[i] = entry
	}
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

// SessionCostUnknown reports whether any invocation in the current session had
// usage without authoritative pricing.
func (cl *CostLedger) SessionCostUnknown() bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	for _, e := range cl.entries {
		if e.CostUnknown {
			return true
		}
	}
	return false
}

// TodayCostUnknown reports whether today's known subtotal has unknown-priced
// invocations.
func (cl *CostLedger) TodayCostUnknown() bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, e := range cl.entries {
		if e.CostUnknown && e.Timestamp.UTC().Truncate(24*time.Hour).Equal(today) {
			return true
		}
	}
	return false
}

// Summary returns a human-readable cost summary.
type CostSummary struct {
	SessionCost        float64    `json:"session_cost"`
	SessionCostUnknown bool       `json:"session_cost_unknown,omitempty"`
	TodayCost          float64    `json:"today_cost"`
	TodayCostUnknown   bool       `json:"today_cost_unknown,omitempty"`
	SessionTokens      TokenUsage `json:"session_tokens"`
	InvocationCount    int        `json:"invocation_count"`
}

// Summary returns aggregated cost data.
func (cl *CostLedger) Summary() CostSummary {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	today := time.Now().UTC().Truncate(24 * time.Hour)
	var summary CostSummary
	summary.InvocationCount = len(cl.entries)
	for _, e := range cl.entries {
		summary.SessionCost += e.Cost
		summary.SessionCostUnknown = summary.SessionCostUnknown || e.CostUnknown
		summary.SessionTokens = AddTokenUsage(summary.SessionTokens, e.Tokens)
		if e.Timestamp.UTC().Truncate(24 * time.Hour).Equal(today) {
			summary.TodayCost += e.Cost
			summary.TodayCostUnknown = summary.TodayCostUnknown || e.CostUnknown
		}
	}
	return summary
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
