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

// RAGConfig configures scratchpad RAG behavior.
type RAGConfig struct {
	MaxEntries          int           // Max entries to search (default 100)
	MaxCacheSize        int           // Max cached embeddings (default 500)
	EmbeddingTTL        time.Duration // How long to cache embeddings (default 1h)
	MinSimilarity       float64       // Minimum similarity to include in results (default 0.1)
	CleanupInterval     time.Duration // How often to clean expired embeddings (default 5m)
}

// DefaultRAGConfig returns sensible defaults.
func DefaultRAGConfig() RAGConfig {
	return RAGConfig{
		MaxEntries:      100,
		MaxCacheSize:    500,
		EmbeddingTTL:    time.Hour,
		MinSimilarity:   0.1,
		CleanupInterval: 5 * time.Minute,
	}
}

type cachedEmbedding struct {
	embedding []float64
	createdAt time.Time
}

// ScratchpadRAG provides semantic search over scratchpad entries.
type ScratchpadRAG struct {
	scratchpad *Scratchpad
	embedder   EmbeddingProvider
	config     RAGConfig

	mu         sync.RWMutex
	embeddings map[string]*cachedEmbedding
	lastUpdate time.Time

	cleanupOnce sync.Once
	stopCleanup chan struct{}
}

// RAGSearchResult represents a semantically matched scratchpad entry.
type RAGSearchResult struct {
	Entry      EntrySummary `json:"entry"`
	Similarity float64      `json:"similarity"`
	Rank       int          `json:"rank"`
}

// NewScratchpadRAG creates a RAG-enabled scratchpad searcher.
func NewScratchpadRAG(scratchpad *Scratchpad, embedder EmbeddingProvider) *ScratchpadRAG {
	return NewScratchpadRAGWithConfig(scratchpad, embedder, DefaultRAGConfig())
}

// NewScratchpadRAGWithConfig creates a RAG searcher with custom configuration.
func NewScratchpadRAGWithConfig(scratchpad *Scratchpad, embedder EmbeddingProvider, config RAGConfig) *ScratchpadRAG {
	if config.MaxEntries <= 0 {
		config.MaxEntries = DefaultRAGConfig().MaxEntries
	}
	if config.MaxCacheSize <= 0 {
		config.MaxCacheSize = DefaultRAGConfig().MaxCacheSize
	}
	if config.EmbeddingTTL <= 0 {
		config.EmbeddingTTL = DefaultRAGConfig().EmbeddingTTL
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = DefaultRAGConfig().CleanupInterval
	}

	r := &ScratchpadRAG{
		scratchpad:  scratchpad,
		embedder:    embedder,
		config:      config,
		embeddings:  make(map[string]*cachedEmbedding),
		stopCleanup: make(chan struct{}),
	}

	// Start background cleanup goroutine
	r.cleanupOnce.Do(func() {
		go r.cleanupLoop()
	})

	return r
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

	// Get scratchpad entries (configurable limit)
	entries, err := r.scratchpad.ListSummaries(ctx, r.config.MaxEntries)
	if err != nil {
		return nil, err
	}

	// Sync cache with current entries (remove stale)
	r.syncCache(entries)

	// Update embeddings for entries we haven't seen
	if err := r.updateEmbeddings(ctx, entries); err != nil {
		return nil, err
	}

	// Calculate similarities
	var results []RAGSearchResult
	r.mu.RLock()
	for _, entry := range entries {
		cached, ok := r.embeddings[entry.Key]
		if !ok {
			continue
		}
		sim := cosineSim(queryEmbed, cached.embedding)
		// Filter by minimum similarity threshold
		if sim < r.config.MinSimilarity {
			continue
		}
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

// syncCache removes embeddings for entries that no longer exist in scratchpad.
func (r *ScratchpadRAG) syncCache(currentEntries []EntrySummary) {
	currentKeys := make(map[string]struct{}, len(currentEntries))
	for _, entry := range currentEntries {
		currentKeys[entry.Key] = struct{}{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for key := range r.embeddings {
		if _, exists := currentKeys[key]; !exists {
			delete(r.embeddings, key)
		}
	}
}

// updateEmbeddings generates embeddings for entries that don't have them.
func (r *ScratchpadRAG) updateEmbeddings(ctx context.Context, entries []EntrySummary) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	for _, entry := range entries {
		cached, ok := r.embeddings[entry.Key]
		// Skip if we have a non-expired embedding
		if ok && now.Sub(cached.createdAt) < r.config.EmbeddingTTL {
			continue
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

		r.embeddings[entry.Key] = &cachedEmbedding{
			embedding: embed,
			createdAt: now,
		}
	}

	// Enforce max cache size (evict oldest)
	r.enforceCacheSizeLocked()

	r.lastUpdate = now
	return nil
}

// enforceCacheSizeLocked removes oldest embeddings if cache exceeds max size.
// Must be called with mu held.
func (r *ScratchpadRAG) enforceCacheSizeLocked() {
	if len(r.embeddings) <= r.config.MaxCacheSize {
		return
	}

	// Build list sorted by creation time (oldest first)
	type keyTime struct {
		key       string
		createdAt time.Time
	}
	items := make([]keyTime, 0, len(r.embeddings))
	for key, cached := range r.embeddings {
		items = append(items, keyTime{key: key, createdAt: cached.createdAt})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].createdAt.Before(items[j].createdAt)
	})

	// Remove oldest until within limit
	toRemove := len(r.embeddings) - r.config.MaxCacheSize
	for i := 0; i < toRemove && i < len(items); i++ {
		delete(r.embeddings, items[i].key)
	}
}

// cleanupLoop periodically removes expired embeddings.
func (r *ScratchpadRAG) cleanupLoop() {
	ticker := time.NewTicker(r.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.cleanupExpired()
		case <-r.stopCleanup:
			return
		}
	}
}

// cleanupExpired removes embeddings that have exceeded their TTL.
func (r *ScratchpadRAG) cleanupExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for key, cached := range r.embeddings {
		if now.Sub(cached.createdAt) > r.config.EmbeddingTTL {
			delete(r.embeddings, key)
		}
	}
}

// ClearCache removes all cached embeddings.
func (r *ScratchpadRAG) ClearCache() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.embeddings = make(map[string]*cachedEmbedding)
	r.mu.Unlock()
}

// Close stops the background cleanup goroutine.
func (r *ScratchpadRAG) Close() {
	if r == nil {
		return
	}
	select {
	case <-r.stopCleanup:
		// Already closed
	default:
		close(r.stopCleanup)
	}
}

// CacheStats returns statistics about the embedding cache.
func (r *ScratchpadRAG) CacheStats() (size int, oldestAge time.Duration) {
	if r == nil {
		return 0, 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	size = len(r.embeddings)
	if size == 0 {
		return size, 0
	}

	var oldest time.Time
	for _, cached := range r.embeddings {
		if oldest.IsZero() || cached.createdAt.Before(oldest) {
			oldest = cached.createdAt
		}
	}
	return size, time.Since(oldest)
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
