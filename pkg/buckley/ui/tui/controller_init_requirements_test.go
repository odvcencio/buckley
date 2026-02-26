package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/storage"
)

func TestNewController_RequiresStore(t *testing.T) {
	t.Parallel()

	_, err := NewController(ControllerConfig{})
	if err == nil {
		t.Fatal("expected error for missing store")
	}
	if !strings.Contains(err.Error(), "store required") {
		t.Fatalf("expected store required error, got: %v", err)
	}
}

func TestNewController_RequiresModelManager(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	_, err = NewController(ControllerConfig{Store: store})
	if err == nil {
		t.Fatal("expected error for missing model manager")
	}
	if !strings.Contains(err.Error(), "model manager required") {
		t.Fatalf("expected model manager required error, got: %v", err)
	}
}
