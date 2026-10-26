package shadowgit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSplitRetention_ReusesStableBlobAfterMixedPackExpires(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("GIT_DEFAULT_HASH", format)
			repo := newTestRepo(t)
			retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
			retentionGit(t, repo, "config", "pack.packSizeLimit", "0")
			original := userGitFiles(t, repo)
			if err := os.WriteFile(filepath.Join(repo, "artifact"), budgetPayload(0, 2<<20), 0600); err != nil {
				t.Fatal(err)
			}
			writeFile(t, repo, "note", "original")
			store, err := New(repo, "stable-mixed-pack")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			before := restoreTree(t, store)
			if err := store.RetainTrees(ctx, []string{before}); err != nil {
				t.Fatal(err)
			}
			var trees []string
			var stable string
			var stableInfo os.FileInfo
			for turn := range 6 {
				writeFile(t, repo, "note", fmt.Sprintf("turn %d", turn))
				trees = append(trees, restoreTree(t, store))
				committed, err := store.RetainTreesWithinLimit(ctx, trees[max(0, len(trees)-2):], 4<<20)
				if err != nil || !committed {
					t.Fatalf("turn %d: committed=%v err=%v", turn, committed, err)
				}
				if turn == 0 {
					packs, err := filepath.Glob(filepath.Join(store.gitDir, "objects/pack/*.pack"))
					if err != nil || len(packs) != 2 {
						t.Fatalf("expected separate blob and metadata packs: %v %v", packs, err)
					}
					for _, name := range packs {
						info, err := os.Stat(name)
						if err != nil {
							t.Fatal(err)
						}
						if info.Size() >= reusablePackMinBytes {
							stable, stableInfo = name, info
						}
					}
					if stable == "" {
						t.Fatal("stable blob pack missing")
					}
				}
				info, err := os.Stat(stable)
				if err != nil || !os.SameFile(stableInfo, info) {
					t.Fatalf("turn %d copied or lost the stable blob: %v", turn, err)
				}
			}
			if err := store.Apply(ctx, trees[5], trees[4]); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(repo, "artifact"))
			if err != nil || sha256.Sum256(got) != sha256.Sum256(budgetPayload(0, 2<<20)) || readFile(t, repo, "note") != "turn 4" {
				t.Fatal("split retention lost completed work or a supported restore target")
			}
			if err := store.RetainTrees(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if privateObjectBytes(t, store) != 0 || !reflect.DeepEqual(original, userGitFiles(t, repo)) {
				t.Fatal("empty retention kept objects or changed user Git data")
			}
		})
	}
}

func TestSplitRetention_AdmissionAndFailuresPreserveHistory(t *testing.T) {
	for _, phase := range []string{"budget", "publication", "collection"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "collection" && runtime.GOOS == "windows" {
				t.Skip("fault fixture uses a POSIX shell")
			}
			repo := newTestRepo(t)
			retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
			retentionGit(t, repo, "config", "pack.packSizeLimit", "0")
			for i := range 2 {
				if err := os.WriteFile(filepath.Join(repo, fmt.Sprint(i)), budgetPayload(i, 2<<20), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, repo, "note", "original")
			store, err := New(repo, phase)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			before := restoreTree(t, store)
			if err := store.RetainTrees(ctx, []string{before}); err != nil {
				t.Fatal(err)
			}
			oldRoot, _ := store.CurrentRef(ctx)
			writeFile(t, repo, "note", "changed")
			after := restoreTree(t, store)
			pack, err := store.preparePack(ctx, []string{after}, false)
			if err != nil {
				t.Fatal(err)
			}
			packs, err := filepath.Glob(filepath.Join(pack.dir, "*.pack"))
			if err != nil {
				t.Fatal(err)
			}
			if used, err := pack.bytes(); err != nil || used < 4<<20 || len(packs) != 3 {
				t.Fatalf("all split packs must be charged: count=%d bytes=%d err=%v", len(packs), used, err)
			}
			if err := pack.close(); err != nil {
				t.Fatal(err)
			}
			limit := int64(8 << 20)
			block := filepath.Join(store.gitDir, store.ref+".lock")
			if phase == "budget" {
				limit = 3 << 20
			} else {
				if phase == "collection" {
					realGit, err := exec.LookPath("git")
					if err != nil {
						t.Fatal(err)
					}
					shim := t.TempDir()
					block = filepath.Join(shim, "block-prune")
					body := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = prune ] && [ -e \"$BUCKLEY_TEST_PRUNE_BLOCK\" ]; then echo 'fixture prune failure' >&2; exit 55; fi\ndone\nexec '" + strings.ReplaceAll(realGit, "'", "'\\''") + "' \"$@\"\n"
					if err := os.WriteFile(filepath.Join(shim, "git"), []byte(body), 0755); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
					t.Setenv("BUCKLEY_TEST_PRUNE_BLOCK", block)
				}
				if err := os.WriteFile(block, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			committed, err := store.RetainTreesWithinLimit(ctx, []string{after}, limit)
			if err == nil || committed != (phase == "collection") || phase == "budget" && !errors.Is(err, ErrStorageLimit) {
				t.Fatalf("committed=%v err=%v", committed, err)
			}
			current, _ := store.CurrentRef(ctx)
			if phase == "collection" {
				retentionPrivateGit(t, store, "cat-file", "-e", after)
				used := privateObjectBytes(t, store)
				if _, err := store.WriteTree(ctx); err == nil || privateObjectBytes(t, store) != used {
					t.Fatal("failed collection allowed additional capture writes")
				}
			} else {
				if current != oldRoot {
					t.Fatal("refusal changed the published root")
				}
				retentionPrivateGit(t, store, "cat-file", "-e", before)
			}
			if phase != "budget" {
				if err := os.Remove(block); err != nil {
					t.Fatal(err)
				}
			}
			after = restoreTree(t, store)
			if committed, err := store.RetainTreesWithinLimit(ctx, []string{after}, 8<<20); err != nil || !committed {
				t.Fatalf("recovery: committed=%v err=%v", committed, err)
			}
			if clean, err := store.Clean(ctx, after); err != nil || !clean || readFile(t, repo, "note") != "changed" {
				t.Fatalf("split pack recovery lost completed work: clean=%v err=%v", clean, err)
			}
		})
	}
}
