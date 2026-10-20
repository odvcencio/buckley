package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestListDirectoryTool_PagesPreserveEveryEntry(t *testing.T) {
	dir := t.TempDir()
	want := map[string]os.FileInfo{}
	names := []string{}
	for i := 330; i >= 0; i-- {
		name := fmt.Sprintf("%04d-界,data.txt", i)
		path := filepath.Join(dir, name)
		if i%17 == 0 {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte(fmt.Sprint(i)), 0640); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		want[name] = info
		names = append(names, name)
	}
	slices.Sort(names)
	for _, limit := range []any{1, float64(17), json.Number("1000")} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			tool := &ListDirectoryTool{}
			tool.SetWorkDir(dir)
			got := []string{}
			cursor := ""
			for page := 0; page < len(names); page++ {
				r, err := tool.Execute(map[string]any{"path": ".", "limit": limit, "cursor": cursor})
				if err != nil || !r.Success {
					t.Fatalf("page: %+v %v", r, err)
				}
				files := r.Data["files"].([]map[string]any)
				if len(files) == 0 || r.Data["count"] != len(names) || r.Data["returned"] != len(files) {
					t.Fatalf("incorrect counts: %+v", r.Data)
				}
				for _, file := range files {
					name := file["name"].(string)
					info := want[name]
					if info == nil || file["size"] != info.Size() || file["is_dir"] != info.IsDir() {
						t.Fatalf("metadata changed: %+v", file)
					}
					got = append(got, name)
				}
				next, more := r.Data["next_cursor"].(string)
				if more != (len(got) < len(names)) || (more && (next == "" || next == cursor)) {
					t.Fatalf("incorrect continuation: %q", next)
				}
				if !more {
					break
				}
				cursor = next
			}
			if !slices.Equal(got, names) {
				t.Fatal("paging omitted, duplicated or reordered an entry")
			}
		})
	}
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(dir)
	r, err := tool.Execute(nil)
	if err != nil || !r.Success || r.Data["returned"] != DefaultDirectoryPageEntries {
		t.Fatalf("default page: %+v %v", r, err)
	}
}

func TestListDirectoryTool_ByteBudgetsKeepCompleteRecords(t *testing.T) {
	dir := t.TempDir()
	names := []string{}
	for i := 0; i < 65; i++ {
		name := fmt.Sprintf("%03d-", i) + strings.Repeat("界&", 40) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(dir)
	tool.SetMaxOutputBytes(2048)
	got := []string{}
	cursor := ""
	for page := 0; page < 65; page++ {
		r, err := tool.Execute(map[string]any{"cursor": cursor, "limit": MaxDirectoryPageEntries})
		if err != nil || !r.Success {
			t.Fatalf("page: %+v %v", r, err)
		}
		encoded, err := json.Marshal(r)
		if err != nil || len(encoded) > 2048 {
			t.Fatalf("JSON budget: %d %v", len(encoded), err)
		}
		wire, err := toon.New(true).MarshalString(map[string]any{"success": true, "data": r.Data})
		if err != nil || len(wire) > 2048 {
			t.Fatalf("TOON budget: %d %v", len(wire), err)
		}
		files := r.Data["files"].([]map[string]any)
		if len(files) == 0 {
			t.Fatal("nonterminal empty page")
		}
		for _, file := range files {
			got = append(got, file["name"].(string))
		}
		next, more := r.Data["next_cursor"].(string)
		if !more {
			break
		}
		if next == cursor {
			t.Fatal("cursor stalled")
		}
		cursor = next
	}
	if !slices.Equal(got, names) {
		t.Fatal("byte limiting changed entry identities or order")
	}
	tool.SetMaxOutputBytes(1)
	r, err := tool.Execute(nil)
	if err != nil || r.Success || !strings.Contains(r.Error, "page budget") {
		t.Fatalf("unusable budget: %+v %v", r, err)
	}
}

func TestListDirectoryTool_EmptyAndChangedDirectoriesTerminate(t *testing.T) {
	dir := t.TempDir()
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(dir)
	r, err := tool.Execute(nil)
	if err != nil || !r.Success || r.Data["count"] != 0 || r.Data["returned"] != 0 || r.Data["next_cursor"] != nil {
		t.Fatalf("empty listing: %+v %v", r, err)
	}
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err = tool.Execute(map[string]any{"limit": 1})
	if err != nil || !r.Success {
		t.Fatal(r, err)
	}
	cursor := r.Data["next_cursor"].(string)
	if err := os.Remove(filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	r, err = tool.Execute(map[string]any{"cursor": cursor})
	if err != nil || !r.Success || r.Data["returned"] != 0 || r.Data["count"] != 1 || r.Data["next_cursor"] != nil {
		t.Fatalf("changed directory: %+v %v", r, err)
	}
}

func TestListDirectoryTool_RejectsInvalidSelectors(t *testing.T) {
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(t.TempDir())
	for _, params := range []map[string]any{{"path": true}, {"cursor": 17}, {"cursor": "not a cursor"}, {"cursor": strings.Repeat("a", 4097)}, {"cursor": base64.RawURLEncoding.EncodeToString([]byte("../outside"))}, {"limit": 0}, {"limit": -1}, {"limit": 1001}, {"limit": 1.5}, {"limit": math.NaN()}, {"limit": math.Inf(1)}, {"limit": "2"}, {"limit": json.Number("9223372036854775808")}, {"path": "../outside"}} {
		r, err := tool.Execute(params)
		if err != nil || r.Success || r.Error == "" {
			t.Fatalf("accepted %+v: %+v %v", params, r, err)
		}
	}
	r, err := tool.Execute(map[string]any{"path": nil, "cursor": nil, "limit": nil})
	if err != nil || !r.Success {
		t.Fatalf("nullable defaults: %+v %v", r, err)
	}
}

func TestListDirectoryTool_CanceledContextDoesNotRead(t *testing.T) {
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := tool.ExecuteWithContext(ctx, map[string]any{"path": "missing"})
	if err != nil || r.Success || !strings.Contains(r.Error, "context canceled") {
		t.Fatalf("canceled listing: %+v %v", r, err)
	}
}

func TestListDirectoryTool_SymlinkMetadataDoesNotFollowTarget(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "external")
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(dir)
	r, err := tool.Execute(nil)
	if err != nil || !r.Success {
		t.Fatal(r, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	file := r.Data["files"].([]map[string]any)[0]
	if file["is_dir"] != false || file["size"] != info.Size() {
		t.Fatalf("followed symlink: %+v", file)
	}
}

func TestListDirectoryTool_CursorPreservesNonUTF8Names(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames require Unicode")
	}
	dir := t.TempDir()
	want := []string{"bad-\xff-name", "bad-\xfe-name", "normal"}
	for _, name := range want {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Skipf("filesystem rejects non-UTF8 names: %v", err)
		}
	}
	slices.Sort(want)
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(dir)
	got := []string{}
	cursor := ""
	for range len(want) {
		r, err := tool.Execute(map[string]any{"limit": 1, "cursor": cursor})
		if err != nil || !r.Success {
			t.Fatal(r, err)
		}
		files := r.Data["files"].([]map[string]any)
		if len(files) != 1 {
			t.Fatal("missing non-UTF8 entry")
		}
		got = append(got, files[0]["name"].(string))
		next, more := r.Data["next_cursor"].(string)
		if !more {
			break
		}
		cursor = next
	}
	if !slices.Equal(got, want) {
		t.Fatal("opaque cursor lost filename bytes")
	}
}

func TestListDirectoryTool_UnavailableMetadataKeepsEntryVisible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory search permissions")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "visible.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if _, err := os.Lstat(filepath.Join(dir, "visible.txt")); err == nil {
		t.Skip("process bypasses directory search permissions")
	}
	tool := &ListDirectoryTool{}
	tool.SetWorkDir(dir)
	r, err := tool.Execute(nil)
	if err != nil || !r.Success || r.Data["count"] != 1 || r.Data["returned"] != 1 {
		t.Fatalf("unavailable metadata hid entry: %+v %v", r, err)
	}
	file := r.Data["files"].([]map[string]any)[0]
	if file["name"] != "visible.txt" || file["metadata_error"] == nil || file["size"] != nil || file["is_dir"] != nil {
		t.Fatalf("metadata uncertainty was hidden: %+v", file)
	}
}
