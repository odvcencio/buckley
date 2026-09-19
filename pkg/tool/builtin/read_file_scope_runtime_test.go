package builtin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func scopedRuntimeReader(t *testing.T, content string, start, end int) (*ReadFileTool, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &ReadFileTool{}
	reader.SetWorkDir(dir)
	if err := reader.SetSourceScope(&agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: start, EndLine: end}}}); err != nil {
		t.Fatal(err)
	}
	return reader, path
}

func TestReadFileScopedRuntimeDefaultAndAnchor(t *testing.T) {
	content := "OUTSIDE needle\nOUTSIDE\n\tallowed needle\r\nsecond\r\nlast\r\nOUTSIDE needle\n"
	for _, anchor := range []bool{false, true} {
		t.Run(fmt.Sprint(anchor), func(t *testing.T) {
			reader, path := scopedRuntimeReader(t, content, 3, 5)
			params := map[string]any{"path": "source.txt"}
			if anchor {
				params["anchor"] = "needle"
			}
			before, _ := json.Marshal(params)
			result, err := reader.Execute(params)
			if err != nil || !result.Success || !result.ShouldAbridge || result.DisplayData["content"] != "\tallowed needle\r\nsecond\r\nlast\r" {
				t.Fatalf("scoped read: %+v, %v", result, err)
			}
			page := result.DisplayData["page"].(map[string]any)
			if page["start_line"] != 3 || page["end_line"] != 5 || page["has_more"] != false || page["next_start_line"] != nil {
				t.Fatalf("scope metadata widened: %#v", page)
			}
			after, _ := json.Marshal(params)
			if string(before) != string(after) || result.Data["content"] != content {
				t.Fatal("caller parameters or host snapshot changed")
			}
			sink := &ArtifactSubmission{}
			if _, err := sink.CaptureReadSource(result); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("changed after capture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			row := sink.RecoveryArtifact().Blocks[0].Table.Rows[0]
			if row[1] != path || row[2] != "3" || row[3] != "5" || row[4] != "\tallowed needle\r\nsecond\r\nlast\r\n" {
				t.Fatalf("captured source was widened or reread: %#v", row)
			}
		})
	}
}

func TestReadFileScopedRuntimeRejectsWidening(t *testing.T) {
	reader, _ := scopedRuntimeReader(t, "OUTSIDE\nOUTSIDE\nallowed\nallowed\nallowed\nOUTSIDE\n", 3, 5)
	for _, selectors := range []map[string]any{
		{"start_line": 2}, {"start_line": 6}, {"end_line": 2}, {"end_line": 999},
		{"start_line": 3, "end_line": 1000}, {"start_line": 4, "end_line": 3},
		{"anchor": "OUTSIDE"}, {"anchor": "allowed", "start_line": 3},
		{"start_line": nil}, {"end_line": 3.5},
	} {
		selectors["path"] = "source.txt"
		result, err := reader.Execute(selectors)
		if err != nil || result.Success || result.Error == "" || len(result.Data) != 0 || len(result.DisplayData) != 0 {
			t.Fatalf("unsafe read exposed content/handles: %+v, %v", result, err)
		}
	}
	commentReader, _ := scopedRuntimeReader(t, "// source excerpt\nfunc Hidden() {}\n", 1, 1)
	result, err := commentReader.Execute(map[string]any{"path": "source.txt", "anchor": "source excerpt"})
	if err != nil || !result.Success || result.DisplayData["content"] != "// source excerpt" || result.Data["page"].(map[string]any)["end_line"] != 1 {
		t.Fatalf("comment anchor widened caller scope: %+v, %v", result, err)
	}
}

func TestReadFileScopedRuntimePaging(t *testing.T) {
	reader, _ := scopedRuntimeReader(t, strings.Repeat("line\n", 300), 50, 205)
	for _, tc := range []struct{ start, end, next int }{{50, 149, 150}, {150, 205, 0}} {
		result, err := reader.Execute(map[string]any{"path": "source.txt", "start_line": tc.start})
		if err != nil || !result.Success {
			t.Fatalf("page: %+v, %v", result, err)
		}
		page := result.DisplayData["page"].(map[string]any)
		if page["start_line"] != tc.start || page["end_line"] != tc.end || page["has_more"] != (tc.next != 0) {
			t.Fatalf("bad scoped page: %#v", page)
		}
		if tc.next == 0 && page["next_start_line"] != nil || tc.next != 0 && page["next_start_line"] != tc.next {
			t.Fatalf("unauthorized continuation: %#v", page)
		}
	}
}

func TestReadFileScopedRuntimeAllowsReturnedPathForNextPage(t *testing.T) {
	reader, _ := scopedRuntimeReader(t, strings.Repeat("line\n", 300), 50, 205)
	first, err := reader.Execute(map[string]any{"path": "source.txt"})
	if err != nil || !first.Success {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	page := first.DisplayData["page"].(map[string]any)
	next, err := reader.Execute(map[string]any{"path": first.DisplayData["path"], "start_line": page["next_start_line"]})
	if err != nil || !next.Success || next.DisplayData["page"].(map[string]any)["end_line"] != 205 {
		t.Fatalf("following the tool-returned path failed or widened scope: %+v, %v", next, err)
	}
}

func TestReadFileScopedRuntimePermissionAndCloning(t *testing.T) {
	reader, path := scopedRuntimeReader(t, "OUTSIDE\nallowed\nOUTSIDE\n", 2, 2)
	scope := &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: 2, EndLine: 2}}}
	if err := reader.SetSourceScope(scope); err != nil {
		t.Fatal(err)
	}
	scope.Files[0].StartLine, scope.Files[0].EndLine = 0, 0
	if err := reader.SetSourceScope(&agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "../outside"}}}); err == nil {
		t.Fatal("invalid scope installed")
	}
	result, err := reader.Execute(map[string]any{"path": "./source.txt"})
	if err != nil || !result.Success || result.DisplayData["content"] != "allowed" {
		t.Fatalf("scope poisoned: %+v, %v", result, err)
	}
	for _, requestPath := range []string{"undeclared.txt", filepath.Join(filepath.Dir(path), "undeclared.txt"), " source.txt", "../outside"} {
		result, err := reader.Execute(map[string]any{"path": requestPath})
		if err != nil || result.Success || len(result.Data) != 0 {
			t.Fatalf("undeclared path admitted: %+v, %v", result, err)
		}
	}
	if err := reader.SetSourceScope(&agentcoord.SourceScope{}); err != nil {
		t.Fatal(err)
	}
	result, _ = reader.Execute(map[string]any{"path": "source.txt"})
	if result.Success {
		t.Fatal("empty scope was unrestricted")
	}
	if err := reader.SetSourceScope(nil); err != nil {
		t.Fatal(err)
	}
	result, err = reader.Execute(map[string]any{"path": path})
	if err != nil || !result.Success || result.Data["content"] != "OUTSIDE\nallowed\nOUTSIDE\n" {
		t.Fatalf("nil-scope legacy changed: %+v, %v", result, err)
	}
}

func TestReadFileScopedRuntimeJailAndLiteralNames(t *testing.T) {
	reader, path := scopedRuntimeReader(t, "selected\n", 0, 0)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("PRIVATE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	result, err := reader.Execute(map[string]any{"path": "source.txt"})
	if err != nil || result.Success || len(result.Data) != 0 || !strings.Contains(result.Error, "symlink") {
		t.Fatalf("symlink escape exposed source: %+v, %v", result, err)
	}
	reader.SetWorkDir("")
	result, _ = reader.Execute(map[string]any{"path": "source.txt"})
	if result.Success {
		t.Fatal("scoped reader worked without a workspace")
	}
	dir := t.TempDir()
	reader.SetWorkDir(dir)
	if err := reader.SetSourceScope(&agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "[literal]!.txt"}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "[literal]!.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = reader.Execute(map[string]any{"path": "[literal]!.txt"})
	if err != nil || !result.Success || !result.ShouldAbridge || result.DisplayData["content"] != "" || result.Data["page"].(map[string]any)["end_line"] != 0 {
		t.Fatalf("literal/empty full-file read: %+v, %v", result, err)
	}
}
