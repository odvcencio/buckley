package agentspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverProjectSpecsFindsAncestorConventions(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".buckley", "agents", "review"), 0o755); err != nil {
		t.Fatalf("mkdir specs: %v", err)
	}
	writeAgentSpec(t, filepath.Join(root, ".buckley", "agent.yaml"), `
version: buckley.agent/v1
name: project
summary: Root project agent
subagents:
  - name: reviewer
  - name: builder
`)
	writeAgentSpec(t, filepath.Join(root, ".buckley", "agents", "review", "strict.yaml"), `
version: buckley.agent/v1
name: strict-reviewer
summary: Reviews changes strictly
`)
	nested := filepath.Join(root, "pkg", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	discovery, err := DiscoverProjectSpecs(nested)
	if err != nil {
		t.Fatalf("DiscoverProjectSpecs: %v", err)
	}
	if discovery.Root != root {
		t.Fatalf("root=%q want %q", discovery.Root, root)
	}
	if len(discovery.Specs) != 2 {
		t.Fatalf("specs=%+v want 2", discovery.Specs)
	}
	if discovery.Specs[0].Name != "project" || !discovery.Specs[0].Valid {
		t.Fatalf("unexpected root spec: %+v", discovery.Specs[0])
	}
	if strings.Join(discovery.Specs[0].Subagents, ",") != "builder,reviewer" {
		t.Fatalf("subagents=%v", discovery.Specs[0].Subagents)
	}
	if discovery.Specs[1].Name != "strict-reviewer" || discovery.Specs[1].Summary != "Reviews changes strictly" {
		t.Fatalf("unexpected nested spec: %+v", discovery.Specs[1])
	}
}

func TestDiscoverProjectSpecsReportsInvalidSpecs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".buckley", "agents"), 0o755); err != nil {
		t.Fatalf("mkdir specs: %v", err)
	}
	writeAgentSpec(t, filepath.Join(root, ".buckley", "agents", "bad.yaml"), `
version: other.agent/v1
name: ""
`)

	discovery, err := DiscoverProjectSpecs(root)
	if err != nil {
		t.Fatalf("DiscoverProjectSpecs: %v", err)
	}
	if len(discovery.Specs) != 1 {
		t.Fatalf("specs=%+v want 1", discovery.Specs)
	}
	spec := discovery.Specs[0]
	if spec.Valid {
		t.Fatalf("invalid spec reported valid: %+v", spec)
	}
	joined := diagnosticsText(spec.Diagnostics)
	if !strings.Contains(joined, "unsupported version") || !strings.Contains(joined, "name is required") {
		t.Fatalf("diagnostics missing expected errors:\n%s", joined)
	}
}

func TestDiscoverProjectSpecsEmptyWhenNoConventions(t *testing.T) {
	discovery, err := DiscoverProjectSpecs(t.TempDir())
	if err != nil {
		t.Fatalf("DiscoverProjectSpecs: %v", err)
	}
	if discovery.Root != "" || len(discovery.Specs) != 0 {
		t.Fatalf("unexpected discovery: %+v", discovery)
	}
}

func writeAgentSpec(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(data)+"\n"), 0o644); err != nil {
		t.Fatalf("write spec %s: %v", path, err)
	}
}
