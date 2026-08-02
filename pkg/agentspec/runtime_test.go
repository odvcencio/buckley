package agentspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestRuntimeProfileApplyToConfig(t *testing.T) {
	network := true
	spec := &Spec{
		Name:    "reviewer",
		Persona: "senior-reviewer",
		Models: ModelSpec{
			Chat:      "z-ai/glm-5.2",
			Planning:  "openai/gpt-5.5-high",
			Review:    "moonshotai/kimi-k2.7-code",
			Reasoning: "medium",
		},
		Tools: ToolSpec{
			Allow: []string{"read_file", "read_file", "search"},
			Deny:  []string{"run_shell"},
		},
		Policies: PolicySpec{ApprovalMode: "auto"},
		Sandbox: SandboxSpec{
			Mode:       "workspace",
			Network:    &network,
			ReadPaths:  []string{"/tmp/read"},
			WritePaths: []string{"/tmp/write"},
		},
	}
	cfg := config.DefaultConfig()

	ApplyToConfig(cfg, spec)

	if cfg.Models.Execution != "z-ai/glm-5.2" {
		t.Fatalf("execution model=%q", cfg.Models.Execution)
	}
	if cfg.Models.Planning != "openai/gpt-5.5" {
		t.Fatalf("planning model=%q", cfg.Models.Planning)
	}
	if cfg.Models.Review != "moonshotai/kimi-k2.7-code" {
		t.Fatalf("review model=%q", cfg.Models.Review)
	}
	if cfg.Models.Reasoning != "medium" {
		t.Fatalf("reasoning=%q", cfg.Models.Reasoning)
	}
	if !cfg.Personality.Enabled || cfg.Personality.DefaultPersona != "senior-reviewer" {
		t.Fatalf("persona not applied: %+v", cfg.Personality)
	}
	if cfg.Approval.Mode != "auto" || !cfg.Approval.AllowNetwork {
		t.Fatalf("approval not applied: %+v", cfg.Approval)
	}
	if !contains(cfg.Approval.AllowedTools, "read_file") || !contains(cfg.Approval.AllowedTools, "search") {
		t.Fatalf("allowed tools=%v", cfg.Approval.AllowedTools)
	}
	if countValue(cfg.Approval.AllowedTools, "read_file") != 1 {
		t.Fatalf("expected read_file to be deduplicated, got %v", cfg.Approval.AllowedTools)
	}
	if got := strings.Join(cfg.Approval.DeniedTools, ","); got != "run_shell" {
		t.Fatalf("denied tools=%q", got)
	}
	if cfg.Sandbox.Mode != "workspace" || !cfg.Sandbox.AllowNetwork {
		t.Fatalf("sandbox not applied: %+v", cfg.Sandbox)
	}
	if !contains(cfg.Sandbox.AllowedPaths, "/tmp/read") || !contains(cfg.Sandbox.AllowedPaths, "/tmp/write") {
		t.Fatalf("sandbox paths=%v", cfg.Sandbox.AllowedPaths)
	}
	if !contains(cfg.Approval.TrustedPaths, "/tmp/write") {
		t.Fatalf("trusted paths=%v", cfg.Approval.TrustedPaths)
	}
}

func TestLoadRuntimeProfilePromptSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent-notes.md"), []byte("Prefer focused validation."), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	specPath := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(specPath, []byte(`
version: buckley.agent/v1
name: verifier
summary: Checks changes carefully
persona: verification-lead
instructions:
  prompt: Run targeted tests before broad tests.
  files: [agent-notes.md]
models:
  chat: z-ai/glm-5.2
tools:
  tier: standard
  allow: [read_file]
policies:
  approval_mode: safe
  domains: [approval]
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	profile, err := LoadRuntimeProfile(specPath)
	if err != nil {
		t.Fatalf("LoadRuntimeProfile: %v", err)
	}
	section := profile.PromptSection()
	for _, want := range []string{
		"Agent: verifier",
		"Summary: Checks changes carefully",
		"Persona: verification-lead",
		"Run targeted tests before broad tests.",
		"Prefer focused validation.",
		"Allow: read_file",
		"Domains: approval",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("expected prompt section to contain %q\n%s", want, section)
		}
	}
}

func TestLoadRuntimeProfileInvalidSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(`version: bad
name: ""
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	_, err := LoadRuntimeProfile(path)
	if err == nil {
		t.Fatal("expected invalid spec error")
	}
	if !strings.Contains(err.Error(), "unsupported version") || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countValue(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}
