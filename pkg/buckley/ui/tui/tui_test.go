package tui

import (
	"testing"

	"github.com/odvcencio/fluffyui/theme"
)

func TestLayoutConstants(t *testing.T) {
	// Verify layout constants are reasonable
	if theme.Layout.HeaderHeight < 1 {
		t.Error("HeaderHeight should be at least 1")
	}
	if theme.Layout.StatusHeight < 1 {
		t.Error("StatusHeight should be at least 1")
	}
	if theme.Layout.InputMinHeight < 1 {
		t.Error("InputMinHeight should be at least 1")
	}
	if theme.Layout.PickerMaxHeight < 5 {
		t.Error("PickerMaxHeight should be at least 5 for usability")
	}
}

func TestSymbolsNotEmpty(t *testing.T) {
	symbols := []struct {
		name  string
		value string
	}{
		{"Bullet", theme.Symbols.Bullet},
		{"Check", theme.Symbols.Check},
		{"Cross", theme.Symbols.Cross},
		{"BorderHorizontal", theme.Symbols.BorderHorizontal},
		{"BorderVertical", theme.Symbols.BorderVertical},
		{"User", theme.Symbols.User},
		{"Assistant", theme.Symbols.Assistant},
		{"ModeNormal", theme.Symbols.ModeNormal},
		{"ModeShell", theme.Symbols.ModeShell},
		{"ModeEnv", theme.Symbols.ModeEnv},
		{"ModeSearch", theme.Symbols.ModeSearch},
	}

	for _, s := range symbols {
		if s.value == "" {
			t.Errorf("Symbol %s is empty", s.name)
		}
	}
}

func TestCoalescerConfig(t *testing.T) {
	cfg := DefaultCoalescerConfig()
	if cfg.MaxWait <= 0 {
		t.Error("MaxWait should be positive")
	}
	if cfg.MaxChars <= 0 {
		t.Error("MaxChars should be positive")
	}
}

func TestRenderMetricsZeroValue(t *testing.T) {
	var m RenderMetrics
	if m.FrameCount != 0 {
		t.Error("FrameCount should be 0")
	}
	if m.DroppedFrames != 0 {
		t.Error("DroppedFrames should be 0")
	}
}
