package model

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyReviewWorkspace_PatchOrderingAndOwnership(t *testing.T) {
	for _, reordered := range []bool{false, true} {
		name := "git order"
		if reordered {
			name = "reordered descriptor"
		}
		t.Run(name, func(t *testing.T) {
			repo := initReviewSnapshotRepo(t)
			other := filepath.Join(repo, "other.txt")
			if err := os.WriteFile(other, []byte("original other\n"), 0600); err != nil {
				t.Fatal(err)
			}
			runCodexProviderGit(t, repo, "add", "other.txt")
			runCodexProviderGit(t, repo, "commit", "-m", "second tracked file")
			for _, file := range []string{"behavior.txt", "other.txt"} {
				if err := os.WriteFile(filepath.Join(repo, file), []byte("captured "+file+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			commit := runCodexProviderGit(t, repo, "rev-parse", "HEAD")
			patch, err := exec.Command("git", "-C", repo, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "HEAD", "--").Output()
			if err != nil {
				t.Fatal(err)
			}
			if reordered {
				boundary := bytes.Index(patch, []byte("\ndiff --git "))
				if boundary < 0 {
					t.Fatal("fixture needs two file segments")
				}
				patch = append(append([]byte(nil), patch[boundary+1:]...), patch[:boundary+1]...)
			}
			original := bytes.Clone(patch)
			snapshot, err := NewReviewSnapshot(ReviewSnapshotTrackedWorktree, repo, repo, commit, patch)
			if err != nil {
				t.Fatal(err)
			}
			id := snapshot.ID()
			patch[0] = 'X'
			exposed := snapshot.Patch()
			exposed[0] = 'Y'
			if !bytes.Equal(snapshot.Patch(), original) {
				t.Fatal("descriptor aliases caller-owned patch")
			}
			dir, cleanup, err := PrepareReviewWorkspace(t.Context(), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			for _, file := range []string{"behavior.txt", "other.txt"} {
				assertCodexProviderFileContent(t, filepath.Join(dir, file), "captured "+file+"\n")
			}
			if err := VerifyReviewWorkspace(t.Context(), dir, snapshot); err != nil {
				t.Fatalf("matching snapshot: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("changed after capture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := VerifyReviewWorkspace(t.Context(), dir, snapshot); err == nil || !strings.Contains(err.Error(), "tracked source differs from immutable snapshot") {
				t.Fatalf("drift was not rejected: %v", err)
			}
			if snapshot.ID() != id || !bytes.Equal(snapshot.Patch(), original) {
				t.Fatal("verification mutated immutable descriptor")
			}
			assertCodexProviderFileContent(t, filepath.Join(repo, "other.txt"), "captured other.txt\n")
		})
	}
}
