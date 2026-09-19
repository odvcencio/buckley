package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEditFileTool_Batch(t *testing.T) {
	for _, tc := range []struct {
		name, input, args, want string
		count                   int
		preview                 bool
	}{
		{name: "three edits", input: "alpha beta gamma", args: `{"edits":[{"old_string":"alpha","new_string":"A"},{"old_string":"beta","new_string":"B"},{"old_string":"gamma","new_string":"C"}]}`, want: "A B C", count: 3},
		{name: "sequential", input: "alpha", args: `{"edits":[{"old_string":"alpha","new_string":"beta"},{"old_string":"beta","new_string":"gamma"}]}`, want: "gamma", count: 2},
		{name: "replace all then delete", input: "alpha alpha beta", args: `{"edits":[{"old_string":"alpha","new_string":"A","replace_all":true},{"old_string":" beta","new_string":""}]}`, want: "A A", count: 3},
		{name: "combined preview", input: "alpha beta", args: `{"edits":[{"old_string":"alpha","new_string":"A"},{"old_string":"beta","new_string":"B"}]}`, want: "A B", preview: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.txt")
			if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			var params map[string]any
			if err := json.Unmarshal([]byte(tc.args), &params); err != nil {
				t.Fatal(err)
			}
			params["path"] = path
			tool := &EditFileTool{ShowDiffPreview: tc.preview}
			result, err := tool.Execute(params)
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.DiffPreview == nil || result.DiffPreview.OldContent != tc.input || result.DiffPreview.NewContent != tc.want {
				t.Fatalf("wrong combined diff: %+v", result.DiffPreview)
			}
			wantDisk := tc.want
			if tc.preview {
				wantDisk = tc.input
				if !result.NeedsApproval || result.Data["new_content"] != tc.want {
					t.Fatalf("wrong preview: %+v", result)
				}
			} else if result.Data["replacements"] != tc.count {
				t.Fatalf("replacements=%v want=%d", result.Data["replacements"], tc.count)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != wantDisk {
				t.Fatalf("disk=%q err=%v want=%q", content, err, wantDisk)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("permissions changed: %v %v", info, err)
			}
		})
	}
}

func TestEditFileTool_BatchRejectsWithoutWriting(t *testing.T) {
	for _, args := range []string{
		`{"edits":[]}`,
		`{"edits":null}`,
		`{"edits":"invalid"}`,
		`{"edits":{"old_string":"alpha","new_string":"A"}}`,
		`{"edits":[42]}`,
		`{"edits":[{"old_string":"alpha","new_string":"A"},42]}`,
		`{"edits":[{"old_string":"alpha"}]}`,
		`{"edits":[{"new_string":"A"}]}`,
		`{"edits":[{"old_string":42,"new_string":"A"}]}`,
		`{"edits":[{"old_string":"alpha","new_string":null}]}`,
		`{"edits":[{"old_string":"alpha","new_string":"A"}],"old_string":"alpha","new_string":"B"}`,
		`{"edits":[{"old_string":"alpha","new_string":"A"}],"new_string":"B"}`,
		`{"edits":[{"old_string":"alpha","new_string":"A"}],"replace_all":false}`,
		`{"edits":[{"old_string":"alpha","new_string":"A"},{"old_string":"absent","new_string":"B"}]}`,
		`{"edits":[{"old_string":"alpha","new_string":"A"},{"old_string":"beta","new_string":"B"}]}`,
		`{"edits":[{"old_string":"alpha","new_string":"A"},{"old_string":"beta","new_string":42}]}`,
	} {
		t.Run(args, func(t *testing.T) {
			const original = "alpha beta beta"
			path := filepath.Join(t.TempDir(), "input.txt")
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			var params map[string]any
			if err := json.Unmarshal([]byte(args), &params); err != nil {
				t.Fatal(err)
			}
			params["path"] = path
			result, err := (&EditFileTool{}).Execute(params)
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != original {
				t.Fatalf("failed batch changed file: %q %v", content, err)
			}
		})
	}
}

func TestEditFileTool_BatchSchema(t *testing.T) {
	schema := (&EditFileTool{}).Parameters()
	if !reflect.DeepEqual(schema.Required, []string{"path"}) {
		t.Fatalf("required=%v", schema.Required)
	}
	batch := schema.Properties["edits"]
	if batch.Type != "array" || batch.Items == nil || batch.Items.Type != "object" {
		t.Fatalf("batch schema=%+v", batch)
	}
	if !reflect.DeepEqual(batch.Items.Required, []string{"old_string", "new_string"}) {
		t.Fatalf("item required=%v", batch.Items.Required)
	}
	for _, key := range []string{"old_string", "new_string", "replace_all"} {
		if !reflect.DeepEqual(schema.Properties[key], batch.Items.Properties[key]) {
			t.Fatalf("flat and batch %s differ", key)
		}
	}
}
