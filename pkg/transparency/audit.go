// Package transparency provides full visibility into LLM invocations.
//
// This is the core of Buckley's radical transparency philosophy:
// - Every token is counted and attributed to its source
// - Every cost is tracked and displayed
// - Every model response is traceable
// - Nothing is hidden from the user
package transparency

import (
	"sort"
	"sync"
)

// ContextSource represents a single source of context tokens.
type ContextSource struct {
	// Name identifies the source (e.g., "git diff", "AGENTS.md")
	Name string `json:"name"`

	// Tokens is the number of tokens from this source
	Tokens int `json:"tokens"`

	// Bytes is the raw byte count before tokenization
	Bytes int `json:"bytes"`

	// Truncated indicates if the source was truncated to fit budget
	Truncated bool `json:"truncated,omitempty"`

	// OriginalTokens is the token count before truncation (if truncated)
	OriginalTokens int `json:"original_tokens,omitempty"`
}

// Percentage returns this source's percentage of total tokens.
func (cs ContextSource) Percentage(total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(cs.Tokens) / float64(total) * 100
}

// ContextAudit tracks all context sources and their token contributions.
// This allows users to see exactly what consumed their token budget.
type ContextAudit struct {
	mu      sync.Mutex
	sources []ContextSource
	total   int
}

// NewContextAudit creates an empty context audit.
func NewContextAudit() *ContextAudit {
	return &ContextAudit{
		sources: make([]ContextSource, 0),
	}
}

// Add records a context source and its token count.
func (ca *ContextAudit) Add(name string, tokens int) {
	ca.AddWithBytes(name, tokens, 0)
}

// AddWithBytes records a context source with both token and byte counts.
func (ca *ContextAudit) AddWithBytes(name string, tokens, bytes int) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	ca.sources = append(ca.sources, ContextSource{
		Name:   name,
		Tokens: tokens,
		Bytes:  bytes,
	})
	ca.total += tokens
}

// AddTruncated records a context source that was truncated.
func (ca *ContextAudit) AddTruncated(name string, tokens, originalTokens int) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	ca.sources = append(ca.sources, ContextSource{
		Name:           name,
		Tokens:         tokens,
		Truncated:      true,
		OriginalTokens: originalTokens,
	})
	ca.total += tokens
}

// Sources returns all context sources, sorted by token count descending.
func (ca *ContextAudit) Sources() []ContextSource {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	// Return a sorted copy
	sources := make([]ContextSource, len(ca.sources))
	copy(sources, ca.sources)

	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Tokens > sources[j].Tokens
	})

	return sources
}

// TotalTokens returns the total token count across all sources.
func (ca *ContextAudit) TotalTokens() int {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.total
}

// HasTruncation returns true if any source was truncated.
func (ca *ContextAudit) HasTruncation() bool {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	for _, s := range ca.sources {
		if s.Truncated {
			return true
		}
	}
	return false
}

// Merge combines another audit into this one.
func (ca *ContextAudit) Merge(other *ContextAudit) {
	if other == nil {
		return
	}

	ca.mu.Lock()
	defer ca.mu.Unlock()

	other.mu.Lock()
	defer other.mu.Unlock()

	ca.sources = append(ca.sources, other.sources...)
	ca.total += other.total
}
