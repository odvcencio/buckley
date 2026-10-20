package builtin

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func TestReadSnapshotString_EnforcesActualSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	content := strings.Repeat("\x00\xff界\r\n", 1000)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		hint, limit int64
		exceeded    bool
	}{
		{"exact", int64(len(content)), int64(len(content)), false},
		{"zero hint", 0, int64(len(content)), false},
		{"undersized hint", 1, int64(len(content)), false},
		{"oversized hint", math.MaxInt64, int64(len(content)), false},
		{"overflow safe", 0, math.MaxInt64, false},
		{"one byte over", 0, int64(len(content) - 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { file.Close() })
			got, exceeded, err := readSnapshotString(file, tc.hint, tc.limit)
			if err != nil || exceeded != tc.exceeded {
				t.Fatalf("exceeded=%v err=%v", exceeded, err)
			}
			if tc.exceeded {
				if got != "" {
					t.Fatal("oversized snapshot exposed partial content")
				}
				return
			}
			if got != content {
				t.Fatal("snapshot bytes changed")
			}
		})
	}
}

func TestReadSnapshotString_GrowthAndReadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 33)), 0600); err != nil {
		t.Fatal(err)
	}
	got, exceeded, err := readSnapshotString(file, info.Size(), 32)
	if err != nil || !exceeded || got != "" {
		t.Fatalf("growth escaped limit: length=%d exceeded=%v err=%v", len(got), exceeded, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	got, exceeded, err = readSnapshotString(file, 0, 32)
	if err == nil || exceeded || got != "" {
		t.Fatalf("read error exposed content: length=%d exceeded=%v err=%v", len(got), exceeded, err)
	}
}

func TestReadFileSnapshot_ZeroReportedSize(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux procfs")
	}
	const path = "/proc/version"
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		t.Skip("requires procfs file with zero reported size")
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, size, exceeded, err := readFileSnapshot(path, int64(len(want)))
	if err != nil || exceeded || size != 0 || got != string(want) {
		t.Fatalf("exact virtual-file read: size=%d exceeded=%v err=%v", size, exceeded, err)
	}
	got, _, exceeded, err = readFileSnapshot(path, int64(len(want)-1))
	if err != nil || !exceeded || got != "" {
		t.Fatalf("virtual file escaped limit: length=%d exceeded=%v err=%v", len(got), exceeded, err)
	}
	reader := &ReadFileTool{}
	reader.SetWorkDir("/proc")
	reader.SetMaxFileSizeBytes(int64(len(want) - 1))
	if err := reader.SetSourceScope(&agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "version"}}}); err != nil {
		t.Fatal(err)
	}
	result, err := reader.Execute(map[string]any{"path": "version"})
	if err != nil || result.Success || result.Data["error_kind"] != "file_too_large" || result.Data["content"] != nil {
		t.Fatalf("native scoped read bypassed virtual-file budget: %+v %v", result, err)
	}
}

func TestReadFileTool_ScopedDefaultSnapshotBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	content := strings.Repeat("x\n", defaultReadFileSnapshotBytes/2) + "last\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &ReadFileTool{}
	reader.SetWorkDir(dir)
	if err := reader.SetSourceScope(&agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: 1, EndLine: 2}}}); err != nil {
		t.Fatal(err)
	}
	result, err := reader.Execute(map[string]any{"path": "source.txt"})
	if err != nil || result.Success || result.Data["error_kind"] != "file_too_large" || result.Data["content"] != nil || result.DisplayData != nil || !strings.Contains(result.Error, "8388608") {
		t.Fatalf("default scoped cap failed: success=%v error=%s err=%v", result.Success, result.Error, err)
	}
	reader.SetMaxFileSizeBytes(int64(len(content)))
	result, err = reader.Execute(map[string]any{"path": "source.txt"})
	if err != nil || !result.Success || result.Data["content"] != content || result.DisplayData["content"] != "x\nx" {
		t.Fatalf("explicit cap failed: success=%v error=%s err=%v", result.Success, result.Error, err)
	}
	if err := reader.SetSourceScope(nil); err != nil {
		t.Fatal(err)
	}
	reader.SetMaxFileSizeBytes(0)
	result, err = reader.Execute(map[string]any{"path": "source.txt", "start_line": 1, "end_line": 2})
	if err != nil || !result.Success || result.Data["content_is_page"] != true || result.Data["content"] != "x\nx" {
		t.Fatalf("large-file page fallback failed: success=%v error=%s err=%v", result.Success, result.Error, err)
	}
}
