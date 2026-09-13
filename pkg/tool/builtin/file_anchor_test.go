package builtin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestReadFileAnchorUsesBoundedPageAndCapturedSnapshot(t *testing.T) {
	for _, numbered := range []bool{false, true} {
		t.Run(fmt.Sprint(numbered), func(t *testing.T) {
			lines := make([]string, 300)
			for i := range lines {
				lines[i] = fmt.Sprintf("line %d\r", i+1)
			}
			lines[150] = "\tfunc Target[T any]() { // unique literal\r"
			content := strings.Join(lines, "\n") + "\n"
			path := filepath.Join(t.TempDir(), "source.go")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			params := map[string]any{"path": path, "anchor": "func Target[T any]()", "line_numbers": numbered}
			before, _ := json.Marshal(params)
			result, err := (&ReadFileTool{}).Execute(params)
			if err != nil || !result.Success || !result.ShouldAbridge {
				t.Fatalf("read=%+v err=%v", result, err)
			}
			after, _ := json.Marshal(params)
			if string(before) != string(after) {
				t.Fatal("reader mutated caller parameters")
			}
			if result.Data["content"] != content {
				t.Fatal("raw file bytes changed")
			}
			page := result.Data["page"].(map[string]any)
			if page["start_line"] != 151 || page["end_line"] != 250 || page["next_start_line"] != 251 || page["has_more"] != true || page["total_lines"] != 300 {
				t.Fatalf("bad page: %+v", page)
			}
			visible := result.DisplayData["content"].(string)
			want := strings.Join(lines[150:250], "\n")
			if numbered {
				if !strings.HasPrefix(visible, "151: "+lines[150]) || !strings.HasSuffix(visible, "250: "+lines[249]) {
					t.Fatal("numbered view lost absolute lines")
				}
			} else if visible != want {
				t.Fatal("display differs from selected source")
			}
			sink := &ArtifactSubmission{}
			ref, err := sink.CaptureReadSource(result)
			if err != nil || ref == "" {
				t.Fatalf("capture: %q %v", ref, err)
			}
			if err := os.WriteFile(path, []byte("changed after read\n"), 0600); err != nil {
				t.Fatal(err)
			}
			a := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusIncomplete, "Observed source", "Only the requested page was observed.")
			a.IncompleteReasons = []string{"Remaining file was not read"}
			if err := sink.SubmitWithSources(a, []string{ref}); err != nil {
				t.Fatal(err)
			}
			got, _ := sink.Artifact()
			row := got.Blocks[0].Table.Rows[0]
			if row[2] != "151" || row[3] != "250" || row[4] != want+"\n" || got.Status != artifactv1.StatusIncomplete {
				t.Fatalf("snapshot/range lost: %+v", got)
			}
			next, err := (&ReadFileTool{}).Execute(map[string]any{"path": path, "start_line": 1})
			if err != nil || !next.Success || next.Data["content"] != "changed after read\n" {
				t.Fatal("reader cached stale file")
			}
		})
	}
}

func TestReadFileAnchorLiteralSelection(t *testing.T) {
	for _, tc := range []struct {
		name, content, anchor string
		line                  int
	}{
		{"last line", "before\nlast", "last", 2},
		{"first line", "needle\nother\n", "needle", 1},
		{"same line twice", "before\nneedle needle\n", "needle", 2},
		{"literal metacharacters", "lookalike\nvalue [x].*()$\n", "[x].*()$", 2},
		{"case sensitive", "Target\ntarget\n", "target", 2},
		{"literal whitespace", "needle\n needle \n", " needle ", 2},
		{"unicode", "before\n函数 λ\n", "函数 λ", 2},
		{"maximum length", "before\n" + strings.Repeat("x", 256) + "\n", strings.Repeat("x", 256), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := (&ReadFileTool{}).Execute(map[string]any{"path": path, "anchor": tc.anchor})
			if err != nil || !result.Success {
				t.Fatalf("read=%+v err=%v", result, err)
			}
			if result.Data["page"].(map[string]any)["start_line"] != tc.line {
				t.Fatalf("wrong line: %+v", result.Data["page"])
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || string(unchanged) != tc.content {
				t.Fatal("read changed source")
			}
		})
	}
	if got := (&ReadFileTool{}).Parameters().Properties["anchor"].Type; got != "string" {
		t.Fatalf("anchor schema=%q", got)
	}
}

func TestReadFileAnchorRejectsInvalidAndAmbiguousSelectors(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		params        map[string]any
		want          string
	}{
		{"missing", "source\n", map[string]any{"anchor": "absent"}, "anchor"},
		{"empty file", "", map[string]any{"anchor": "absent"}, "anchor"},
		{"empty", "source", map[string]any{"anchor": ""}, "anchor"},
		{"blank", "source", map[string]any{"anchor": " \t"}, "anchor"},
		{"null", "source", map[string]any{"anchor": nil}, "anchor"},
		{"number", "source", map[string]any{"anchor": 1}, "anchor"},
		{"too long", "source", map[string]any{"anchor": strings.Repeat("x", 257)}, "anchor"},
		{"multiline", "one\ntwo", map[string]any{"anchor": "one\ntwo"}, "anchor"},
		{"CR", "source\r\n", map[string]any{"anchor": "source\r"}, "anchor"},
		{"start conflict", "source", map[string]any{"anchor": "source", "start_line": 1}, "start_line"},
		{"null start conflict", "source", map[string]any{"anchor": "source", "start_line": nil}, "start_line"},
		{"end conflict", "source", map[string]any{"anchor": "source", "end_line": 1}, "end_line"},
		{"ambiguous", "needle\nother\nneedle\n", map[string]any{"anchor": "needle"}, "anchor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			tc.params["path"] = path
			before, _ := json.Marshal(tc.params)
			result, err := (&ReadFileTool{}).Execute(tc.params)
			if err != nil || result.Success || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if len(result.Data) != 0 || len(result.DisplayData) != 0 {
				t.Fatal("failed selector returned source as successful evidence")
			}
			after, _ := json.Marshal(tc.params)
			if string(before) != string(after) {
				t.Fatal("failed read mutated params")
			}
			if _, err := (&ArtifactSubmission{}).CaptureReadSource(result); err == nil {
				t.Fatal("failed selector became capture")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "many.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("needle\n", 1000)), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (&ReadFileTool{}).Execute(map[string]any{"path": path, "anchor": "needle"})
	if err != nil || result.Success || !strings.Contains(result.Error, "1000") || !strings.Contains(result.Error, "start_line") || len(result.Error) > 512 {
		t.Fatalf("unbounded or unhelpful ambiguity: %+v, %v", result, err)
	}
}

func TestReadFileAnchorDoesNotChangeUnanchoredReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := &ReadFileTool{}
	ordinary, _ := tool.Execute(map[string]any{"path": path, "start_line": 2, "end_line": 3})
	anchored, _ := tool.Execute(map[string]any{"path": path, "anchor": "two"})
	if !reflect.DeepEqual(ordinary, anchored) {
		t.Fatalf("anchor diverged from existing page: ordinary=%+v anchored=%+v", ordinary, anchored)
	}
}
