package shadowgit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestReusablePacks_RetainsOriginalFilesAndCollectsExpiredSnapshots(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		for _, indexVersion := range []string{"1", "2"} {
			t.Run(format+"/index"+indexVersion, func(t *testing.T) {
				t.Setenv("GIT_DEFAULT_HASH", format)
				repo := newTestRepo(t)
				retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
				retentionGit(t, repo, "config", "pack.indexVersion", indexVersion)
				original := userGitFiles(t, repo)
				store, err := New(repo, "streamed-lifetime")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				ctx := context.Background()
				var trees, blobs []string
				var infos []os.FileInfo
				for turn := range 6 {
					before, err := filepath.Glob(filepath.Join(store.gitDir, "objects/pack/*.pack"))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(repo, "artifact"), budgetPayload(turn, 2<<20), 0600); err != nil {
						t.Fatal(err)
					}
					trees = append(trees, restoreTree(t, store))
					after, err := filepath.Glob(filepath.Join(store.gitDir, "objects/pack/*.pack"))
					if err != nil {
						t.Fatal(err)
					}
					newPacks := 0
					for _, name := range after {
						info, err := os.Stat(name)
						if err != nil {
							t.Fatal(err)
						}
						if info.Size() < 1<<20 {
							continue
						}
						found := false
						for _, old := range before {
							found = found || old == name
						}
						if !found {
							blobs = append(blobs, name)
							infos = append(infos, info)
							newPacks++
						}
					}
					if newPacks != 1 {
						t.Fatalf("turn %d: expected one streamed blob, got %d", turn, newPacks)
					}
					index, err := os.Open(strings.TrimSuffix(blobs[turn], ".pack") + ".idx")
					if err != nil {
						t.Fatal(err)
					}
					var magic [4]byte
					_, err = index.ReadAt(magic[:], 0)
					_ = index.Close()
					if err != nil || (binary.BigEndian.Uint32(magic[:]) == 0xff744f63) != (indexVersion == "2") {
						t.Fatalf("capture did not exercise index version %s: %v", indexVersion, err)
					}
					start := max(0, len(trees)-3)
					committed, err := store.RetainTreesWithinLimit(ctx, trees[start:], 8<<20)
					if err != nil || !committed {
						t.Fatalf("turn %d: committed=%v err=%v", turn, committed, err)
					}
					for i, name := range blobs {
						info, err := os.Stat(name)
						if i < start {
							if !errors.Is(err, os.ErrNotExist) {
								t.Fatalf("expired blob %d was not collected: %v", i, err)
							}
						} else if err != nil || !os.SameFile(infos[i], info) {
							t.Fatalf("retained blob %d was copied or lost: %v", i, err)
						}
					}
					var used int64
					if err := filepath.WalkDir(store.gitDir, func(_ string, entry fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						info, err := entry.Info()
						if err == nil {
							used += allocatedBytes(info)
						}
						return err
					}); err != nil || used > 8<<20 {
						t.Fatalf("turn %d: retained bytes=%d err=%v", turn, used, err)
					}
				}
				if err := store.Apply(ctx, trees[5], trees[3]); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(repo, "artifact"))
				if err != nil || sha256.Sum256(got) != sha256.Sum256(budgetPayload(3, 2<<20)) {
					t.Fatal("retained snapshot did not restore")
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
}

func TestReusablePacks_PublicationAndCollectionFailuresPreserveHistory(t *testing.T) {
	for _, phase := range []string{"publication", "collection"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "collection" && runtime.GOOS == "windows" {
				t.Skip("fault fixture uses a POSIX shell")
			}
			repo := newTestRepo(t)
			retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
			store, err := New(repo, phase)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			for turn := range 2 {
				if err := os.WriteFile(filepath.Join(repo, "artifact"), budgetPayload(turn, 2<<20), 0600); err != nil {
					t.Fatal(err)
				}
				if turn == 0 {
					tree := restoreTree(t, store)
					if err := store.RetainTrees(ctx, []string{tree}); err != nil {
						t.Fatal(err)
					}
				}
			}
			oldRoot, _ := store.CurrentRef(ctx)
			before, err := store.run(ctx, nil, "rev-parse", oldRoot+":0000")
			if err != nil {
				t.Fatal(err)
			}
			after := restoreTree(t, store)
			block := filepath.Join(store.gitDir, store.ref+".lock")
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
			committed, err := store.RetainTreesWithinLimit(ctx, []string{before, after}, 8<<20)
			if err == nil || committed != (phase == "collection") {
				t.Fatalf("committed=%v err=%v", committed, err)
			}
			root, _ := store.CurrentRef(ctx)
			if phase == "publication" && root != oldRoot {
				t.Fatal("failed publication changed the old root")
			}
			retentionPrivateGit(t, store, "cat-file", "-e", before)
			if phase == "collection" {
				retentionPrivateGit(t, store, "cat-file", "-e", after)
				used := privateObjectBytes(t, store)
				if _, err := store.WriteTree(ctx); err == nil || privateObjectBytes(t, store) != used {
					t.Fatal("collection failure allowed more capture writes")
				}
			}
			if err := os.Remove(block); err != nil {
				t.Fatal(err)
			}
			after = restoreTree(t, store)
			if err := store.Apply(ctx, after, before); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(repo, "artifact"))
			if err != nil || sha256.Sum256(got) != sha256.Sum256(budgetPayload(0, 2<<20)) {
				t.Fatal("failure recovery lost the published snapshot")
			}
		})
	}
}

func TestReusablePacks_BoundsCandidatesWithoutDroppingOtherObjects(t *testing.T) {
	repo := newTestRepo(t)
	retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
	for i := range maxReusablePacks + 1 {
		if err := os.WriteFile(filepath.Join(repo, strconv.Itoa(i)), budgetPayload(i, (1<<20)+4096), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := New(repo, "reuse-cap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	tree := restoreTree(t, store)
	kept, err := store.reusablePacks(ctx, []string{tree}, false)
	if err != nil || len(kept) != maxReusablePacks {
		t.Fatalf("candidate limit: kept=%d err=%v", len(kept), err)
	}
	committed, err := store.RetainTreesWithinLimit(ctx, []string{tree}, 96<<20)
	if err != nil || !committed {
		t.Fatalf("committed=%v err=%v", committed, err)
	}
	for i := range maxReusablePacks + 1 {
		got := retentionPrivateGit(t, store, "rev-parse", fmt.Sprintf("%s:%d", tree, i))
		retentionPrivateGit(t, store, "cat-file", "-e", got)
	}
	if clean, err := store.Clean(ctx, tree); err != nil || !clean {
		t.Fatalf("objects outside the candidate cap were lost: clean=%v err=%v", clean, err)
	}
}
