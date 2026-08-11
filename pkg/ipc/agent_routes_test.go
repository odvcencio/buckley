package ipc

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/headless"
)

func TestResolveHeadlessAgentProfile_SelectsSubagent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".buckley"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".buckley", "agent.yaml")
	if err := os.WriteFile(path, []byte(`version: buckley.agent/v1
name: workspace
summary: Workspace coordinator
subagents:
  - name: reviewer
    model: codex/gpt-5.6-sol
    tool_tier: read_only
    tools:
      allow: [read_file, search_text]
      deny: [run_shell]
    instructions: Check the repository carefully.
`), 0o644); err != nil {
		t.Fatal(err)
	}

	server := &Server{projectRoot: root}
	got, model, policy, err := server.resolveHeadlessAgentSelection(root, "workspace", "reviewer")
	if err != nil {
		t.Fatalf("resolveHeadlessAgentProfile() error = %v", err)
	}
	if model != "codex/gpt-5.6-sol" {
		t.Fatalf("resolved model = %q, want codex/gpt-5.6-sol", model)
	}
	if policy == nil || strings.Join(policy.AllowedTools, ",") != "read_file,search_text" || strings.Join(policy.DeniedTools, ",") != "run_shell" {
		t.Fatalf("resolved tool policy = %#v", policy)
	}
	for _, want := range []string{
		"Agent: workspace/reviewer",
		"Models: chat=codex/gpt-5.6-sol",
		"Tool tier: read_only",
		"Subagent Instructions:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("profile missing %q:\n%s", want, got)
		}
	}
}

func TestResolveHeadlessAgentProfileRejectsUnknownSubagent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".buckley"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".buckley", "agent.yaml"), []byte(`version: buckley.agent/v1
name: workspace
subagents:
  - name: builder
`), 0o644); err != nil {
		t.Fatal(err)
	}

	server := &Server{projectRoot: root}
	if _, err := server.resolveHeadlessAgentProfile(root, "", "missing"); err == nil {
		t.Fatal("expected unknown subagent error")
	}
}

func TestResolveAgentProjectPathStaysWithinProjectRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "repo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	server := &Server{projectRoot: root}
	got, err := server.resolveAgentProjectPath("repo")
	if err != nil {
		t.Fatalf("resolveAgentProjectPath() error = %v", err)
	}
	if got != inside {
		t.Fatalf("project path = %q, want %q", got, inside)
	}
	if _, err := server.resolveAgentProjectPath("../outside"); err == nil {
		t.Fatal("expected project-root boundary error")
	}
}

func TestMergeHeadlessToolPolicies_ProfileNeverWidensRequest(t *testing.T) {
	merged := mergeHeadlessToolPolicies(
		&headless.ToolPolicy{
			AllowedTools:       []string{"read_file", "search_text"},
			DeniedTools:        []string{"run_shell"},
			MaxExecTimeSeconds: 30,
		},
		&headless.ToolPolicy{
			AllowedTools:       []string{"read_file", "run_shell"},
			DeniedTools:        []string{"write_file"},
			MaxExecTimeSeconds: 10,
		},
	)
	want := &headless.ToolPolicy{
		AllowedTools:       []string{"read_file"},
		DeniedTools:        []string{"run_shell", "write_file"},
		MaxExecTimeSeconds: 10,
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged policy = %#v, want %#v", merged, want)
	}

	denyAll := mergeHeadlessToolPolicies(
		&headless.ToolPolicy{AllowedTools: []string{"read_file"}},
		&headless.ToolPolicy{AllowedTools: []string{"search_text"}},
	)
	if denyAll == nil || denyAll.AllowedTools == nil || len(denyAll.AllowedTools) != 0 {
		t.Fatalf("disjoint profile allow list should be explicit deny-all, got %#v", denyAll)
	}
}
