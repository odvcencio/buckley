package widgets

import (
	"strings"
	"testing"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	uitesting "github.com/odvcencio/fluffyui/testing"
)

func renderStatusBar(t *testing.T, sb *StatusBar, width int) string {
	t.Helper()
	be := uitesting.RenderWidget(sb, width, 1)
	return be.Capture()
}

func TestStatusBar_Defaults(t *testing.T) {
	sb := NewStatusBar(StatusBarConfig{})

	if sb == nil {
		t.Fatal("expected non-nil StatusBar")
	}
	if sb.AccessibleLabel() != "Ready" {
		t.Errorf("expected accessible label Ready, got %q", sb.AccessibleLabel())
	}

	output := renderStatusBar(t, sb, 40)
	if !strings.Contains(output, "Ready") {
		t.Errorf("expected output to contain Ready, got %q", output)
	}
}

func TestStatusBar_Measure(t *testing.T) {
	sb := NewStatusBar(StatusBarConfig{})
	constraints := runtime.Constraints{MaxWidth: 80, MaxHeight: 24}
	size := sb.Measure(constraints)
	if size.Width != 80 {
		t.Errorf("expected width 80, got %d", size.Width)
	}
	if size.Height != 1 {
		t.Errorf("expected height 1, got %d", size.Height)
	}
}

func TestStatusBar_Render_WithTokensAndContext(t *testing.T) {
	status := state.NewSignal("Working")
	mode := state.NewSignal("classic")
	tokens := state.NewSignal(5000)
	cost := state.NewSignal(25.0)
	contextUsed := state.NewSignal(1200)
	contextBudget := state.NewSignal(8000)
	streaming := state.NewSignal(true)
	progressItems := state.NewSignal([]progress.Progress{{ID: "p1", Label: "Run"}})
	effects := state.NewSignal(false)

	sb := NewStatusBar(StatusBarConfig{
		StatusText:     status,
		StatusMode:     mode,
		Tokens:         tokens,
		CostCents:      cost,
		ContextUsed:    contextUsed,
		ContextBudget:  contextBudget,
		IsStreaming:    streaming,
		ProgressItems:  progressItems,
		EffectsEnabled: effects,
		BGStyle:        backend.DefaultStyle(),
		TextStyle:      backend.DefaultStyle(),
		ModeStyle:      backend.DefaultStyle().Bold(true),
	})

	output := renderStatusBar(t, sb, 80)
	if !strings.Contains(output, "Working") {
		t.Errorf("expected output to contain status, got %q", output)
	}
	if !strings.Contains(output, "classic") {
		t.Errorf("expected output to contain mode, got %q", output)
	}
	if !strings.Contains(output, "ctx") {
		t.Errorf("expected output to contain ctx, got %q", output)
	}
	if !strings.Contains(output, "5.0K") {
		t.Errorf("expected output to contain tokens, got %q", output)
	}
	if !strings.Contains(output, "$0.25") {
		t.Errorf("expected output to contain cost, got %q", output)
	}
}

func TestStatusBar_AccessibleDescription(t *testing.T) {
	status := state.NewSignal("Ready")
	mode := state.NewSignal("classic")
	tokens := state.NewSignal(120)
	contextUsed := state.NewSignal(100)
	contextBudget := state.NewSignal(1000)
	streaming := state.NewSignal(true)
	effects := state.NewSignal(false)

	sb := NewStatusBar(StatusBarConfig{
		StatusText:     status,
		StatusMode:     mode,
		Tokens:         tokens,
		ContextUsed:    contextUsed,
		ContextBudget:  contextBudget,
		IsStreaming:    streaming,
		EffectsEnabled: effects,
		BGStyle:        backend.DefaultStyle(),
		TextStyle:      backend.DefaultStyle(),
		ModeStyle:      backend.DefaultStyle(),
	})

	desc := sb.AccessibleDescription()
	if !strings.Contains(desc, "streaming") {
		t.Errorf("expected description to mention streaming, got %q", desc)
	}
	if !strings.Contains(desc, "classic") {
		t.Errorf("expected description to mention mode, got %q", desc)
	}
	if !strings.Contains(desc, "ctx") {
		t.Errorf("expected description to mention ctx, got %q", desc)
	}
	if !strings.Contains(desc, "tokens") {
		t.Errorf("expected description to mention tokens, got %q", desc)
	}
}
