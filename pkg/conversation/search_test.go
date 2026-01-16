package conversation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/storage"
)

type searchStubEmbedder struct{}

func (searchStubEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	text = strings.ToLower(text)
	switch {
	case strings.Contains(text, "alpha"):
		return []float64{1, 0, 0}, nil
	case strings.Contains(text, "beta"):
		return []float64{0, 1, 0}, nil
	default:
		return []float64{0, 0, 1}, nil
	}
}

var _ embeddings.EmbeddingProvider = searchStubEmbedder{}

func TestConversationSearcher_Search(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "conv.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	sessionID := "sess-1"
	if err := store.CreateSession(&storage.Session{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	msg1 := &storage.Message{SessionID: sessionID, Role: "user", Content: "alpha topic", Timestamp: time.Now()}
	if err := store.SaveMessage(msg1); err != nil {
		t.Fatalf("save message: %v", err)
	}
	msg2 := &storage.Message{SessionID: sessionID, Role: "assistant", Content: "beta response", Timestamp: time.Now().Add(time.Second)}
	if err := store.SaveMessage(msg2); err != nil {
		t.Fatalf("save message: %v", err)
	}

	searcher := NewConversationSearcher(store, searchStubEmbedder{})
	ctx := context.Background()
	if err := searcher.IndexMessage(ctx, sessionID, msg1); err != nil {
		t.Fatalf("index message: %v", err)
	}
	if err := searcher.IndexMessage(ctx, sessionID, msg2); err != nil {
		t.Fatalf("index message: %v", err)
	}

	results, err := searcher.Search(ctx, "alpha", SearchOptions{SessionID: sessionID, Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if !strings.Contains(results[0].Content, "alpha") {
		t.Fatalf("expected alpha result, got %q", results[0].Content)
	}
}

func TestConversationSearcher_SearchFullText(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "conv.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	sessionID := "sess-1"
	if err := store.CreateSession(&storage.Session{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	msg := &storage.Message{SessionID: sessionID, Role: "user", Content: "full text search works", Timestamp: time.Now()}
	if err := store.SaveMessage(msg); err != nil {
		t.Fatalf("save message: %v", err)
	}

	searcher := NewConversationSearcher(store, searchStubEmbedder{})
	results, err := searcher.SearchFullText(context.Background(), "search", SearchOptions{SessionID: sessionID, Limit: 5})
	if err != nil {
		t.Fatalf("search full text: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if !strings.Contains(results[0].Content, "search") {
		t.Fatalf("expected search match, got %q", results[0].Content)
	}
}
