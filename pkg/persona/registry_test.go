package persona

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFileMkdir(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func writePersonaFile(t *testing.T, dir, name, model string) {
	t.Helper()
	if err := writeFileMkdir(filepath.Join(dir, name+".md"),
		"---\nname: "+name+"\nmodel: "+model+"\n---\nbody for "+name+"\n"); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
}

func TestDiscover_PrecedenceOrder(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	// A persona defined only at the user level.
	writePersonaFile(t, filepath.Join(home, ".claude", "agents"), "user-only", "haiku")

	// A persona present at both user and project .claude/agents; project
	// must win.
	writePersonaFile(t, filepath.Join(home, ".claude", "agents"), "shared", "haiku")
	writePersonaFile(t, filepath.Join(project, ".claude", "agents"), "shared", "sonnet")

	// A persona present at both project .claude/agents and project
	// .tiller/agents; .tiller/agents must win.
	writePersonaFile(t, filepath.Join(project, ".claude", "agents"), "tiller-priority", "sonnet")
	writePersonaFile(t, filepath.Join(project, ".tiller", "agents"), "tiller-priority", "opus")

	reg, err := Discover(project, home)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if p, ok := reg.Get("user-only"); !ok || p.Model != "haiku" {
		t.Fatalf("user-only = %+v, ok=%v, want model=haiku", p, ok)
	}
	if p, ok := reg.Get("shared"); !ok || p.Model != "sonnet" {
		t.Fatalf("shared = %+v, ok=%v, want model=sonnet (project wins over user)", p, ok)
	}
	if p, ok := reg.Get("tiller-priority"); !ok || p.Model != "opus" {
		t.Fatalf("tiller-priority = %+v, ok=%v, want model=opus (.tiller/agents wins over .claude/agents)", p, ok)
	}

	names := make([]string, 0)
	for _, p := range reg.List() {
		names = append(names, p.Name)
	}
	want := []string{"shared", "tiller-priority", "user-only"}
	if len(names) != len(want) {
		t.Fatalf("List() names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("List() names = %v, want %v", names, want)
		}
	}
}

func TestDiscover_MissingDirectoriesAreSkipped(t *testing.T) {
	reg, err := Discover(filepath.Join(t.TempDir(), "does-not-exist"), filepath.Join(t.TempDir(), "also-missing"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("List() = %v, want empty", reg.List())
	}
}

func TestDiscover_SkipsNonPersonaMarkdownFiles(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".claude", "agents")
	if err := writeFileMkdir(filepath.Join(dir, "README.md"), "# Not a persona\n\njust prose.\n"); err != nil {
		t.Fatalf("writing README fixture: %v", err)
	}
	writePersonaFile(t, dir, "tiller-worker", "sonnet")

	reg, err := Discover(project, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, ok := reg.Get("README"); ok {
		t.Fatalf("README.md should not have been registered as a persona")
	}
	if _, ok := reg.Get("tiller-worker"); !ok {
		t.Fatalf("tiller-worker should have been discovered")
	}
}

func TestRegistry_ResolveHandlesAtPrefix(t *testing.T) {
	reg := NewRegistry()
	reg.Add(Persona{Name: "tiller-worker", Model: "sonnet"})

	if _, ok := reg.Resolve("@tiller-worker"); !ok {
		t.Fatalf("Resolve(@tiller-worker) should find the persona")
	}
	if _, ok := reg.Resolve("tiller-worker"); !ok {
		t.Fatalf("Resolve(tiller-worker) should find the persona")
	}
	if _, ok := reg.Resolve("@missing"); ok {
		t.Fatalf("Resolve(@missing) should not find a persona")
	}
}

func TestRegistry_NilSafety(t *testing.T) {
	var reg *Registry
	if _, ok := reg.Get("anything"); ok {
		t.Fatalf("nil registry Get should report not-found")
	}
	if reg.List() != nil {
		t.Fatalf("nil registry List should return nil")
	}
	reg.Add(Persona{Name: "irrelevant"}) // must not panic
}
