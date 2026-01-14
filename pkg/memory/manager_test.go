package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/storage"
)

type stubProvider struct{}

func (stubProvider) Embed(ctx context.Context, text string) ([]float64, error) {
	// Produce stable, low-dimensional embeddings based on keyword presence.
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

var _ embeddings.EmbeddingProvider = stubProvider{}

func TestManager_RecordAndRetrieveRelevant(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	mgr := NewManager(store, stubProvider{})
	if mgr == nil {
		t.Fatal("expected manager")
	}

	ctx := context.Background()
	sessionID := "sess-1"

	if err := store.CreateSession(&storage.Session{
		ID:         sessionID,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := mgr.Record(ctx, sessionID, "summary", "alpha decision about retries", map[string]any{"n": 1}); err != nil {
		t.Fatalf("record alpha: %v", err)
	}
	if err := mgr.Record(ctx, sessionID, "summary", "beta decision about tools", map[string]any{"n": 2}); err != nil {
		t.Fatalf("record beta: %v", err)
	}

	results, err := mgr.RetrieveRelevant(ctx, "alpha please", RecallOptions{
		Scope:     RecallScopeSession,
		SessionID: sessionID,
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if !strings.Contains(results[0].Content, "alpha") {
		t.Fatalf("expected alpha first, got %q", results[0].Content)
	}
}

func TestManager_RetrieveRelevant_ProjectScope(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	mgr := NewManager(store, stubProvider{})
	if mgr == nil {
		t.Fatal("expected manager")
	}

	ctx := context.Background()
	sessionID := "sess-1"
	projectPath := "/tmp/project"

	if err := store.CreateSession(&storage.Session{
		ID:         sessionID,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := mgr.RecordWithScope(ctx, sessionID, "summary", "alpha project decision", nil, projectPath); err != nil {
		t.Fatalf("record project memory: %v", err)
	}

	results, err := mgr.RetrieveRelevant(ctx, "alpha", RecallOptions{
		Scope:       RecallScopeProject,
		ProjectPath: projectPath,
		Limit:       2,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].ProjectPath != projectPath {
		t.Fatalf("expected project path, got %q", results[0].ProjectPath)
	}
}
