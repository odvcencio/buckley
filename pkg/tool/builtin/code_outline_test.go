package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func outlineReply() string {
	return `{"format_version":1,"path":" file.go","language":"go","source_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source_bytes":80,"coverage":{"definition_kinds":["function"],"tree_has_error":false,"query_truncated":false},"total_symbols":1,"offset":0,"symbols":[{"kind":"function","name":"Read","start_line":2,"end_line":4,"depth":0,"parent_index":-1}]}`
}

func TestCodeOutlineTool_LiteralScopeAndStructuredOutput(t *testing.T) {
	root := t.TempDir()
	record := filepath.Join(t.TempDir(), "args")
	if err := os.WriteFile(filepath.Join(root, " file.go"), []byte("package p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	newCanopyStub(t, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$OUTLINE_ARGS\"\ncat <<'REPLY'\n"+outlineReply()+"\nREPLY\n")
	tool := &CodeOutlineTool{}
	tool.SetWorkDir(root)
	tool.SetEnv(map[string]string{"OUTLINE_ARGS": record})
	result, err := tool.Execute(map[string]any{"path": " file.go", "limit": 1000, "cursor": "opaque-cursor"})
	if err != nil || !result.Success {
		t.Fatalf("outline: %+v %v", result, err)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "search\noutline\n--json\n--limit\n100\n--cursor\nopaque-cursor\n--\n file.go\n"
	if string(args) != want {
		t.Fatalf("argv=%q want=%q", args, want)
	}
	if result.Data["language"] != "go" || result.Data["symbols"].([]any)[0].(map[string]any)["name"] != "Read" || result.Data["output"] != nil || result.DisplayData != nil {
		t.Fatalf("lost structured evidence or duplicated payload: %+v", result)
	}
	raw, _ := json.Marshal(result)
	t.Logf("structured_json_bytes=%d symbol=Read start_line=2 end_line=4 literal_path_preserved=true clamped_limit=100 duplicate_output=false", len(raw))
	for _, path := range []string{"../outside.go", filepath.Join(t.TempDir(), "outside.go")} {
		r, err := tool.Execute(map[string]any{"path": path})
		if err != nil || r.Success || !strings.Contains(r.Error, "escapes") {
			t.Fatalf("outside path accepted: %+v %v", r, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.go")); err == nil {
		r, _ := tool.Execute(map[string]any{"path": "escape.go"})
		if r.Success {
			t.Fatal("symlink escape accepted")
		}
	}
}
func TestCodeOutlineTool_ProtocolFailures(t *testing.T) {
	for _, tc := range []struct{ name, script, want string }{
		{"invalid_json", "#!/bin/sh\nprintf 'not json'\n", "invalid outline JSON"},
		{"old_format", "#!/bin/sh\nprintf '{\"format_version\":2,\"symbols\":[]}'\n", "unsupported Canopy outline format"},
		{"missing_rows", "#!/bin/sh\nprintf '{\"format_version\":1}'\n", "missing symbol rows"},
		{"unsupported_cli", "#!/bin/sh\nprintf 'unknown command outline' >&2\nexit 1\n", "update the Canopy CLI"},
		{"oversized", "#!/bin/sh\nhead -c 20000 /dev/zero | tr '\\000' x\n", "exceeded the capture budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newCanopyStub(t, tc.script)
			tool := &CodeOutlineTool{}
			tool.SetWorkDir(t.TempDir())
			result, err := tool.Execute(map[string]any{"path": "source.go"})
			if err != nil || result.Success || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("failure response: %+v %v", result, err)
			}
		})
	}
}
func TestCodeOutlineTool_ContextCancellation(t *testing.T) {
	newCanopyStub(t, "#!/bin/sh\nsleep 60\n")
	tool := &CodeOutlineTool{}
	tool.SetWorkDir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := tool.ExecuteWithContext(ctx, map[string]any{"path": "source.go"})
	if err != nil || result.Success || !strings.Contains(result.Error, "context canceled") {
		t.Fatalf("cancellation lost: %+v %v", result, err)
	}
}
