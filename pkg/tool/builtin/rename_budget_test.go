package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenameSymbolTool_FileBudgets(t *testing.T) {
	for _, dry := range []bool{false, true} {
		for _, tc := range []struct {
			name, content, replacement string
			limit                      int64
			success                    bool
		}{
			{"input", strings.Repeat("x", 80) + " OldName", "NewName", 64, false},
			{"growth", "OldName", "LongerNewName", 7, false},
			{"exact", "OldName", "NewName", 7, true},
		} {
			t.Run(fmt.Sprintf("%s/dry=%v", tc.name, dry), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "source.txt")
				if err := os.WriteFile(path, []byte(tc.content), 0640); err != nil {
					t.Fatal(err)
				}
				tool := &RenameSymbolTool{}
				tool.SetWorkDir(dir)
				tool.SetMaxFileSizeBytes(tc.limit)
				result, err := tool.Execute(map[string]any{"old_name": "OldName", "new_name": tc.replacement, "path": ".", "file_pattern": "source.txt", "dry_run": dry})
				if err != nil || result.Success != tc.success {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				want := tc.content
				if tc.success && !dry {
					want = strings.ReplaceAll(want, "OldName", tc.replacement)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("unexpected write: %q %v", got, err)
				}
				if !tc.success {
					rows := result.Data["files_failed"].([]map[string]any)
					if result.Data["failed_count"] != 1 || len(rows) != 1 || !strings.Contains(rows[0]["error"].(string), "too large") {
						t.Fatalf("missing budget failure: %+v", result)
					}
				}
			})
		}
	}
}

func TestRenameSymbolTool_PartialFailureSurvivesAbridging(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 23; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("good-%02d.txt", i)), []byte("OldName"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	bad := filepath.Join(dir, "bad.txt")
	before := strings.Repeat("x", 80) + " OldName"
	if err := os.WriteFile(bad, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	tool := &RenameSymbolTool{}
	tool.SetWorkDir(dir)
	tool.SetMaxFileSizeBytes(64)
	result, err := tool.Execute(map[string]any{"old_name": "OldName", "new_name": "NewName", "path": ".", "file_pattern": "*.txt"})
	if err != nil || result.Success || !result.ShouldAbridge || result.Data["failed_count"] != 1 || result.Data["total_replacements"] != 23 {
		t.Fatalf("partial rename hidden: %+v %v", result, err)
	}
	if len(result.Data["files_modified"].([]map[string]any)) != 23 || len(result.DisplayData["files_modified"].([]map[string]any)) != renamePreviewFiles || result.DisplayData["failed_count"] != 1 || len(result.DisplayData["files_failed"].([]map[string]any)) != 1 || !strings.Contains(result.DisplayData["summary"].(string), "failed") {
		t.Fatalf("display hid partial failure: %+v", result.DisplayData)
	}
	for i := 0; i < 23; i++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("good-%02d.txt", i)))
		if err != nil || string(b) != "NewName" {
			t.Fatal("successful result did not match disk")
		}
	}
	b, err := os.ReadFile(bad)
	if err != nil || string(b) != before {
		t.Fatal("failed file changed")
	}
}

func TestRenameSymbolTool_FailureDetailsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 25; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f-%02d.txt", i)), []byte("OldName"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RenameSymbolTool{}
	tool.SetWorkDir(dir)
	tool.SetMaxFileSizeBytes(1)
	result, err := tool.Execute(map[string]any{"old_name": "OldName", "new_name": "NewName", "path": ".", "file_pattern": "*.txt"})
	if err != nil || result.Success || result.Data["failed_count"] != 25 || len(result.Data["files_failed"].([]map[string]any)) != renamePreviewFiles || result.Data["total_replacements"] != 0 {
		t.Fatalf("unbounded or missing failures: %+v %v", result, err)
	}
}

func TestRenameSymbolTool_ReadOnlyFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions as non-root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte("OldName"), 0440); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0640) })
	tool := &RenameSymbolTool{}
	tool.SetWorkDir(dir)
	result, err := tool.Execute(map[string]any{"old_name": "OldName", "new_name": "NewName", "path": ".", "file_pattern": "source.txt"})
	if err != nil || result.Success || result.Error == "" || result.Data["failed_count"] != 1 || result.Data["total_replacements"] != 0 {
		t.Fatalf("write failure hidden: %+v %v", result, err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "OldName" {
		t.Fatal("failed write changed file")
	}
}

func TestRenameSymbolTool_DiscoveryBudgetAndStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, tc := range []struct {
		name, script string
		limit        int
		success      bool
		hint         string
	}{
		{"exact", "printf 'source.txt\\n'", 11, true, ""},
		{"overflow", "printf 'source.txt\\n'", 10, false, "narrow path"},
		{"stderr isolated", "printf 'source.txt\\n'; printf 'warning\\n' >&2", 64, true, ""},
		{"stderr overflow", "printf 'source.txt\\n'; printf 'too much diagnostic output\\n' >&2", 11, false, "narrow path"},
		{"command error", "printf 'cannot open missingpath\\n' >&2; exit 2", 64, false, "cannot open missingpath"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "rg"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			path := filepath.Join(dir, "source.txt")
			if err := os.WriteFile(path, []byte("OldName"), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &RenameSymbolTool{}
			tool.SetWorkDir(dir)
			tool.SetMaxOutputBytes(tc.limit)
			result, err := tool.Execute(map[string]any{"old_name": "OldName", "new_name": "NewName", "path": "."})
			if err != nil || result.Success != tc.success {
				t.Fatalf("discovery result=%+v err=%v", result, err)
			}
			want := "OldName"
			if tc.success {
				want = "NewName"
			} else if !strings.Contains(result.Error, tc.hint) {
				t.Fatalf("missing recovery guidance: %+v", result)
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != want {
				t.Fatal("discovery failure allowed a partial write")
			}
		})
	}
}
