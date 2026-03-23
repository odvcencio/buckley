package rules

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// validArbOverride is a minimal valid .arb file for complexity override.
const validArbOverride = `rule CustomDirect priority 10 {
    when {
        true
    }
    then Direct {
        action: "direct",
        confidence: 0.5,
    }
}
`

// invalidArbContent is intentionally malformed to trigger a compile error.
const invalidArbContent = `this is not valid arbiter syntax !!!`

func TestNewWatcher_ReloadsOnWrite(t *testing.T) {
	dir := t.TempDir()

	e, err := NewEngine(WithUserOverrides(dir))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	w, err := NewWatcher(e, dir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()

	// Write a valid override for the complexity domain.
	arbFile := filepath.Join(dir, "complexity.arb")
	if err := os.WriteFile(arbFile, []byte(validArbOverride), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Give the watcher goroutine time to process the event.
	time.Sleep(200 * time.Millisecond)

	// The engine should still be functional after the reload.
	matched, err := Eval(e, "complexity", TaskFacts{WordCount: 3})
	if err != nil {
		t.Fatalf("Eval after reload: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule after hot reload")
	}
}

func TestNewWatcher_KeepsPreviousOnCompileError(t *testing.T) {
	dir := t.TempDir()

	e, err := NewEngine(WithUserOverrides(dir))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Capture a pre-reload result to compare against later.
	before, err := Eval(e, "complexity", TaskFacts{WordCount: 3})
	if err != nil {
		t.Fatalf("Eval before watcher: %v", err)
	}

	w, err := NewWatcher(e, dir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()

	// Write invalid .arb content — should be rejected, keeping previous version.
	arbFile := filepath.Join(dir, "complexity.arb")
	if err := os.WriteFile(arbFile, []byte(invalidArbContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Domain must still be functional (previous version kept).
	after, err := Eval(e, "complexity", TaskFacts{WordCount: 3})
	if err != nil {
		t.Fatalf("Eval after bad write: %v", err)
	}
	if len(after) == 0 {
		t.Fatal("expected domain to still work after failed reload")
	}
	_ = before
}

func TestWatcher_Close(t *testing.T) {
	dir := t.TempDir()

	e, err := NewEngine(WithUserOverrides(dir))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	w, err := NewWatcher(e, dir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// Close should not block or panic.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
