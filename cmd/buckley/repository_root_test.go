package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindGitRepoRoot_RejectsInvalidMarkers(t *testing.T) {
	for _, kind := range []string{"empty directory", "empty file", "malformed gitfile", "missing gitfile target", "damaged HEAD"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, ".git")
			var err error
			switch kind {
			case "empty directory":
				err = os.Mkdir(marker, 0o755)
			case "empty file":
				err = os.WriteFile(marker, nil, 0o644)
			case "malformed gitfile":
				err = os.WriteFile(marker, []byte("not Git metadata\n"), 0o644)
			case "missing gitfile target":
				err = os.WriteFile(marker, []byte("gitdir: missing\n"), 0o644)
			case "damaged HEAD":
				runGitIn(t, root, "init", "-q")
				err = os.WriteFile(filepath.Join(marker, "HEAD"), []byte("corrupt\n"), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(root, "child")
			if err := os.Mkdir(child, 0o755); err != nil {
				t.Fatal(err)
			}
			if got, ok := findGitRepoRoot(child); ok || got != "" {
				t.Fatalf("invalid marker promoted workspace to %q", got)
			}
			if got := normalizeProjectTrustPath(child); got != child {
				t.Fatalf("trust scope = %q, want %q", got, child)
			}
		})
	}
}

func TestFindGitRepoRoot_SeparateGitDirectory(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	runGitIn(t, root, "init", "-q", "--separate-git-dir", filepath.Join(root, "metadata"), repo)
	child := filepath.Join(repo, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := findGitRepoRoot(child); !ok || got != want {
		t.Fatalf("gitfile root = %q, %v, want %q", got, ok, want)
	}
}

func TestFindGitRepoRoot_ExplicitMarkerIgnoresAmbientGitDirectory(t *testing.T) {
	valid, other, invalid := t.TempDir(), t.TempDir(), t.TempDir()
	runGitIn(t, valid, "init", "-q")
	runGitIn(t, other, "init", "-q")
	if err := os.Mkdir(filepath.Join(invalid, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	if got, ok := findGitRepoRoot(invalid); ok || got != "" {
		t.Fatalf("ambient Git directory validated an empty marker: %q", got)
	}
	want, err := filepath.EvalSymlinks(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := findGitRepoRoot(valid); !ok || got != want {
		t.Fatalf("ambient Git directory changed actual root: %q, %v", got, ok)
	}
}

func TestFindSingleChildGitRepo_IgnoresInvalidMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "invalid", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "valid")
	runGitIn(t, root, "init", "-q", repo)
	if got, ok := findSingleChildGitRepo(root); !ok || got != repo {
		t.Fatalf("single valid child = %q, %v, want %q", got, ok, repo)
	}
}

func TestNormalizeProjectTrustPath_SymlinkedSubdirectory(t *testing.T) {
	root := t.TempDir()
	runGitIn(t, root, "init", "-q")
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(child, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalizeProjectTrustPath(alias); got != want {
		t.Fatalf("symlinked subdirectory trust root = %q, want %q", got, want)
	}
}

func TestProjectTrustStore_InvalidAncestorKeepsDecisionsSeparate(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := loadProjectTrustStore(filepath.Join(root, "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(first, projectTrustTrusted); err != nil {
		t.Fatal(err)
	}
	if got := store.Status(second); got != projectTrustUnknown {
		t.Fatalf("trust leaked to second workspace: %s", got)
	}
	if err := store.Set(second, projectTrustRestricted); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(first); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadProjectTrustStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Status(second); got != projectTrustRestricted {
		t.Fatalf("reset removed the second workspace decision: %s", got)
	}
}

func TestLoadProjectTrustStore_RepositoryChangesDoNotBroadenRecordedScope(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := loadProjectTrustStore(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(child, projectTrustTrusted); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, root, "init", "-q")
	reloaded, err := loadProjectTrustStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Status(root); got != projectTrustUnknown {
		t.Fatalf("new parent repository inherited a child approval: %s", got)
	}
	if reloaded.statuses[child] != projectTrustTrusted {
		t.Fatal("original child decision was lost")
	}
}
