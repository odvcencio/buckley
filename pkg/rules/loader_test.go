package rules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoader_LoadEmbedded(t *testing.T) {
	l := NewLoader("")
	src, err := l.Load("complexity")
	if err != nil {
		t.Fatalf("load embedded complexity: %v", err)
	}
	if len(src) == 0 {
		t.Fatal("expected non-empty source for complexity")
	}
}

func TestLoader_LoadAllDomains(t *testing.T) {
	l := NewLoader("")
	domains, err := l.Domains()
	if err != nil {
		t.Fatalf("Domains: %v", err)
	}
	expected := []string{"approval", "compaction", "complexity", "coordinator", "escalation", "gts_context", "oneshot", "reasoning", "retry", "risk", "routing", "spawning", "tool_budget"}
	if len(domains) != len(expected) {
		t.Fatalf("got %d domains, want %d: %v", len(domains), len(expected), domains)
	}
	for i, d := range domains {
		if d != expected[i] {
			t.Errorf("domain[%d] = %q, want %q", i, d, expected[i])
		}
	}
}

func TestLoader_LoadUserOverride(t *testing.T) {
	dir := t.TempDir()
	content := []byte("rule Custom priority 10 {\n    when {\n        true\n    }\n    then Custom {\n        action: \"custom\",\n    }\n}\n")
	os.WriteFile(filepath.Join(dir, "complexity.arb"), content, 0644)

	l := NewLoader(dir)
	src, err := l.Load("complexity")
	if err != nil {
		t.Fatalf("load override: %v", err)
	}
	if string(src) != string(content) {
		t.Errorf("expected override content, got: %s", src)
	}
}

func TestLoader_LoadMissing(t *testing.T) {
	l := NewLoader("")
	_, err := l.Load("nonexistent_domain")
	if err == nil {
		t.Fatal("expected error for missing domain")
	}
}
