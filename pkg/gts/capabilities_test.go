package gts

import (
	"strings"
	"testing"
)

func TestCapabilities_NotEmpty(t *testing.T) {
	caps := Capabilities()
	if len(caps) == 0 {
		t.Fatal("expected non-empty capabilities list")
	}
	for _, c := range caps {
		if c.Tool == "" {
			t.Error("capability has empty tool name")
		}
		if c.Description == "" {
			t.Error("capability has empty description")
		}
	}
}

func TestPromptSection_WithTools(t *testing.T) {
	section := PromptSection("callgraph,impact,scope")
	if section == "" {
		t.Fatal("expected non-empty prompt section")
	}
	if !strings.Contains(section, "gts callgraph") {
		t.Error("expected callgraph in prompt section")
	}
	if !strings.Contains(section, "gts impact") {
		t.Error("expected impact in prompt section")
	}
	if !strings.Contains(section, "gts scope") {
		t.Error("expected scope in prompt section")
	}
}

func TestPromptSection_Empty(t *testing.T) {
	section := PromptSection("")
	if section != "" {
		t.Error("expected empty prompt section for empty tools")
	}
}

func TestPromptSection_UnknownTool(t *testing.T) {
	section := PromptSection("nonexistent")
	// Should still return the header even if no tools matched
	if !strings.Contains(section, "Structural Intelligence") {
		t.Error("expected header even with unknown tool")
	}
}
