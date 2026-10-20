package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/filewatch"
	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/touch"
)

func TestPermissionMiddleware_LiteralFileTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX whitespace paths")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link ")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{" note.txt", "note.txt ", "   ", "link /missing.txt "} {
		req, ok := derivePermissionRequest("write_file", map[string]any{"path": name}, root, policy.PostureInteractive)
		if !ok || req.Arg != name || req.WorkspaceRelative != !strings.HasPrefix(name, "link ") {
			t.Fatalf("literal request=%+v ok=%v", req, ok)
		}
		called := false
		gate := &PermissionGate{WorkspaceRoot: root, Layers: []policy.PermissionLayer{{Name: "literal", Rules: []policy.PermissionRule{{Tool: "write_file", ArgPattern: name, Action: policy.PermissionDeny}}}}}
		exec := NewPermissionMiddleware(gate)(func(*ExecutionContext) (*builtin.Result, error) {
			called = true
			return &builtin.Result{Success: true}, nil
		})
		r, err := exec(&ExecutionContext{Context: context.Background(), ToolName: "write_file", Params: map[string]any{"path": name}})
		if err != nil || called || r.Success {
			t.Fatalf("literal deny missed: name=%q result=%+v error=%v", name, r, err)
		}
	}
}

func TestFileChangeTracking_LiteralPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX whitespace paths")
	}
	for _, name := range []string{" note.txt", "note.txt ", "\tname.txt\t", "   "} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			watcher := filewatch.NewFileWatcher(10)
			var changes []filewatch.FileChange
			watcher.Subscribe(name, func(c filewatch.FileChange) { changes = append(changes, c) })
			neighborEvents := 0
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				watcher.Subscribe(trimmed, func(filewatch.FileChange) { neighborEvents++ })
			}
			reg := NewEmptyRegistry()
			reg.Register(&builtin.WriteFileTool{})
			reg.SetWorkDir(root)
			reg.Use(FileChangeTracking(watcher))
			t.Cleanup(func() { _ = reg.Close() })
			r, err := reg.Execute("write_file", map[string]any{"path": name, "content": "literal bytes\n"})
			if err != nil || !r.Success || len(changes) != 1 || changes[0].Path != path || changes[0].Size != 14 || neighborEvents != 0 {
				t.Fatalf("literal event: result=%+v error=%v changes=%+v neighbor=%d", r, err, changes, neighborEvents)
			}
			rich := touch.ExtractFromArgsWithOptions("write_file", map[string]any{"path": name, "content": "literal bytes\n"}, touch.ExtractOptions{ResolvePath: func(raw string) (string, error) { return filepath.Join(root, raw), nil }})
			if rich.FilePath != path {
				t.Fatalf("telemetry target changed: %q want %q", rich.FilePath, path)
			}
		})
	}
}
