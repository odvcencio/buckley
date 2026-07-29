package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowRiskContextProviderSurfacesRefSHAProvenance(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	workflow := `name: Release
on:
  workflow_dispatch:
    inputs:
      tag:
        required: true
jobs:
  release:
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.tag }}
          fetch-depth: 0
      - name: Validate source
        run: git merge-base --is-ancestor "$GITHUB_SHA" origin/main
      - name: Publish
        run: goreleaser release --clean
`
	filename := filepath.Join(workflowDir, "release.yml")
	if err := os.WriteFile(filename, []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	provider := NewWorkflowRiskContextProvider()
	evidence, err := provider.Collect(context.Background(), PRContextProviderRequest{
		RepositoryRoot: root,
		ChangedFiles:   []string{".github/workflows/docker-images.yml", ".github/workflows/release.yml"},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(evidence) != 1 {
		t.Fatalf("evidence = %+v, want one provenance hypothesis", evidence)
	}
	item := evidence[0]
	if item.Priority != workflowRiskEvidencePriority || len(item.Files) != 1 || item.Files[0] != ".github/workflows/release.yml" {
		t.Fatalf("evidence metadata = %+v", item)
	}
	for _, want := range []string{
		"manual dispatch at line 3",
		"explicit checkout ref",
		"event SHA at line 15",
		"does not rewrite workflow event variables",
		"demonstrated MAJOR defect",
		"git rev-parse HEAD",
	} {
		if !strings.Contains(item.Body, want) {
			t.Fatalf("evidence missing %q:\n%s", want, item.Body)
		}
	}
}

func TestWorkflowRiskContextProviderOmitsUnrelatedWorkflow(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	workflow := `name: Test
on: pull_request
jobs:
  test:
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`
	if err := os.WriteFile(filepath.Join(workflowDir, "ci.yml"), []byte(workflow), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	evidence, err := NewWorkflowRiskContextProvider().Collect(context.Background(), PRContextProviderRequest{
		RepositoryRoot: root,
		ChangedFiles:   []string{".github/workflows/ci.yml"},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(evidence) != 0 {
		t.Fatalf("evidence = %+v, want none", evidence)
	}
}
