package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func launchResultData(t *testing.T, result *builtin.Result) map[string]any {
	t.Helper()
	if result == nil {
		t.Fatal("tool returned nil result")
	}
	if !result.Success {
		t.Fatalf("tool failed: %#v", result)
	}
	if result.Data == nil {
		t.Fatal("tool returned nil data")
	}
	return result.Data
}

func TestLaunchFileTools_HappyPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello launch\nneedle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "code.go"), []byte("package nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := newTestLaunchRegistry(t, root, &launchTestSandbox{})

	readResult, err := registry.ExecuteWithContext(context.Background(), "read_file", map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatalf("read_file returned error: %v", err)
	}
	readData := launchResultData(t, readResult)
	if readData["content"] != "hello launch\nneedle here\n" || readData["size"] != 25 {
		t.Fatalf("read_file data = %#v", readData)
	}

	listResult, err := registry.ExecuteWithContext(context.Background(), "list_files", nil)
	if err != nil {
		t.Fatalf("list_files returned error: %v", err)
	}
	listData := launchResultData(t, listResult)
	files, ok := listData["files"].([]string)
	if !ok {
		t.Fatalf("list_files files type = %T, value %#v", listData["files"], listData["files"])
	}
	if !containsString(files, "README.md") || !containsString(files, "nested/code.go") {
		t.Fatalf("list_files = %v, want README.md and nested/code.go", files)
	}

	searchResult, err := registry.ExecuteWithContext(context.Background(), "search_files", map[string]any{"query": "needle"})
	if err != nil {
		t.Fatalf("search_files returned error: %v", err)
	}
	searchData := launchResultData(t, searchResult)
	matches, ok := searchData["matches"].([]map[string]any)
	if !ok {
		t.Fatalf("search_files matches type = %T, value %#v", searchData["matches"], searchData["matches"])
	}
	if len(matches) != 1 || matches[0]["path"] != "README.md" || matches[0]["line"] != 2 {
		t.Fatalf("search_files matches = %#v", matches)
	}

	editResult, err := registry.ExecuteWithContext(context.Background(), "edit_file", map[string]any{
		"path":       "README.md",
		"old_string": "hello launch",
		"new_string": "edited launch",
	})
	if err != nil {
		t.Fatalf("edit_file returned error: %v", err)
	}
	launchResultData(t, editResult)
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "edited launch\nneedle here\n" {
		t.Fatalf("edited content = %q", content)
	}

	writeResult, err := registry.ExecuteWithContext(context.Background(), "write_file", map[string]any{
		"path":    "nested/new.txt",
		"content": "written by launch tool\n",
	})
	if err != nil {
		t.Fatalf("write_file returned error: %v", err)
	}
	writeData := launchResultData(t, writeResult)
	if writeData["path"] != "nested/new.txt" || writeData["size"] != len("written by launch tool\n") {
		t.Fatalf("write_file data = %#v", writeData)
	}
	content, err = os.ReadFile(filepath.Join(root, "nested", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "written by launch tool\n" {
		t.Fatalf("written content = %q", content)
	}
}

func TestLaunchSearch_SkipsLargeBinaryAndStreamsBoundedLargeText(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "gosx-docs")
	binary, err := os.OpenFile(binaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binary.Write([]byte("\x7fELF\x00\x01")); err != nil {
		_ = binary.Close()
		t.Fatal(err)
	}
	if err := binary.Truncate(49 << 20); err != nil {
		_ = binary.Close()
		t.Fatal(err)
	}
	if err := binary.Close(); err != nil {
		t.Fatal(err)
	}
	largeText := strings.Repeat("source map line\n", (6<<20)/16) + "launch-search-needle\n"
	if err := os.WriteFile(filepath.Join(root, "artifact.js.map"), []byte(largeText), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := newTestLaunchRegistry(t, root, &launchTestSandbox{})
	result, err := registry.ExecuteWithContext(context.Background(), "search_files", map[string]any{"query": "launch-search-needle"})
	if err != nil {
		t.Fatalf("search_files returned error: %v", err)
	}
	data := launchResultData(t, result)
	matches, ok := data["matches"].([]map[string]any)
	if !ok || len(matches) != 1 || matches[0]["path"] != "artifact.js.map" {
		t.Fatalf("large-artifact search matches = %#v", data["matches"])
	}
}

func TestLaunchSearch_OversizedTextAtRemainingBudgetIsTruncatedNotFailed(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "large.txt"), []byte(strings.Repeat("text\n", 1<<16)), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	workspace := &launchWorkspace{root: root, maxFileBytes: defaultLaunchFileBytes}
	data, binary, skipped, err := workspace.readForSearch("large.txt", 64<<10)
	if err != nil || binary || !skipped || data != nil {
		t.Fatalf("bounded large text = data:%d binary:%t skipped:%t err:%v", len(data), binary, skipped, err)
	}
}

func TestLaunchWorkspace_DeniesEscapeGitAndSymlinks(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("host sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("git secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinelPath, filepath.Join(root, "link-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link-dir")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"host-home", "buckley-config", "buckley-ledger", "hyphae", "unrelated-repo"} {
		target := filepath.Join(outside, name)
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "sentinel.txt"), []byte(name+" sentinel\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	registry := newTestLaunchRegistry(t, root, &launchTestSandbox{})

	deniedReads := []string{"../outside/sentinel.txt", ".git/config", "link-file", "link-dir/sentinel.txt"}
	for _, name := range []string{"host-home", "buckley-config", "buckley-ledger", "hyphae", "unrelated-repo"} {
		deniedReads = append(deniedReads, name+"/sentinel.txt")
	}
	for _, path := range deniedReads {
		t.Run("read_"+strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			result, err := registry.ExecuteWithContext(context.Background(), "read_file", map[string]any{"path": path})
			if err != nil {
				t.Fatalf("read_file returned Go error: %v", err)
			}
			if result == nil || result.Success {
				t.Fatalf("read_file(%q) = %#v, want denial", path, result)
			}
		})
	}

	for _, path := range []string{"link-file", "link-dir/sentinel.txt"} {
		t.Run("write_"+strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			result, err := registry.ExecuteWithContext(context.Background(), "write_file", map[string]any{"path": path, "content": "must not escape"})
			if err != nil {
				t.Fatalf("write_file returned Go error: %v", err)
			}
			if result == nil || result.Success {
				t.Fatalf("write_file(%q) = %#v, want denial", path, result)
			}
		})
	}
	unchanged, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != "host sentinel\n" {
		t.Fatalf("host sentinel changed to %q", unchanged)
	}

	listResult, err := registry.ExecuteWithContext(context.Background(), "list_files", nil)
	if err != nil {
		t.Fatalf("list_files returned error: %v", err)
	}
	listData := launchResultData(t, listResult)
	files, ok := listData["files"].([]string)
	if !ok {
		t.Fatalf("list_files files type = %T", listData["files"])
	}
	for _, forbidden := range []string{".git/config", "link-file", "link-dir/sentinel.txt"} {
		if containsString(files, forbidden) {
			t.Errorf("list_files exposed %q: %v", forbidden, files)
		}
	}

	searchResult, err := registry.ExecuteWithContext(context.Background(), "search_files", map[string]any{"query": "host sentinel"})
	if err != nil {
		t.Fatalf("search_files returned error: %v", err)
	}
	searchData := launchResultData(t, searchResult)
	if matches, ok := searchData["matches"].([]map[string]any); !ok || len(matches) != 0 {
		t.Fatalf("search_files exposed host sentinel: %#v", searchData["matches"])
	}
}

func TestLaunchWorkspace_DeniesHardlinksBeforeFileOrCommandAccess(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(source, []byte("outside sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked.txt")
	if err := os.Link(source, linked); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}

	for _, name := range []string{"read_file", "list_files", "search_files", "edit_file", "write_file"} {
		t.Run(name, func(t *testing.T) {
			sandbox := &launchTestSandbox{}
			registry := newTestLaunchRegistry(t, root, sandbox)
			params := map[string]any{"path": "linked.txt"}
			switch name {
			case "search_files":
				params = map[string]any{"query": "sentinel"}
			case "edit_file":
				params = map[string]any{"path": "linked.txt", "old_string": "outside", "new_string": "inside"}
			case "write_file":
				params = map[string]any{"path": "linked.txt", "content": "inside"}
			}
			result, err := registry.ExecuteWithContext(context.Background(), name, params)
			if err != nil {
				t.Fatalf("%s returned Go error: %v", name, err)
			}
			if result == nil || result.Success {
				t.Fatalf("%s exposed hardlink: %#v", name, result)
			}
		})
	}

	sandbox := &launchTestSandbox{}
	registry := newTestLaunchRegistry(t, root, sandbox)
	if _, err := registry.ExecuteWithContext(context.Background(), "run_shell", map[string]any{"command": "cat linked.txt"}); !errors.Is(err, ErrLaunchSandboxUnavailable) {
		t.Fatalf("run_shell hardlink boundary error = %v, want unavailable", err)
	}
	sandbox.mu.Lock()
	requests, closes := len(sandbox.requests), sandbox.closeCalls
	sandbox.mu.Unlock()
	if requests != 0 || closes != 1 {
		t.Fatalf("hardlink boundary sandbox requests/closes = %d/%d, want 0/1", requests, closes)
	}
}

func TestLaunchWorkspace_AtomicWriteRejectsHardlinkAddedBeforeRename(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outsideLink := filepath.Join(t.TempDir(), "outside-link.txt")
	workspace := &launchWorkspace{
		root:         root,
		maxFileBytes: maxLaunchFileBytes,
		afterAtomicWriteSync: func(temp string) {
			if linkErr := os.Link(filepath.Join(rootPath, temp), outsideLink); linkErr != nil {
				t.Skipf("hardlinks unavailable: %v", linkErr)
			}
		},
	}
	if err := workspace.write("target.txt", []byte("new content\n"), nil); !errors.Is(err, errLaunchWorkspaceBoundary) {
		t.Fatalf("atomic write error = %v, want workspace boundary", err)
	}
}

func TestLaunchRegistry_AtomicWriteHardlinkRacePoisonsRegistry(t *testing.T) {
	rootPath := t.TempDir()
	sandbox := &launchTestSandbox{}
	registry := newTestLaunchRegistry(t, rootPath, sandbox)
	registered, ok := registry.registry.Get("write_file")
	if !ok {
		t.Fatal("write_file missing")
	}
	serialized, ok := registered.(*serializedLaunchTool)
	if !ok {
		t.Fatalf("write_file type = %T", registered)
	}
	writeTool, ok := serialized.inner.(*launchWriteFileTool)
	if !ok {
		t.Fatalf("inner write_file type = %T", serialized.inner)
	}
	outsideLink := filepath.Join(t.TempDir(), "outside-link.txt")
	writeTool.workspace.afterAtomicWriteSync = func(temp string) {
		if err := os.Link(filepath.Join(rootPath, temp), outsideLink); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
	}
	if _, err := registry.ExecuteWithContext(context.Background(), "write_file", map[string]any{"path": "target.txt", "content": "new content\n"}); !errors.Is(err, ErrLaunchSandboxUnavailable) {
		t.Fatalf("atomic hardlink race error = %v, want unavailable", err)
	}
	if len(registry.List()) != 0 || sandbox.closeCalls != 1 {
		t.Fatalf("registry remained active after boundary race: tools=%v closes=%d", registry.List(), sandbox.closeCalls)
	}
}

type launchBoundaryMutatingSandbox struct {
	source string
	target string
	closed int
}

func (s *launchBoundaryMutatingSandbox) Execute(context.Context, SandboxRequest) (*SandboxResult, error) {
	if err := os.Link(s.source, s.target); err != nil {
		return nil, err
	}
	return &SandboxResult{ExitCode: 0}, nil
}

func (*launchBoundaryMutatingSandbox) Ready(context.Context) error { return nil }
func (s *launchBoundaryMutatingSandbox) Close() error {
	s.closed++
	return nil
}

func TestLaunchWorkspace_PostCommandBoundaryDriftPoisonsRegistry(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(source, []byte("outside sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox := &launchBoundaryMutatingSandbox{source: source, target: filepath.Join(root, "linked.txt")}
	registry := newTestLaunchRegistry(t, root, sandbox)
	if _, err := registry.ExecuteWithContext(context.Background(), "run_tests", map[string]any{"command": "true"}); !errors.Is(err, ErrLaunchSandboxUnavailable) {
		t.Fatalf("post-command hardlink boundary error = %v, want unavailable", err)
	}
	if sandbox.closed != 1 || len(registry.List()) != 0 {
		t.Fatalf("poisoned registry close/list = %d/%v", sandbox.closed, registry.List())
	}
}

func TestLaunchTools_RejectInvalidParameters(t *testing.T) {
	root := t.TempDir()
	registry := newTestLaunchRegistry(t, root, &launchTestSandbox{})
	cases := []struct {
		name   string
		params map[string]any
	}{
		{name: "absolute read path", params: map[string]any{"path": "/etc/passwd"}},
		{name: "empty write path", params: map[string]any{"path": "", "content": "x"}},
		{name: "empty command", params: map[string]any{"command": "   "}},
		{name: "command path escape", params: map[string]any{"command": "pwd", "working_directory": "../outside"}},
		{name: "command timeout", params: map[string]any{"command": "true", "timeout_seconds": 0}},
		{name: "command timeout overflow", params: map[string]any{"command": "true", "timeout_seconds": int64(9_223_372_037)}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var toolName string
			switch {
			case strings.Contains(tt.name, "read"):
				toolName = "read_file"
			case strings.Contains(tt.name, "write"):
				toolName = "write_file"
			default:
				toolName = "run_shell"
			}
			result, err := registry.ExecuteWithContext(context.Background(), toolName, tt.params)
			if err != nil {
				t.Fatalf("%s returned Go error: %v", toolName, err)
			}
			if result == nil || result.Success {
				t.Fatalf("%s(%#v) = %#v, want rejected result", toolName, tt.params, result)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
