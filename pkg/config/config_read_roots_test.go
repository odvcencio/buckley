package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveReadRoots_ExpandsValidatesAndDeduplicates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPEC_DIR", "specs")
	space := filepath.Join(home, ".hyphae", "spaces", "bv")
	file := filepath.Join(home, "one.md")
	for _, d := range []string{space, filepath.Join(home, "specs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveReadRoots(home, []string{"~/.hyphae/spaces/bv", " $HOME/one.md ", ""}, []string{"~/.hyphae/spaces/bv", "$SPEC_DIR"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{space, file, filepath.Join(home, "specs")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("roots = %v, want %v", got, want)
	}
	if _, err := ResolveReadRoots(home, []string{"~/missing"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing root error = %v", err)
	}
	if none, err := ResolveReadRoots(home); err != nil || len(none) != 0 {
		t.Fatalf("no roots by default: %v %v", none, err)
	}
}

func TestToolsConfig_ReadRootsCombineUserAndProject(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Tools.ReadRoots) != 0 {
		t.Fatalf("default read roots = %v, want none", cfg.Tools.ReadRoots)
	}
	if err := mergeConfigData(cfg, []byte("tools:\n  read_roots: [\"/a\", \"/b\"]\n"), "user config", false); err != nil {
		t.Fatal(err)
	}
	if err := mergeConfigData(cfg, []byte("tools:\n  read_roots: [\"/b\", \"/c\"]\n"), "project config", true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Tools.ReadRoots, ","); got != "/a,/b,/c" {
		t.Fatalf("combined roots = %s", got)
	}
}

func TestResolveReadRoots_EmptyExpansionCannotBroadenRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BV_SPEC_HOME", home)
	t.Setenv("BV_SPEC_SPACE", "")
	// Expanding this with os.ExpandEnv would silently allowlist the whole home.
	for _, root := range []string{"$BV_SPEC_HOME/$BV_SPEC_SPACE", "$BV_SPEC_HOME/${BV_SPEC_SPACE}"} {
		if got, err := ResolveReadRoots(home, []string{root}); err == nil || got != nil {
			t.Fatalf("empty variable broadened allowlist: roots=%v error=%v", got, err)
		}
	}
}

func TestResolveReadRoots_RejectsSpecialFiles(t *testing.T) {
	if info, err := os.Stat(os.DevNull); err != nil || info.Mode().IsRegular() {
		t.Skip("no special null device")
	}
	if got, err := ResolveReadRoots(t.TempDir(), []string{os.DevNull}); err == nil || got != nil {
		t.Fatalf("special file accepted: roots=%v error=%v", got, err)
	}
}
