package oneshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeduplicateNodeSnapshotEvidencePreservesPackageCoverage(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"assets", "scripts", "nested"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{".", "nested"} {
		if err := os.WriteFile(filepath.Join(root, dir, "package.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var requests []AgentEvidenceRequest
	for _, dir := range []string{".", "assets", "scripts", "nested"} {
		for _, kind := range []string{"build", "test"} {
			requests = append(requests, AgentEvidenceRequest{Tool: "run_verification", Parameters: map[string]any{"language": "node", "kind": kind, "path": dir}})
		}
	}
	requests = append(requests,
		AgentEvidenceRequest{Tool: "run_verification", Parameters: map[string]any{"language": "node", "kind": "test", "path": "scripts", "pattern": "other"}},
		AgentEvidenceRequest{Tool: "run_verification", Parameters: map[string]any{"language": "go", "kind": "test", "path": "scripts"}},
		AgentEvidenceRequest{Tool: "run_verification", Parameters: map[string]any{"language": "node", "kind": "test", "path": "../outside"}},
	)
	got := deduplicateNodeSnapshotEvidence(root, requests)
	if len(got) != 7 {
		t.Fatalf("got %d requests, want two per package and three distinct/invalid requests", len(got))
	}
	for i, path := range []string{".", ".", "nested", "nested", ".", "scripts", "../outside"} {
		if got[i].Parameters["path"] != path {
			t.Fatalf("request %d path=%v want %s", i, got[i].Parameters["path"], path)
		}
	}
	if requests[2].Parameters["path"] != "assets" {
		t.Fatal("mutated caller's request")
	}
}
