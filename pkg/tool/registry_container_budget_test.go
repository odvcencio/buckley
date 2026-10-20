package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestRegistry_ContainerUsesConfiguredOutputBudget(t *testing.T) {
	root := t.TempDir()
	response := `{"success":true,"data":{"output":"` + strings.Repeat("x", 1024) + `"}}`
	script := "#!/bin/sh\nprintf '%s' '" + response + "'\n"
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	registry := NewEmptyRegistry()
	registry.Register(&builtin.WriteFileTool{})
	registry.SetWorkDir(root)
	registry.EnableContainers(filepath.Join(root, "compose.yml"), root)
	registry.SetMaxOutputBytes(64)
	params := map[string]any{"path": "note.txt", "content": "hello"}
	result, err := registry.Execute("write_file", params)
	if err != nil || result == nil || result.Success || result.Data["output_truncated"] != true {
		t.Fatalf("container ignored output budget: %+v %v", result, err)
	}
	registry.SetMaxOutputBytes(2048)
	result, err = registry.Execute("write_file", params)
	if err != nil || result == nil || !result.Success || result.Data["output"] != strings.Repeat("x", 1024) {
		t.Fatalf("container ignored changed output budget: %+v %v", result, err)
	}
}
