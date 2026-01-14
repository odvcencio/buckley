package conversation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/memory"
	"github.com/odvcencio/buckley/pkg/storage"
)

// SearchResult represents a semantic search result.
type SearchResult struct {
	MessageID int64
	SessionID string
	Role      string
	Content   string
	Snippet   string
	Score     float64
	Timestamp time.Time
}

// SearchOptions configures search scope.
type SearchOptions struct {
	SessionID string
	Limit     int
	MinScore  float64
}

// ConversationSearcher provides semantic search over stored messages.
type ConversationSearcher struct {
	store    *storage.Store
	embedder embeddings.EmbeddingProvider
}

// NewConversationSearcher creates a new conversation searcher.
func NewConversationSearcher(store *storage.Store, embedder embeddings.EmbeddingProvider) *ConversationSearcher {
	return &ConversationSearcher{store: store, embedder: embedder}
}

// IndexMessage stores an embedding for a message.
func (cs *ConversationSearcher) IndexMessage(ctx context.Context, sessionID string, msg *storage.Message) error {
	if cs == nil || cs.store == nil || cs.embedder == nil || msg == nil {
		return nil
	}
	if msg.ID <= 0 {
		return fmt.Errorf("message id required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(msg.SessionID)
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" && strings.TrimSpace(msg.Reasoning) != "" {
		content = strings.TrimSpace(msg.Reasoning)
	}
	if content == "" {
		return nil
	}

	embedding, err := cs.embedder.Embed(ctx, content)
	if err != nil {
		return err
	}
	bytes, err := memory.SerializeEmbedding(embedding)
	if err != nil {
		return err
	}
	return cs.store.SaveMessageEmbedding(ctx, msg.ID, bytes)
}

// Search performs semantic search over embedded messages.
func (cs *ConversationSearcher) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if cs == nil || cs.store == nil || cs.embedder == nil {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.MinScore <= 0 {
		opts.MinScore = 0.6
	}

	if err := cs.ensureEmbeddings(ctx, opts.SessionID); err != nil {
		return nil, err
	}

	queryEmbedding, err := cs.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	messages, err := cs.store.GetMessagesWithEmbeddings(ctx, opts.SessionID)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(messages))
	for _, msg := range messages {
		if len(msg.Embedding) == 0 {
			continue
		}
		embedding, err := memory.DeserializeEmbedding(msg.Embedding)
		if err != nil {
			continue
		}
		score, err := embeddings.CosineSimilarity(queryEmbedding, embedding)
		if err != nil {
			continue
		}
		if score < opts.MinScore {
			continue
		}
		snippet := extractSnippet(msg.Content, query)
		results = append(results, SearchResult{
			MessageID: msg.ID,
			SessionID: msg.SessionID,
			Role:      msg.Role,
			Content:   msg.Content,
			Snippet:   snippet,
			Score:     score,
			Timestamp: msg.Timestamp,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	return results, nil
}

func (cs *ConversationSearcher) ensureEmbeddings(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	missing, err := cs.store.GetMessagesMissingEmbeddings(ctx, sessionID, 500)
	if err != nil {
		return err
	}
	for i := range missing {
		msg := missing[i]
		if err := cs.IndexMessage(ctx, sessionID, &msg); err != nil {
			return err
		}
	}
	return nil
}

func extractSnippet(content, query string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	normalized := strings.Join(strings.Fields(content), " ")
	lower := strings.ToLower(normalized)
	query = strings.ToLower(strings.TrimSpace(query))

	start := 0
	if query != "" {
		terms := strings.Fields(query)
		for _, term := range terms {
			idx := strings.Index(lower, term)
			if idx >= 0 {
				start = idx
				break
			}
		}
	}

	if start > 40 {
		start -= 40
	} else {
		start = 0
	}
	end := start + 200
	if end > len(normalized) {
		end = len(normalized)
	}
	snippet := normalized[start:end]
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(normalized) {
		snippet += "..."
	}
	return snippet
}
