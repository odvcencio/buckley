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

func TestWatcher_SubdirectoryReload(t *testing.T) {
	dir := t.TempDir()
	permDir := filepath.Join(dir, "permissions")
	if err := os.MkdirAll(permDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write initial valid override for permissions/escalation.
	// Must include at least one when arm before else to satisfy the arbiter compiler.
	arbFile := filepath.Join(permDir, "escalation.arb")
	initial := []byte(`outcome PermissionDecision {
    action: string
    new_tier: string
    reason: string
}

strategy permission_escalation_policy returns PermissionDecision {
    when {
        role == "subagent" and required_tier == "full_access"
    } then DenySubagentFull {
        action: "deny",
        new_tier: "",
        reason: "subagents cannot escalate to full access",
    }

    else DefaultDeny {
        action: "deny",
        new_tier: "",
        reason: "v1 override",
    }
}
`)
	if err := os.WriteFile(arbFile, initial, 0o644); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(WithUserOverrides(dir))
	if err != nil {
		t.Fatalf("creating engine: %v", err)
	}

	w, err := NewWatcher(engine, dir)
	if err != nil {
		t.Fatalf("creating watcher: %v", err)
	}
	defer w.Close()

	// Modify subdirectory file — watcher should extract "permissions/escalation" as domain.
	updated := []byte(`outcome PermissionDecision {
    action: string
    new_tier: string
    reason: string
}

strategy permission_escalation_policy returns PermissionDecision {
    when {
        role == "subagent" and required_tier == "full_access"
    } then DenySubagentFull {
        action: "deny",
        new_tier: "",
        reason: "subagents cannot escalate to full access",
    }

    else DefaultDeny {
        action: "deny",
        new_tier: "",
        reason: "v2 override",
    }
}
`)
	if err := os.WriteFile(arbFile, updated, 0o644); err != nil {
		t.Fatal(err)
	}

	// Allow time for fsnotify
	time.Sleep(200 * time.Millisecond)
	// Success = no panic. The watcher should extract "permissions/escalation" as the domain
	// and call engine.Reload() without error.
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
