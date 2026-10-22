package execmode

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBrokerSearchText_WalkOrderAndGlob(t *testing.T) {
	workspace := t.TempDir()
	// All matches are after a full inventory window in the wide directory.
	for _, dir := range []string{"a/child", "a.b", "wide", "z", ".git", "a/vendor", "a/node_modules", "a/.worktrees"} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, dir, "target.txt"), []byte("ordinary\nneedle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < maxDirectoryPrefix+3; i++ {
		if err := os.WriteFile(filepath.Join(workspace, "wide", fmt.Sprintf("file-%06d.bin", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "z", "target.go"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(workspace, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	type match struct {
		File string
		Line int
		Text string
	}
	for _, glob := range []string{"", "*.txt", "target.?o", "[tz]*.txt", "*.missing"} {
		t.Run(glob, func(t *testing.T) {
			var want []match
			err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if skipDirName(entry.Name()) {
						return fs.SkipDir
					}
					return nil
				}
				if glob != "" {
					if ok, _ := filepath.Match(glob, entry.Name()); !ok {
						return nil
					}
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(workspace, path)
				if err != nil {
					return err
				}
				for i, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, "needle") {
						want = append(want, match{rel, i + 1, line})
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := broker.searchText(context.Background(), map[string]any{"pattern": "needle", "glob": glob})
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Matches []match
				Capped  bool
			}
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if got.Capped || !reflect.DeepEqual(got.Matches, want) {
				t.Fatalf("glob %q matches=%+v want=%+v capped=%t", glob, got.Matches, want, got.Capped)
			}
		})
	}
}
