package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/storage"
)

func newTestService(t *testing.T) (*Service, *storage.Store, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	service := NewService(store)
	cleanup := func() {
		_ = store.Close()
	}
	return service, store, cleanup
}

func TestNewService(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	service := NewService(store)
	if service == nil {
		t.Fatal("expected non-nil service")
	}
	if service.store != store {
		t.Error("service store not set correctly")
	}
}

func TestNewService_NilStore(t *testing.T) {
	service := NewService(nil)
	if service != nil {
		t.Error("expected nil service when store is nil")
	}
}

func TestService_LookupFiles_Success(t *testing.T) {
	service, store, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create some file records
	files := []storage.FileRecord{
		{
			Path:      "main.go",
			Checksum:  "abc123",
			Language:  "go",
			SizeBytes: 100,
		},
		{
			Path:      "pkg/model/user.go",
			Checksum:  "def456",
			Language:  "go",
			SizeBytes: 200,
		},
		{
			Path:      "pkg/model/post.go",
			Checksum:  "ghi789",
			Language:  "go",
			SizeBytes: 150,
		},
	}

	for _, f := range files {
		rec := f // Copy to avoid pointer issues
		if err := store.UpsertFileRecord(ctx, &rec); err != nil {
			t.Fatalf("failed to insert file record: %v", err)
		}
	}

	// Test lookup all files
	results, err := service.LookupFiles(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("LookupFiles failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 files, got %d", len(results))
	}

	// Test lookup with path filter (using SQL LIKE pattern)
	results, err = service.LookupFiles(ctx, "", "pkg/model/*", 10)
	if err != nil {
		t.Fatalf("LookupFiles with path filter failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 files matching pkg/model/, got %d", len(results))
	}

	// Test lookup with limit
	results, err = service.LookupFiles(ctx, "", "", 1)
	if err != nil {
		t.Fatalf("LookupFiles with limit failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 file with limit, got %d", len(results))
	}
}

func TestService_LookupFiles_NoStore(t *testing.T) {
	// Create service with store, then set store to nil
	service, _, cleanup := newTestService(t)
	defer cleanup()

	service.store = nil

	ctx := context.Background()
	_, err := service.LookupFiles(ctx, "", "", 10)
	if err == nil {
		t.Fatal("expected error when store is nil")
	}
	if err.Error() != "store not initialized" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestService_LookupFiles_Empty(t *testing.T) {
	service, _, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()
	results, err := service.LookupFiles(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("LookupFiles failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 files, got %d", len(results))
	}
}

func TestService_LookupSymbols_Success(t *testing.T) {
	service, store, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()

	// First, create a file record
	fileRec := &storage.FileRecord{
		Path:      "user.go",
		Checksum:  "abc123",
		Language:  "go",
		SizeBytes: 100,
	}
	if err := store.UpsertFileRecord(ctx, fileRec); err != nil {
		t.Fatalf("failed to insert file record: %v", err)
	}

	// Create some symbol records
	symbols := []storage.SymbolRecord{
		{
			FilePath:  "user.go",
			Name:      "User",
			Kind:      "type",
			Signature: "",
			StartLine: 5,
			EndLine:   10,
		},
		{
			FilePath:  "user.go",
			Name:      "NewUser",
			Kind:      "function",
			Signature: "func NewUser(name string) *User",
			StartLine: 12,
			EndLine:   14,
		},
		{
			FilePath:  "user.go",
			Name:      "Save",
			Kind:      "method",
			Signature: "func (u *User) Save() error",
			StartLine: 16,
			EndLine:   20,
		},
	}

	if err := store.ReplaceSymbols(ctx, "user.go", symbols); err != nil {
		t.Fatalf("failed to insert symbols: %v", err)
	}

	// Test lookup all symbols
	results, err := service.LookupSymbols(ctx, "", "user.go", 10)
	if err != nil {
		t.Fatalf("LookupSymbols failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 symbols, got %d", len(results))
	}

	// Test lookup by name
	results, err = service.LookupSymbols(ctx, "User", "", 10)
	if err != nil {
		t.Fatalf("LookupSymbols by name failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected to find symbols matching 'User'")
	}

	// Test lookup with limit
	results, err = service.LookupSymbols(ctx, "", "user.go", 1)
	if err != nil {
		t.Fatalf("LookupSymbols with limit failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 symbol with limit, got %d", len(results))
	}
}

func TestService_LookupSymbols_NoStore(t *testing.T) {
	service, _, cleanup := newTestService(t)
	defer cleanup()

	service.store = nil

	ctx := context.Background()
	_, err := service.LookupSymbols(ctx, "", "", 10)
	if err == nil {
		t.Fatal("expected error when store is nil")
	}
	if err.Error() != "store not initialized" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestService_LookupSymbols_Empty(t *testing.T) {
	service, _, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()
	results, err := service.LookupSymbols(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("LookupSymbols failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 symbols, got %d", len(results))
	}
}

func TestService_IntegrationWithIndexer(t *testing.T) {
	// Full integration test: index a file and then query it via service
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	// Create a test Go file
	rootDir := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	testFile := filepath.Join(rootDir, "example.go")
	content := `package example

import "fmt"

type Widget struct {
	ID   int
	Name string
}

func NewWidget(name string) *Widget {
	return &Widget{Name: name}
}

func (w *Widget) Display() {
	fmt.Printf("Widget: %s\n", w.Name)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Index the file
	indexer := New(store, rootDir)
	ctx := context.Background()
	if err := indexer.Scan(ctx); err != nil {
		t.Fatalf("indexer.Scan failed: %v", err)
	}

	// Query via service
	service := NewService(store)

	// Lookup files
	files, err := service.LookupFiles(ctx, "", "example.go", 10)
	if err != nil {
		t.Fatalf("LookupFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "example.go" {
		t.Errorf("expected path example.go, got %s", files[0].Path)
	}
	if files[0].Language != "go" {
		t.Errorf("expected language go, got %s", files[0].Language)
	}

	// Lookup symbols
	symbols, err := service.LookupSymbols(ctx, "", "example.go", 100)
	if err != nil {
		t.Fatalf("LookupSymbols failed: %v", err)
	}
	if len(symbols) != 3 {
		t.Fatalf("expected 3 symbols, got %d", len(symbols))
	}

	// Verify specific symbols
	symbolsByName := make(map[string]storage.SymbolRecord)
	for _, sym := range symbols {
		symbolsByName[sym.Name] = sym
	}

	if widget, ok := symbolsByName["Widget"]; !ok {
		t.Error("expected to find Widget type")
	} else if widget.Kind != "type" {
		t.Errorf("Widget: expected kind type, got %s", widget.Kind)
	}

	if newWidget, ok := symbolsByName["NewWidget"]; !ok {
		t.Error("expected to find NewWidget function")
	} else if newWidget.Kind != "function" {
		t.Errorf("NewWidget: expected kind function, got %s", newWidget.Kind)
	}

	if display, ok := symbolsByName["Display"]; !ok {
		t.Error("expected to find Display method")
	} else if display.Kind != "method" {
		t.Errorf("Display: expected kind method, got %s", display.Kind)
	}
}

func TestService_LookupFiles_ConcurrentAccess(t *testing.T) {
	service, store, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Insert a file record
	fileRec := &storage.FileRecord{
		Path:      "concurrent.go",
		Checksum:  "abc123",
		Language:  "go",
		SizeBytes: 100,
	}
	if err := store.UpsertFileRecord(ctx, fileRec); err != nil {
		t.Fatalf("failed to insert file record: %v", err)
	}

	// Run concurrent lookups
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			_, err := service.LookupFiles(ctx, "", "", 10)
			if err != nil {
				t.Errorf("concurrent LookupFiles failed: %v", err)
			}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestService_LookupSymbols_ConcurrentAccess(t *testing.T) {
	service, store, cleanup := newTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Insert file and symbols
	fileRec := &storage.FileRecord{
		Path:      "concurrent.go",
		Checksum:  "abc123",
		Language:  "go",
		SizeBytes: 100,
	}
	if err := store.UpsertFileRecord(ctx, fileRec); err != nil {
		t.Fatalf("failed to insert file record: %v", err)
	}

	symbols := []storage.SymbolRecord{
		{
			FilePath:  "concurrent.go",
			Name:      "Test",
			Kind:      "function",
			Signature: "func Test()",
			StartLine: 1,
			EndLine:   5,
		},
	}
	if err := store.ReplaceSymbols(ctx, "concurrent.go", symbols); err != nil {
		t.Fatalf("failed to insert symbols: %v", err)
	}

	// Run concurrent lookups
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			_, err := service.LookupSymbols(ctx, "", "concurrent.go", 10)
			if err != nil {
				t.Errorf("concurrent LookupSymbols failed: %v", err)
			}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
