package personality

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefinitionsFromDirs_LoadsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "builder.yaml"), "name: Builder\nsystem_prompt: test\n")
	writeFile(t, filepath.Join(dir, "calm.yaml"), "system_prompt: calm\n")

	defs, err := LoadDefinitionsFromDirs([]string{dir})
	if err != nil {
		t.Fatalf("LoadDefinitionsFromDirs returned error: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("expected 2 personas, got %d", len(defs))
	}
	if defs["builder"].Name != "Builder" {
		t.Fatalf("expected Builder name, got %s", defs["builder"].Name)
	}
	if defs["calm"].Name != "Calm" {
		t.Fatalf("expected defaulted name Calm, got %s", defs["calm"].Name)
	}
}

func TestLoadDefinitionsFromDirs_IgnoresMissingAndInvalid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "notes.txt"), "not yaml")

	defs, err := LoadDefinitionsFromDirs([]string{filepath.Join(dir, "missing"), dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 0 {
		t.Fatalf("expected no personas, got %d", len(defs))
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("failed to write file %s: %v", path, err)
	}
}
