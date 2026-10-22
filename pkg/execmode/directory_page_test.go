package execmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestBrokerFilesList_FlatPagesPreserveOrder(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < 700; i++ {
		if err := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("item%04d.txt", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a", "a.b", "item0497", "é"} {
		if err := os.Mkdir(filepath.Join(workspace, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	broker, err := NewBroker(workspace, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []int{0, 499, 500, 501, 703, 704, 705, math.MaxInt} {
		t.Run(fmt.Sprint(cursor), func(t *testing.T) {
			entries, err := os.ReadDir(workspace)
			if err != nil {
				t.Fatal(err)
			}
			start := min(cursor, len(entries))
			end := min(start+maxListEntries, len(entries))
			var want []string
			for _, entry := range entries[start:end] {
				name := entry.Name()
				if entry.IsDir() {
					name += "/"
				}
				want = append(want, name)
			}
			sort.Strings(want)
			result, err := broker.filesList(context.Background(), map[string]any{"cursor": cursor})
			if err != nil {
				t.Fatal(err)
			}
			page := result.(map[string]any)
			if !reflect.DeepEqual(page["entries"], want) || page["capped"] != (end < len(entries)) || page["next_cursor"] != end {
				t.Fatalf("cursor=%d page bounds/order differ from complete directory ordering", cursor)
			}
		})
	}
	first, err := broker.filesList(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "000-new"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := broker.filesList(context.Background(), map[string]any{})
	if err != nil || second.(map[string]any)["entries"].([]string)[0] != "000-new" {
		t.Fatalf("listing did not read the edited directory: %v", err)
	}
	after, err := json.Marshal(first)
	if err != nil || string(before) != string(after) {
		t.Fatal("later directory reads mutated an earlier page")
	}
}

func TestDirectorySelection_ByteRanksAndEdges(t *testing.T) {
	workspace := t.TempDir()
	names := []string{"a", "aa", "a.a", "a0", "x", "z", "é", "é0", "界", "界a"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(workspace, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(names)
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for rank, want := range names {
		got, err := directoryNameAtRank(context.Background(), root, ".", rank)
		if err != nil || got != want {
			t.Fatalf("rank=%d name=%q want=%q error=%v", rank, got, want, err)
		}
	}
	for _, largest := range []bool{false, true} {
		selected, total, err := selectDirectoryNames(context.Background(), root, ".", "", 3, largest, "")
		if err != nil || total != len(names) || len(selected) != 3 {
			t.Fatalf("selection length/count: %d %d %v", len(selected), total, err)
		}
		want := names[:3]
		if largest {
			want = names[len(names)-3:]
		}
		for i, entry := range selected {
			if entry.name != want[i] || entry.mode.IsDir() {
				t.Fatalf("edge selection: %+v want=%v", selected, want)
			}
		}
	}
	selected, total, err := selectDirectoryNames(context.Background(), root, ".", names[4], 3, false, "")
	if err != nil || total != len(names) || len(selected) != 3 || selected[0].name != names[5] {
		t.Fatalf("continuation lost total count or ordering: %+v %d %v", selected, total, err)
	}
	if _, err := directoryNameAtRank(context.Background(), root, ".", len(names)); err == nil {
		t.Fatal("out-of-range rank did not report a changed directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanDirectory(ctx, root, "missing", func([]os.DirEntry) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan opened the directory: %v", err)
	}
	if err := scanDirectory(context.Background(), root, "..", func([]os.DirEntry) {}); err == nil {
		t.Fatal("directory scan escaped its opened root")
	}
}
