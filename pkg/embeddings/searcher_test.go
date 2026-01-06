package embeddings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

type fakeProvider struct{}

func (fakeProvider) Embed(ctx context.Context, text string) ([]float64, error) {
	text = strings.ToLower(text)
	sum := 0.0
	for _, ch := range text {
		if ch >= 'a' && ch <= 'z' {
			sum += float64(ch-'a'+1) / 10.0
		}
	}
	return []float64{sum, float64(len(text))}, nil
}

func newTestSearcher(t *testing.T) (*Searcher, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	searcher := NewSearcher(fakeProvider{}, store.DB())
	cleanup := func() {
		_ = store.Close()
	}
	return searcher, cleanup
}

func TestSearcherIndexDirectorySkipsUnchanged(t *testing.T) {
	searcher, cleanup := newTestSearcher(t)
	defer cleanup()

	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ctx := context.Background()
	report, err := searcher.IndexDirectory(ctx, root)
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	if report.Embedded != 1 {
		t.Fatalf("expected 1 embedded file, got %d", report.Embedded)
	}

	// Second run without changes should skip everything
	report, err = searcher.IndexDirectory(ctx, root)
	if err != nil {
		t.Fatalf("IndexDirectory second run: %v", err)
	}
	if report.Embedded != 0 {
		t.Fatalf("expected 0 embedded files on second run, got %d", report.Embedded)
	}

}

func TestSearcherHasNewerFiles(t *testing.T) {
	searcher, cleanup := newTestSearcher(t)
	defer cleanup()

	root := t.TempDir()
	file := filepath.Join(root, "app.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ctx := context.Background()
	if _, err := searcher.IndexDirectory(ctx, root); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}

	lastIndexed, err := searcher.LatestSourceModTime(ctx)
	if err != nil {
		t.Fatalf("LatestSourceModTime: %v", err)
	}
	if lastIndexed.IsZero() {
		t.Fatalf("expected non-zero last indexed time")
	}

	// Touch file without content change; HasNewerFiles should detect newer mod time
	newMod := lastIndexed.Add(2 * time.Second)
	if err := os.Chtimes(file, newMod, newMod); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	hasNewer, err := searcher.HasNewerFiles(root, lastIndexed)
	if err != nil {
		t.Fatalf("HasNewerFiles: %v", err)
	}
	if !hasNewer {
		t.Fatalf("expected HasNewerFiles to return true after touching file")
	}
}

func TestSearcherSearchIncludesMetadata(t *testing.T) {
	searcher, cleanup := newTestSearcher(t)
	defer cleanup()

	root := t.TempDir()
	file := filepath.Join(root, "server.go")
	if err := os.WriteFile(file, []byte("package server\n\n// Foo handler\nfunc FooHandler() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ctx := context.Background()
	if _, err := searcher.IndexDirectory(ctx, root); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}

	results, err := searcher.Search(ctx, "Foo handler", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least one search result")
	}

	if got := results[0].Metadata["file"]; got != "server.go" {
		t.Fatalf("expected metadata file server.go, got %q", got)
	}
}
