package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSearchTextLiteral(t *testing.T) {
	property := (&SearchTextTool{}).Parameters().Properties["literal"]
	if property.Type != "boolean" || property.Default != false {
		t.Errorf("literal schema = %#v, want boolean default false", property)
	}
	type backend struct {
		name, path string
		err        error
	}
	backends := []backend{}
	for _, name := range []string{"grep", "rg"} {
		path, err := exec.LookPath(name)
		backends = append(backends, backend{name, path, err})
	}
	for _, executable := range backends {
		t.Run(executable.name, func(t *testing.T) {
			if executable.err != nil {
				t.Skipf("%s unavailable: %v", executable.name, executable.err)
			}
			bin := t.TempDir()
			if err := os.Symlink(executable.path, filepath.Join(bin, executable.name)); err != nil {
				t.Skipf("cannot isolate %s backend: %v", executable.name, err)
			}
			t.Setenv("PATH", bin)
			root := t.TempDir()
			files := map[string]string{
				"sample.txt": "func (t *SearchTextTool) Parameters()\na.b\naxb\n[\n--follow\n",
				"code.go":    "before\nMiXeD[(\nafter\n",
				"note.md":    "MiXeD[(\n",
			}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			tool := &SearchTextTool{}
			tool.SetWorkDir(root)
			cases := []struct {
				name      string
				params    map[string]any
				want      []string
				errorText string
			}{
				{"declaration", map[string]any{"query": "func (t *SearchTextTool) Parameters()", "literal": true}, []string{"func (t *SearchTextTool) Parameters()"}, ""},
				{"literal-dot", map[string]any{"query": "a.b", "literal": true}, []string{"a.b"}, ""},
				{"regex-default", map[string]any{"query": "a.b"}, []string{"a.b", "axb"}, ""},
				{"regex-false", map[string]any{"query": "a.b", "literal": false}, []string{"a.b", "axb"}, ""},
				{"literal-bracket", map[string]any{"query": "[", "literal": true}, []string{"["}, ""},
				{"invalid-regex", map[string]any{"query": "["}, nil, "search command failed"},
				{"leading-option", map[string]any{"query": "--follow", "literal": true}, []string{"--follow"}, ""},
				{"missing-literal", map[string]any{"query": "absent[(", "literal": true}, []string{}, ""},
				{"nil-mode", map[string]any{"query": "a.b", "literal": nil}, nil, "literal must be a boolean"},
				{"string-mode", map[string]any{"query": "a.b", "literal": "true"}, nil, "literal must be a boolean"},
				{"numeric-mode", map[string]any{"query": "a.b", "literal": float64(1)}, nil, "literal must be a boolean"},
				{"multiline", map[string]any{"query": "a.b\naxb", "literal": true}, nil, "literal query must be a single line"},
				{"carriage-return", map[string]any{"query": "a.b\r", "literal": true}, nil, "literal query must be a single line"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					tc.params["path"] = "sample.txt"
					result, err := tool.Execute(tc.params)
					if err != nil {
						t.Fatal(err)
					}
					if tc.errorText != "" {
						if result.Success || !strings.Contains(result.Error, tc.errorText) {
							t.Fatalf("expected %q failure, got %#v", tc.errorText, result)
						}
						return
					}
					if !result.Success || result.Data["tool"] != executable.name {
						t.Fatalf("search = %#v, want successful %s dispatch", result, executable.name)
					}
					records := result.Data["matches"].([]map[string]any)
					got := []string{}
					for _, record := range records {
						if filepath.Base(fmt.Sprint(record["path"])) != "sample.txt" || record["kind"] != "match" {
							t.Fatalf("unexpected match identity: %#v", record)
						}
						got = append(got, record["match"].(string))
					}
					if !slices.Equal(got, tc.want) || result.Data["count"] != len(tc.want) || result.Data["records"] != len(tc.want) {
						t.Fatalf("matches=%q count=%v records=%v, want %q", got, result.Data["count"], result.Data["records"], tc.want)
					}
				})
			}
			t.Run("case-glob-context-pages", func(t *testing.T) {
				params := map[string]any{"query": "mixed[(", "path": ".", "literal": true, "case_sensitive": false, "glob": "*.go", "context_before": 1, "context_after": 1, "limit": 1}
				for i, want := range []struct{ kind, content string }{{"context", "before"}, {"match", "MiXeD[("}, {"context", "after"}} {
					params["offset"] = i
					result, err := tool.Execute(params)
					if err != nil {
						t.Fatal(err)
					}
					if !result.Success || result.Data["count"] != 1 || result.Data["records"] != 3 || result.Data["tool"] != executable.name {
						t.Fatalf("page %d = %#v, want one match in three records", i, result)
					}
					records := result.Data["matches"].([]map[string]any)
					if len(records) != 1 {
						t.Fatalf("page %d records=%#v, want one", i, records)
					}
					record := records[0]
					field := "match"
					if want.kind == "context" {
						field = "context"
					}
					if record["kind"] != want.kind || record[field] != want.content || record["line"] != i+1 || filepath.Base(fmt.Sprint(record["path"])) != "code.go" {
						t.Fatalf("page %d record=%#v, want %#v at line %d", i, record, want, i+1)
					}
					next, hasNext := result.Data["next_offset"]
					if (i < 2 && (!hasNext || next != i+1)) || (i == 2 && hasNext) {
						t.Fatalf("page %d next_offset=%#v (present=%t)", i, next, hasNext)
					}
				}
			})
		})
	}
}
