package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool_NumberedView(t *testing.T) {
	tool := &ReadFileTool{}
	if got := tool.Parameters().Properties["line_numbers"].Type; got != "boolean" {
		t.Fatalf("line_numbers type = %q", got)
	}
	for _, tc := range []struct {
		name, content, want string
		start, end          int
	}{
		{name: "short", content: "one\ntwo\n", want: "1: one\n2: two"},
		{name: "blank and unterminated", content: "one\n\nthree", start: 2, end: 3, want: "2: \n3: three"},
		{name: "empty"},
		{name: "blank file", content: "\n", want: "1: "},
		{name: "capped page", content: strings.Repeat("x\n", 210), start: 101, end: 210},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			params := map[string]any{"path": path, "line_numbers": true}
			if tc.start > 0 {
				params["start_line"] = tc.start
				params["end_line"] = tc.end
			}
			result, err := tool.Execute(params)
			if err != nil || !result.Success || !result.ShouldAbridge {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Data["content"] != tc.content {
				t.Fatal("raw content changed")
			}
			got, ok := result.DisplayData["content"].(string)
			if !ok {
				t.Fatal("missing display content")
			}
			if tc.name == "capped page" {
				if !strings.HasPrefix(got, "101: x\n") || !strings.HasSuffix(got, "200: x") || len(strings.Split(got, "\n")) != 100 {
					t.Fatalf("bad numbered page: %q", got)
				}
				page := result.DisplayData["page"].(map[string]any)
				if page["next_start_line"] != 201 || page["has_more"] != true {
					t.Fatalf("bad continuation: %+v", page)
				}
			} else if got != tc.want {
				t.Fatalf("display=%q want=%q", got, tc.want)
			}
			params["line_numbers"] = false
			plain, err := tool.Execute(params)
			if err != nil || !plain.Success || plain.Data["content"] != tc.content {
				t.Fatalf("plain result=%+v err=%v", plain, err)
			}
			if tc.start == 0 && plain.ShouldAbridge {
				t.Fatal("numbering changed default short-file behavior")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{"true", 1, nil} {
		result, err := tool.Execute(map[string]any{"path": path, "line_numbers": value})
		if err != nil || result.Success || !strings.Contains(result.Error, "line_numbers") {
			t.Fatalf("invalid value %v: result=%+v err=%v", value, result, err)
		}
	}
}
