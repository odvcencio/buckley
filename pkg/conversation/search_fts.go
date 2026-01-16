package conversation

import (
	"context"
	"strings"
)

// SearchFullText performs full-text search using FTS5.
func (cs *ConversationSearcher) SearchFullText(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if cs == nil || cs.store == nil {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	rows, err := cs.store.SearchMessagesFTS(ctx, query, opts.SessionID, opts.Limit)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		if opts.MinScore > 0 && row.Score < opts.MinScore {
			continue
		}
		content := row.Message.Content
		snippet := row.Snippet
		if snippet == "" {
			snippet = extractSnippet(content, query)
		}
		results = append(results, SearchResult{
			MessageID: row.Message.ID,
			SessionID: row.Message.SessionID,
			Role:      row.Message.Role,
			Content:   content,
			Snippet:   snippet,
			Score:     row.Score,
			Timestamp: row.Message.Timestamp,
		})
	}

	return results, nil
}
