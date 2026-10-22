package execmode

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestBroker_RecursiveDirectoryPageMatchesWalkDir(t *testing.T) {
	workspace := t.TempDir()
	for _, dir := range []string{"a/child", "a.b", "a/empty", "wide", "z", ".git", "a/vendor", "a/node_modules", "a/.worktrees"} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, dir, "leaf"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(workspace, "a/empty/leaf")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxDirectoryPrefix+maxListEntries+3; i++ {
		if err := os.WriteFile(filepath.Join(workspace, "wide", fmt.Sprintf("file-%06d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	broker, err := NewBroker(workspace, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".", "a", "wide", ".git"} {
		var all []string
		full := filepath.Join(workspace, dir)
		if err := filepath.WalkDir(full, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || path == full {
				return err
			}
			if entry.IsDir() && skipDirName(entry.Name()) {
				return fs.SkipDir
			}
			rel, err := filepath.Rel(workspace, path)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				rel += "/"
			}
			all = append(all, rel)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, cursor := range []int{0, 1, 499, 500, 15000, 16000, 16001, 16500, len(all), len(all) + 1, math.MaxInt} {
			t.Run(fmt.Sprintf("%s/%d", dir, cursor), func(t *testing.T) {
				result, err := broker.filesList(context.Background(), map[string]any{"dir": dir, "recursive": true, "cursor": cursor})
				if err != nil {
					t.Fatal(err)
				}
				start := min(cursor, len(all))
				end := start + min(maxListEntries, len(all)-start)
				want := append([]string(nil), all[start:end]...)
				sort.Strings(want)
				page := result.(map[string]any)
				if !reflect.DeepEqual(page["entries"], want) || page["capped"] != (end < len(all)) || page["next_cursor"] != cursor+len(want) {
					t.Fatalf("page differs from WalkDir at %s offset %d", dir, cursor)
				}
			})
		}
	}
}

func TestBroker_RecursiveDirectoryPageDoesNotFollowSymlinkEntries(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	broker, err := NewBroker(workspace, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := broker.filesList(context.Background(), map[string]any{"recursive": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["entries"]; !reflect.DeepEqual(got, []string{"link"}) {
		t.Fatalf("entries = %v, want symlink without its target", got)
	}
}
