package rlm

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// EmbeddingProvider generates embeddings for text.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

// ScratchpadRAG provides semantic search over scratchpad entries.
type ScratchpadRAG struct {
	scratchpad *Scratchpad
	embedder   EmbeddingProvider

	mu         sync.RWMutex
	embeddings map[string][]float64 // key -> embedding
	lastUpdate time.Time
}

// RAGSearchResult represents a semantically matched scratchpad entry.
type RAGSearchResult struct {
	Entry      EntrySummary `json:"entry"`
	Similarity float64      `json:"similarity"`
	Rank       int          `json:"rank"`
}

// NewScratchpadRAG creates a RAG-enabled scratchpad searcher.
func NewScratchpadRAG(scratchpad *Scratchpad, embedder EmbeddingProvider) *ScratchpadRAG {
	return &ScratchpadRAG{
		scratchpad: scratchpad,
		embedder:   embedder,
		embeddings: make(map[string][]float64),
	}
}

// Search finds semantically similar scratchpad entries.
func (r *ScratchpadRAG) Search(ctx context.Context, query string, limit int) ([]RAGSearchResult, error) {
	if r == nil || r.scratchpad == nil || r.embedder == nil {
		return nil, nil
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	// Get query embedding
	queryEmbed, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	// Get all scratchpad entries
	entries, err := r.scratchpad.ListSummaries(ctx, 100)
	if err != nil {
		return nil, err
	}

	// Update embeddings for entries we haven't seen
	if err := r.updateEmbeddings(ctx, entries); err != nil {
		return nil, err
	}

	// Calculate similarities
	var results []RAGSearchResult
	r.mu.RLock()
	for _, entry := range entries {
		embed, ok := r.embeddings[entry.Key]
		if !ok {
			continue
		}
		sim := cosineSim(queryEmbed, embed)
		results = append(results, RAGSearchResult{
			Entry:      entry,
			Similarity: sim,
		})
	}
	r.mu.RUnlock()

	// Sort by similarity (highest first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	// Apply limit and add ranks
	if len(results) > limit {
		results = results[:limit]
	}
	for i := range results {
		results[i].Rank = i + 1
	}

	return results, nil
}

// SearchByType finds entries matching both semantic query and type filter.
func (r *ScratchpadRAG) SearchByType(ctx context.Context, query string, entryType EntryType, limit int) ([]RAGSearchResult, error) {
	results, err := r.Search(ctx, query, limit*2) // Get more results to filter
	if err != nil {
		return nil, err
	}

	// Filter by type
	var filtered []RAGSearchResult
	for _, result := range results {
		if result.Entry.Type == entryType {
			filtered = append(filtered, result)
		}
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	// Re-rank after filtering
	for i := range filtered {
		filtered[i].Rank = i + 1
	}

	return filtered, nil
}

// updateEmbeddings generates embeddings for entries that don't have them.
func (r *ScratchpadRAG) updateEmbeddings(ctx context.Context, entries []EntrySummary) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, entry := range entries {
		if _, ok := r.embeddings[entry.Key]; ok {
			continue // Already have embedding
		}

		// Build text to embed: combine type, summary, and metadata
		var textParts []string
		textParts = append(textParts, string(entry.Type))
		textParts = append(textParts, entry.Summary)
		if entry.CreatedBy != "" {
			textParts = append(textParts, "created by: "+entry.CreatedBy)
		}

		text := strings.Join(textParts, " | ")
		embed, err := r.embedder.Embed(ctx, text)
		if err != nil {
			continue // Skip on error, don't fail whole operation
		}

		r.embeddings[entry.Key] = embed
	}

	r.lastUpdate = time.Now()
	return nil
}

// ClearCache removes all cached embeddings.
func (r *ScratchpadRAG) ClearCache() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.embeddings = make(map[string][]float64)
	r.mu.Unlock()
}

// cosineSim computes cosine similarity between two vectors.
func cosineSim(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
