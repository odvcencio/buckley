package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRoots_DeniedPathsOverrideGrants(t *testing.T) {
	type configuredReader interface {
		Name() string
		Execute(map[string]any) (*Result, error)
		SetWorkDir(string)
		SetReadRoots([]string)
		SetDeniedReadPaths([]string)
	}

	work, root := readRootFixture(t)
	denied := filepath.Join(root, "spaces", "s")
	spore := filepath.Join(denied, "inbox", "spore.md")
	allowed := filepath.Join(root, "allowed.md")
	if err := os.WriteFile(allowed, []byte("Allowed readable marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		tool   configuredReader
		params map[string]any
	}{
		{"read", &ReadFileTool{}, map[string]any{"path": spore}},
		{"list", &ListDirectoryTool{}, map[string]any{"path": denied}},
		{"find", &FindFilesTool{}, map[string]any{"base_path": denied, "pattern": "**/*"}},
		{"search", &SearchTextTool{}, map[string]any{"path": denied, "query": "."}},
		{"exists", &FileExistsTool{}, map[string]any{"path": spore}},
		{"outline", &CodeOutlineTool{}, map[string]any{"path": spore}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configured := tc.tool
			configured.SetWorkDir(work)
			configured.SetReadRoots([]string{root})
			configured.SetDeniedReadPaths([]string{denied})
			result, err := tc.tool.Execute(tc.params)
			if err != nil || result == nil || result.Success || !strings.Contains(result.Error, "denied") {
				t.Fatalf("denied source accepted: result=%+v error=%v", result, err)
			}
		})
	}

	reader := &ReadFileTool{}
	reader.SetWorkDir(work)
	reader.SetReadRoots([]string{root})
	reader.SetDeniedReadPaths([]string{denied})
	if result, _ := reader.Execute(map[string]any{"path": allowed}); result == nil || !result.Success {
		t.Fatalf("unrelated allowed source was blocked: %+v", result)
	}
	alias := filepath.Join(root, "alias.md")
	if err := os.Symlink(spore, alias); err != nil {
		t.Fatal(err)
	}
	if result, _ := reader.Execute(map[string]any{"path": alias}); result == nil || result.Success || !strings.Contains(result.Error, "denied") {
		t.Fatalf("symlink bypassed denied source: %+v", result)
	}
	reader.SetDeniedReadPaths(nil)
	if result, _ := reader.Execute(map[string]any{"path": spore}); result == nil || !result.Success {
		t.Fatalf("cleared denial remained active: %+v", result)
	}

	for _, candidate := range []configuredReader{&ListDirectoryTool{}, &FindFilesTool{}, &SearchTextTool{}} {
		configured := candidate
		configured.SetWorkDir(work)
		configured.SetReadRoots([]string{root})
		configured.SetDeniedReadPaths([]string{denied})
		params := map[string]any{"path": root, "base_path": root, "pattern": "**/*", "query": "."}
		result, err := candidate.Execute(params)
		if err != nil || result == nil || !result.Success {
			t.Fatalf("%s failed to inspect allowed parent: %+v %v", candidate.Name(), result, err)
		}
		text := readRootText(result.Data)
		if strings.Contains(text, "spore.md") || strings.Contains(text, "Spore title") || strings.Contains(text, "alias.md") {
			t.Errorf("%s exposed a denied descendant: %s", candidate.Name(), text)
		}
	}
}

func TestReadRoots_DenialsApplyWithoutAdditionalRoots(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "restricted.md")
	if err := os.WriteFile(path, []byte("private source"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := &ReadFileTool{}
	reader.SetWorkDir(work)
	reader.SetDeniedReadPaths([]string{"restricted.md"})
	if result, _ := reader.Execute(map[string]any{"path": path}); result == nil || result.Success || !strings.Contains(result.Error, "denied") {
		t.Fatalf("workspace-relative denial ignored: %+v", result)
	}
	t.Setenv("HOME", work)
	reader.SetDeniedReadPaths([]string{"~/restricted.md"})
	if result, _ := reader.Execute(map[string]any{"path": path}); result == nil || result.Success {
		t.Fatalf("home-relative denial ignored: %+v", result)
	}
}
