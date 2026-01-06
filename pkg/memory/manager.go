package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/storage"
)

// Record represents an episodic memory item.
type Record struct {
	ID         int64
	SessionID  string
	Kind       string
	Content    string
	Metadata   map[string]any
	CreatedAt  time.Time
	Similarity float64
}

// Manager stores and retrieves episodic memories using embeddings.
type Manager struct {
	store    *storage.Store
	provider embeddings.EmbeddingProvider
}

// NewManager returns nil when dependencies are unavailable.
func NewManager(store *storage.Store, provider embeddings.EmbeddingProvider) *Manager {
	if store == nil || store.DB() == nil || provider == nil {
		return nil
	}
	return &Manager{store: store, provider: provider}
}

// Record stores a memory item for a session.
func (m *Manager) Record(ctx context.Context, sessionID, kind, content string, metadata map[string]any) error {
	if m == nil || m.store == nil || m.provider == nil {
		return nil
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(content) == "" {
		return nil
	}
	embedding, err := m.provider.Embed(ctx, content)
	if err != nil {
		return err
	}
	embeddingBytes, err := serializeEmbedding(embedding)
	if err != nil {
		return err
	}
	metaJSON := ""
	if metadata != nil {
		if raw, err := json.Marshal(metadata); err == nil {
			metaJSON = string(raw)
		}
	}
	_, err = m.store.DB().ExecContext(ctx, `
		INSERT INTO memories (session_id, kind, content, embedding, metadata)
		VALUES (?, ?, ?, ?, ?)
	`, sessionID, kind, content, embeddingBytes, metaJSON)
	return err
}

// RetrieveRelevant returns the most relevant memory items for a query.
// If maxTokens > 0, results are capped to roughly that token budget.
func (m *Manager) RetrieveRelevant(ctx context.Context, sessionID, query string, limit int, maxTokens int) ([]Record, error) {
	if m == nil || m.store == nil || m.provider == nil {
		return nil, nil
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	queryEmbedding, err := m.provider.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT id, session_id, kind, content, embedding, metadata, created_at
		FROM memories
		WHERE session_id = ?
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var (
			id         int64
			sid        string
			kind       string
			content    string
			embedBytes []byte
			metaRaw    sql.NullString
			createdAt  time.Time
		)
		if err := rows.Scan(&id, &sid, &kind, &content, &embedBytes, &metaRaw, &createdAt); err != nil {
			continue
		}

		embedding, err := deserializeEmbedding(embedBytes)
		if err != nil {
			continue
		}
		similarity, _ := embeddings.CosineSimilarity(queryEmbedding, embedding)
		meta := map[string]any{}
		if metaRaw.Valid && strings.TrimSpace(metaRaw.String) != "" {
			_ = json.Unmarshal([]byte(metaRaw.String), &meta)
		}

		records = append(records, Record{
			ID:         id,
			SessionID:  sid,
			Kind:       kind,
			Content:    content,
			Metadata:   meta,
			CreatedAt:  createdAt,
			Similarity: similarity,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, nil
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].Similarity > records[j].Similarity
	})

	selected := make([]Record, 0, limit)
	tokenBudget := maxTokens
	for _, rec := range records {
		// Skip very low-similarity noise.
		if rec.Similarity < 0.6 {
			continue
		}
		if tokenBudget > 0 {
			toks := conversation.CountTokens(rec.Content)
			if toks > tokenBudget && len(selected) > 0 {
				break
			}
			if toks > tokenBudget {
				// Allow one big memory if it's the only one.
				selected = append(selected, rec)
				break
			}
			tokenBudget -= toks
		}
		selected = append(selected, rec)
		if len(selected) >= limit {
			break
		}
	}

	return selected, nil
}

// serializeEmbedding converts a float64 slice to bytes.
func serializeEmbedding(embedding []float64) ([]byte, error) {
	embeddingLen := len(embedding)
	if embeddingLen < 0 {
		return nil, fmt.Errorf("invalid embedding length")
	}
	if embeddingLen > math.MaxInt32 {
		return nil, fmt.Errorf("embedding too large: %d", embeddingLen)
	}
	if embeddingLen > (math.MaxInt-4)/8 {
		return nil, fmt.Errorf("embedding too large: %d", embeddingLen)
	}

	buf := make([]byte, 4+embeddingLen*8)
	binary.BigEndian.PutUint32(buf[:4], uint32(embeddingLen))
	for i, v := range embedding {
		binary.BigEndian.PutUint64(buf[4+i*8:4+(i+1)*8], math.Float64bits(v))
	}
	return buf, nil
}

// deserializeEmbedding converts bytes back to float64 slice.
func deserializeEmbedding(data []byte) ([]float64, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid embedding data")
	}
	length := binary.BigEndian.Uint32(data[:4])
	expected := 4 + int(length)*8
	if len(data) != expected {
		return nil, fmt.Errorf("invalid embedding length")
	}
	embedding := make([]float64, length)
	for i := 0; i < int(length); i++ {
		offset := 4 + i*8
		embedding[i] = math.Float64frombits(binary.BigEndian.Uint64(data[offset : offset+8]))
	}
	return embedding, nil
}
