package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func conflictTestSection(ours, base, theirs string, size int) string {
	text := strings.Repeat("<", size) + " HEAD\n" + ours + "\n"
	if base != "" {
		text += strings.Repeat("|", size) + " base\n" + base + "\n"
	}
	return text + strings.Repeat("=", size) + "\n" + theirs + "\n" + strings.Repeat(">", size) + " side\n"
}

func conflictTestFiles(t *testing.T, r *Result, e error) []map[string]any {
	t.Helper()
	if e != nil || !r.Success {
		t.Fatalf("result=%+v err=%v", r, e)
	}
	return r.Data["files"].([]map[string]any)
}

func TestListMergeConflictsTool_LiteralPathsAndSectionPages(t *testing.T) {
	for _, name := range []string{"config one.txt", "line\nbreak.txt", " trailing ", "--all", ":(glob)*"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && (strings.ContainsAny(name, ":*\n") || strings.TrimSpace(name) != name) {
				t.Skip("filename unsupported on Windows")
			}
			dir := resolutionTestRepo(t, name, "", "diff3")
			var text strings.Builder
			for i := 0; i < 3; i++ {
				text.WriteString(conflictTestSection(fmt.Sprint("ours", i), fmt.Sprint("base", i), fmt.Sprint("theirs", i), 7))
			}
			writeFile(t, filepath.Join(dir, name), text.String())
			tool := &ListMergeConflictsTool{}
			tool.SetWorkDir(dir)
			r, e := tool.Execute(map[string]any{"limit": 1})
			files := conflictTestFiles(t, r, e)
			if r.Data["total_files"] != int64(2) || len(files) != 1 || files[0]["path"] != name || files[0]["conflict_count"] != int64(3) {
				t.Fatalf("bad file page: %+v", r.Data)
			}
			fileCursor := r.Data["next_cursor"].(string)
			for i := 0; i < 3; i++ {
				section := files[0]["conflicts"].([]map[string]any)[0]
				if section["ours"] != fmt.Sprint("ours", i) || section["base"] != fmt.Sprint("base", i) || section["theirs"] != fmt.Sprint("theirs", i) || section["start_line"] != int64(i*7+1) || section["end_line"] != int64(i*7+7) {
					t.Fatalf("bad section: %+v", section)
				}
				if i < 2 {
					r, e = tool.Execute(map[string]any{"cursor": files[0]["next_section_cursor"], "file": "ignored", "limit": -1})
					files = conflictTestFiles(t, r, e)
				}
			}
			if _, more := files[0]["next_section_cursor"]; more {
				t.Fatal("unexpected section continuation")
			}
			r, e = tool.Execute(map[string]any{"cursor": fileCursor, "limit": 1})
			files = conflictTestFiles(t, r, e)
			if len(files) != 1 || files[0]["path"] != "other.txt" || r.Data["next_cursor"] != nil {
				t.Fatalf("bad second file page: %+v", r.Data)
			}
		})
	}
}

func TestListMergeConflictsTool_RejectsChangedIndexSourceAndAttributes(t *testing.T) {
	dir := resolutionTestRepo(t, "config.txt", "", "")
	path := filepath.Join(dir, "config.txt")
	text := conflictTestSection("ours", "base", "theirs", 7) + conflictTestSection("second ours", "second base", "second theirs", 7)
	writeFile(t, path, text)
	tool := &ListMergeConflictsTool{}
	tool.SetWorkDir(dir)
	r, e := tool.Execute(map[string]any{"limit": 1})
	files := conflictTestFiles(t, r, e)
	sectionCursor := files[0]["next_section_cursor"]
	fileCursor := r.Data["next_cursor"]
	writeFile(t, path, strings.Replace(text, "second ours", "new second ours", 1))
	r, e = tool.Execute(map[string]any{"cursor": sectionCursor})
	if e != nil || r.Success || !strings.Contains(r.Error, "source changed") {
		t.Fatalf("source edit accepted: %+v %v", r, e)
	}
	r, e = tool.Execute(map[string]any{"file": "config.txt"})
	files = conflictTestFiles(t, r, e)
	writeFile(t, filepath.Join(dir, ".gitattributes"), "* conflict-marker-size=3\n")
	r, e = tool.Execute(map[string]any{"cursor": files[0]["next_section_cursor"]})
	if e != nil || r.Success || !strings.Contains(r.Error, "source changed") {
		t.Fatalf("attribute edit accepted: %+v %v", r, e)
	}
	writeFile(t, filepath.Join(dir, "other.txt"), "resolved\n")
	statusTestGit(t, dir, "add", "--", "other.txt")
	r, e = tool.Execute(map[string]any{"cursor": fileCursor})
	if e != nil || r.Success || !strings.Contains(r.Error, "index changed") {
		t.Fatalf("index edit accepted: %+v %v", r, e)
	}
}

func TestListMergeConflictsTool_NoMarkersRemainUnmergedAndSubdirScope(t *testing.T) {
	dir := resolutionTestRepo(t, "sub/source.txt", "", "")
	tool := &ListMergeConflictsTool{}
	tool.SetWorkDir(filepath.Join(dir, "sub"))
	r, e := tool.Execute(map[string]any{})
	files := conflictTestFiles(t, r, e)
	if len(files) != 1 || files[0]["path"] != "source.txt" || r.Data["total_files"] != int64(1) {
		t.Fatalf("subdir scope: %+v", r.Data)
	}
	path := filepath.Join(dir, "sub", "source.txt")
	for _, content := range []string{"resolved but unstaged\n", "\x00binary\xffdata"} {
		writeFile(t, path, content)
		r, e = tool.Execute(map[string]any{})
		files = conflictTestFiles(t, r, e)
		if files[0]["unmerged"] != true || files[0]["state"] != "unmerged_without_text_markers" || files[0]["conflict_count"] != int64(0) {
			t.Fatalf("false clean: %+v", r.Data)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r, e = tool.Execute(map[string]any{})
	files = conflictTestFiles(t, r, e)
	if files[0]["kind"] != "missing" || files[0]["unmerged"] != true {
		t.Fatalf("missing unmerged file: %+v", r.Data)
	}
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	r, e = tool.Execute(map[string]any{})
	files = conflictTestFiles(t, r, e)
	if files[0]["kind"] != "directory" || files[0]["unmerged"] != true {
		t.Fatalf("directory unmerged file: %+v", r.Data)
	}
}

func TestListMergeConflictsTool_OutputAndSourceBudgets(t *testing.T) {
	dir := resolutionTestRepo(t, "config.txt", "* conflict-marker-size=3\n", "zdiff3")
	text := conflictTestSection(strings.Repeat("\x00", 3000), strings.Repeat("界", 2000), strings.Repeat("\xff", 3000), 3)
	for _, name := range []string{"config.txt", "other.txt"} {
		writeFile(t, filepath.Join(dir, name), text)
	}
	tool := &ListMergeConflictsTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultConflictOutputBytes, 4096} {
		tool.SetMaxOutputBytes(budget)
		r, e := tool.Execute(map[string]any{"limit": 50})
		files := conflictTestFiles(t, r, e)
		if len(files) < 1 || r.Data["total_files"] != int64(2) {
			t.Fatal(r.Data)
		}
		for _, format := range []bool{true, false} {
			wire, err := toon.New(format).MarshalString(r)
			if err != nil || len(wire) > budget {
				t.Fatalf("encoding=%v bytes=%d error=%v", format, len(wire), err)
			}
		}
		section := files[0]["conflicts"].([]map[string]any)[0]
		if len(section["truncated_sides"].([]string)) != 3 || section["excerpt_encodings"].(map[string]string)["theirs"] != "go_quoted_bytes" {
			t.Fatalf("unmarked truncation/byte encoding: %+v", section)
		}
	}
	tool.SetMaxOutputBytes(20)
	r, e := tool.Execute(map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "output budget") {
		t.Fatalf("output budget: %+v %v", r, e)
	}
	tool.SetMaxOutputBytes(0)
	longLine := strings.Repeat("x", 1<<20)
	writeFile(t, filepath.Join(dir, "config.txt"), conflictTestSection(longLine, "base", "theirs", 3))
	r, e = tool.Execute(map[string]any{"file": "config.txt"})
	files := conflictTestFiles(t, r, e)
	section := files[0]["conflicts"].([]map[string]any)[0]
	if section["ours_bytes"] != int64(len(longLine)) || len(section["ours"].(string)) > 256 {
		t.Fatalf("long-line summary: %+v", section)
	}
	tool.SetMaxFileSizeBytes(20 << 20)
	for _, name := range []string{"config.txt", "other.txt"} {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(20 << 20); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	r, e = tool.Execute(map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "scan budget") {
		t.Fatalf("aggregate source budget: %+v %v", r, e)
	}
}

func TestListMergeConflictsTool_MalformedMarkersAndArguments(t *testing.T) {
	dir := resolutionTestRepo(t, "source.txt", "", "")
	tool := &ListMergeConflictsTool{}
	tool.SetWorkDir(dir)
	for _, content := range []string{"=======\n", "<<<<<<< HEAD\nours\n", "<<<<<<< HEAD\n<<<<<<< nested\n", "<<<<<<< HEAD\n=======\n||||||| base\n"} {
		writeFile(t, filepath.Join(dir, "source.txt"), content)
		r, e := tool.Execute(map[string]any{"file": "source.txt"})
		if e != nil || r.Success {
			t.Fatalf("malformed markers accepted: %+v %v", r, e)
		}
	}
	for _, params := range []map[string]any{{"limit": 0}, {"limit": 51}, {"limit": 1.5}, {"file": false}, {"file": "../escape"}, {"cursor": "bad"}, {"cursor": false}} {
		r, e := tool.Execute(params)
		if e != nil || r.Success || r.Error == "" {
			t.Fatalf("params=%v result=%+v err=%v", params, r, e)
		}
	}
}

func TestListMergeConflictsTool_MalformedIndexScanLimitAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := resolutionTestRepo(t, "source.txt", "", "")
	bin := t.TempDir()
	payload := filepath.Join(t.TempDir(), "index")
	fake := filepath.Join(bin, "git")
	writeFile(t, fake, "#!/bin/sh\n/bin/cat \"$CONFLICT_TEST_INDEX\"\n")
	if err := os.Chmod(fake, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CONFLICT_TEST_INDEX", payload)
	tool := &ListMergeConflictsTool{}
	tool.SetWorkDir(dir)
	record := "100644 " + strings.Repeat("a", 40) + " 1\tsource.txt\x00"
	for _, raw := range []string{"bad\x00", strings.TrimSuffix(record, "\x00"), strings.Replace(record, " 1\t", " 4\t", 1), strings.Repeat("x", 20<<10) + "\x00", strings.Replace(record, "source.txt", "\xff", 1)} {
		writeFile(t, payload, raw)
		r, e := tool.Execute(map[string]any{})
		if e != nil || r.Success {
			t.Fatalf("malformed index accepted: %+v %v", r, e)
		}
	}
	f, err := os.Create(payload)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat(record, 1024)
	for n := 0; n <= DefaultConflictScanBytes; n += len(chunk) {
		if _, err := f.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r, e := tool.Execute(map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "32 MiB scan budget") {
		t.Fatalf("index scan budget: %+v %v", r, e)
	}
	writeFile(t, fake, "#!/bin/sh\n/bin/sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, e = tool.ExecuteWithContext(ctx, map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "deadline") {
		t.Fatalf("stream cancellation: %+v %v", r, e)
	}
}
