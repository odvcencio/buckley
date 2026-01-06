package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
)

func TestGeneratePersonaCreatesSluggedFile(t *testing.T) {
	tmp := t.TempDir()
	path, err := GeneratePersona(PersonaOptions{
		BaseDir: tmp,
		Name:    "Friendly Reviewer",
		Tone:    "friendly",
		Force:   true,
	})
	if err != nil {
		t.Fatalf("GeneratePersona failed: %v", err)
	}
	expected := filepath.Join(tmp, "friendly-reviewer.yaml")
	if path != expected {
		t.Fatalf("expected persona path %s, got %s", expected, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persona file missing: %v", err)
	}
}

func TestGenerateAgentsUsesDefaults(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "AGENTS.md")
	if _, err := GenerateAgents(AgentsOptions{
		Path:    out,
		Force:   true,
		Config:  &config.Config{},
		Context: &projectcontext.ProjectContext{Summary: "Unit test project"},
	}); err != nil {
		t.Fatalf("GenerateAgents failed: %v", err)
	}
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read AGENTS: %v", err)
	}
	if len(content) == 0 {
		t.Fatalf("expected AGENTS content")
	}
}

func TestResolveBaseDirPrefersProjectRoot(t *testing.T) {
	tmp := t.TempDir()
	base, err := ResolveBaseDir(tmp, ".buckley/skills", false)
	if err != nil {
		t.Fatalf("ResolveBaseDir failed: %v", err)
	}
	expected := filepath.Join(tmp, ".buckley", "skills")
	if base != expected {
		t.Fatalf("expected %s, got %s", expected, base)
	}
}
