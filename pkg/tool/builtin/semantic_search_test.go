package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/storage"
)

type stubEmbeddingProvider struct{}

func (stubEmbeddingProvider) Embed(ctx context.Context, text string) ([]float64, error) {
	sum := 0.0
	for _, ch := range []byte(text) {
		sum += float64(ch%5) + 1
	}
	return []float64{sum, float64(len(text))}, nil
}

func newTestSearcher(t *testing.T) (*embeddings.Searcher, string, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	searcher := embeddings.NewSearcher(stubEmbeddingProvider{}, store.DB())
	cleanup := func() {
		_ = store.Close()
	}
	return searcher, tempDir, cleanup
}

func TestSemanticSearchToolExecuteReturnsResults(t *testing.T) {
	searcher, root, cleanup := newTestSearcher(t)
	defer cleanup()

	sourceFile := filepath.Join(root, "handlers.go")
	if err := os.WriteFile(sourceFile, []byte("// handler\nfunc FooHandler() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := searcher.IndexDirectory(context.Background(), root); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}

	tool := NewSemanticSearchTool(searcher)
	result, err := tool.Execute(map[string]any{"query": "foo handler"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	data := result.Data
	if count, _ := data["count"].(int); count == 0 {
		t.Fatalf("expected at least one search result")
	}
}

func TestIndexManagementToolBuildAndStatus(t *testing.T) {
	searcher, root, cleanup := newTestSearcher(t)
	defer cleanup()

	sourceFile := filepath.Join(root, "service.go")
	if err := os.WriteFile(sourceFile, []byte("package main\nfunc Service() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewIndexManagementTool(searcher)
	buildResult, err := tool.Execute(map[string]any{
		"action": "build",
		"path":   root,
	})
	if err != nil {
		t.Fatalf("build action error: %v", err)
	}
	if !buildResult.Success {
		t.Fatalf("build action failed: %s", buildResult.Error)
	}

	statusResult, err := tool.Execute(map[string]any{
		"action": "status",
	})
	if err != nil {
		t.Fatalf("status action error: %v", err)
	}
	data := statusResult.Data
	ready, _ := data["ready_to_search"].(bool)
	if !ready {
		t.Fatalf("expected ready_to_search=true, data=%v", data)
	}
}
