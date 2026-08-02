package main

import (
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// TestBuildToolCallContents_DiffAlwaysCarriesPathAndNewText locks M2: the
// ACP "diff" content variant requires "path" and "newText" verbatim. An
// empty string is a legitimate value (e.g. a delete-all edit produces
// newText ""), so the field must never be dropped via a nil pointer.
func TestBuildToolCallContents_DiffAlwaysCarriesPathAndNewText(t *testing.T) {
	t.Parallel()

	result := &builtin.Result{
		Success: true,
		DiffPreview: &builtin.DiffInfo{
			FilePath:   "/repo/notes.txt",
			IsNew:      false,
			OldContent: "hello world",
			NewContent: "", // delete-all edit
			Preview:    "-hello world",
		},
	}

	contents := buildToolCallContents("", result, "")
	diff := findDiffContent(t, contents)

	data, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := raw["path"]; !ok {
		t.Fatalf("diff content missing required path field: %s", data)
	}
	newText, ok := raw["newText"]
	if !ok {
		t.Fatalf("diff content missing required newText field for a delete-all edit: %s", data)
	}
	if newText != "" {
		t.Fatalf("newText = %v, want empty string for a delete-all edit", newText)
	}
	oldText, ok := raw["oldText"]
	if !ok || oldText != "hello world" {
		t.Fatalf("oldText = %v (present=%v), want %q", oldText, ok, "hello world")
	}
}

// TestBuildToolCallContents_DiffOmitsOldTextForNewFiles asserts oldText is
// nullable and only omitted when the tool reports the file as new.
func TestBuildToolCallContents_DiffOmitsOldTextForNewFiles(t *testing.T) {
	t.Parallel()

	result := &builtin.Result{
		Success: true,
		DiffPreview: &builtin.DiffInfo{
			FilePath:   "/repo/new_file.txt",
			IsNew:      true,
			OldContent: "",
			NewContent: "fresh content",
			Preview:    "+fresh content",
		},
	}

	contents := buildToolCallContents("", result, "")
	diff := findDiffContent(t, contents)

	if diff.OldText != nil {
		t.Fatalf("OldText = %v, want nil for a new file", *diff.OldText)
	}
	if diff.NewText == nil || *diff.NewText != "fresh content" {
		t.Fatalf("NewText = %v, want \"fresh content\"", diff.NewText)
	}
}

// TestBuildToolCallContents_DiffDoesNotTrimOrTruncate asserts the diff
// content matches the file exactly: no whitespace trimming and no 8000-byte
// truncation, even for content that exceeds the byte limit applied to plain
// text output blocks.
func TestBuildToolCallContents_DiffDoesNotTrimOrTruncate(t *testing.T) {
	t.Parallel()

	oldContent := "  leading and trailing whitespace preserved  \n" + strings.Repeat("x", 9000)
	newContent := strings.Repeat("y", 9000) + "  trailing whitespace  "

	result := &builtin.Result{
		Success: true,
		DiffPreview: &builtin.DiffInfo{
			FilePath:   "/repo/big.txt",
			IsNew:      false,
			OldContent: oldContent,
			NewContent: newContent,
			Preview:    "large diff",
		},
	}

	contents := buildToolCallContents("", result, "")
	diff := findDiffContent(t, contents)

	if diff.OldText == nil || *diff.OldText != oldContent {
		t.Fatalf("OldText was trimmed or truncated: got %d bytes, want %d bytes", len(derefOrEmpty(diff.OldText)), len(oldContent))
	}
	if diff.NewText == nil || *diff.NewText != newContent {
		t.Fatalf("NewText was trimmed or truncated: got %d bytes, want %d bytes", len(derefOrEmpty(diff.NewText)), len(newContent))
	}
}

func findDiffContent(t *testing.T, contents []acp.ToolCallContent) acp.ToolCallContent {
	t.Helper()
	for _, c := range contents {
		if c.Type == "diff" {
			return c
		}
	}
	t.Fatalf("no diff content block found among %d entries", len(contents))
	return acp.ToolCallContent{}
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
