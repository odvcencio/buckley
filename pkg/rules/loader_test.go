package rules

import (
	"os"
	"path/filepath"
	"strings"
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
	// 14 flat domains + 3 permissions/ subdomain = 17 minimum
	if len(domains) < 17 {
		t.Fatalf("got %d domains, want at least 17: %v", len(domains), domains)
	}
	// Verify flat domains are still present
	flatExpected := []string{"approval", "compaction", "complexity", "coordinator", "escalation", "gts_context", "oneshot", "reasoning", "retry", "risk", "role_permissions", "routing", "spawning", "tool_budget"}
	domainSet := make(map[string]bool, len(domains))
	for _, d := range domains {
		domainSet[d] = true
	}
	for _, want := range flatExpected {
		if !domainSet[want] {
			t.Errorf("missing expected flat domain %q", want)
		}
	}
	// Verify permissions subdomain entries are present
	permExpected := []string{"permissions/delegation", "permissions/escalation", "permissions/sandbox"}
	for _, want := range permExpected {
		if !domainSet[want] {
			t.Errorf("missing expected subdomain %q", want)
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

func TestLoader_LoadSubdirectoryDomain(t *testing.T) {
	loader := NewLoader("")
	data, err := loader.Load("permissions/escalation")
	if err != nil {
		t.Fatalf("loading permissions/escalation: %v", err)
	}
	if len(data) == 0 {
		t.Error("permissions/escalation is empty")
	}
	if !strings.Contains(string(data), "permission_escalation_policy") {
		t.Error("permissions/escalation does not contain expected strategy name")
	}
}

func TestLoader_SubdirectoryUserOverride(t *testing.T) {
	dir := t.TempDir()
	permDir := filepath.Join(dir, "permissions")
	if err := os.MkdirAll(permDir, 0o755); err != nil {
		t.Fatal(err)
	}
	override := []byte("outcome X { a: string }\nstrategy test_strat returns X { else D { a: \"overridden\" } }")
	if err := os.WriteFile(filepath.Join(permDir, "escalation.arb"), override, 0o644); err != nil {
		t.Fatal(err)
	}
	loader := NewLoader(dir)
	data, err := loader.Load("permissions/escalation")
	if err != nil {
		t.Fatalf("loading override: %v", err)
	}
	if !strings.Contains(string(data), "overridden") {
		t.Error("expected user override content")
	}
}
