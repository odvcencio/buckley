package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
)

type extractorStubEmbedder struct{}

func (extractorStubEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	text = strings.ToLower(text)
	if strings.Contains(text, "waffle") {
		return []float64{1, 0, 0}, nil
	}
	return []float64{0, 1, 0}, nil
}

type extractorStubModel struct{
	response string
}

func (m extractorStubModel) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	return &model.ChatResponse{
		Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: m.response}}},
	}, nil
}

func (m extractorStubModel) GetExecutionModel() string {
	return "stub-model"
}

func TestMemoryExtractor_ExtractFromMessage(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	sessionID := "sess-1"
	if err := store.CreateSession(&storage.Session{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	mgr := NewManager(store, extractorStubEmbedder{})
	if mgr == nil {
		t.Fatalf("expected manager")
	}

	modelClient := extractorStubModel{response: `[{"kind":"preference","content":"prefers waffles","metadata":{"source":"user"}}]`}
	extractor := NewMemoryExtractor(mgr, modelClient, []ExtractionPattern{{Name: "prefs", Description: "prefs", Prompt: ""}})

	msg := &storage.Message{SessionID: sessionID, Role: "user", Content: "I like waffles."}
	if err := extractor.ExtractFromMessage(context.Background(), msg, sessionID, "/tmp/project"); err != nil {
		t.Fatalf("extract: %v", err)
	}

	row := store.DB().QueryRow(`SELECT content FROM memories WHERE session_id = ?`, sessionID)
	var content string
	if err := row.Scan(&content); err != nil {
		t.Fatalf("scan memory: %v", err)
	}
	if !strings.Contains(content, "waffles") {
		t.Fatalf("expected memory content, got %q", content)
	}
}
