package external

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPlugins_NonexistentDir(t *testing.T) {
	tools, err := DiscoverPlugins("/nonexistent/directory")
	if err != nil {
		t.Errorf("expected no error for nonexistent directory, got: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools list, got %d tools", len(tools))
	}
}

func TestDiscoverPlugins_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	tools, err := DiscoverPlugins(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverPlugins failed: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools list, got %d tools", len(tools))
	}
}

func TestDiscoverPlugins_ValidPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	if err := os.Mkdir(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create manifest
	manifestPath := filepath.Join(pluginDir, "tool.yaml")
	manifest := `
name: test_tool
description: A test tool
executable: ./test.sh
timeout_ms: 30000
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	// Create executable
	scriptPath := filepath.Join(pluginDir, "test.sh")
	script := `#!/bin/bash
echo '{"success": true}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	tools, err := DiscoverPlugins(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverPlugins failed: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	if tools[0].Name() != "test_tool" {
		t.Errorf("expected tool name 'test_tool', got %q", tools[0].Name())
	}
}

func TestDiscoverPlugins_InvalidManifest(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "bad-plugin")
	if err := os.Mkdir(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create invalid manifest
	manifestPath := filepath.Join(pluginDir, "tool.yaml")
	manifest := `
name: test
# Missing required fields
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	// Should skip invalid plugins
	tools, err := DiscoverPlugins(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverPlugins failed: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools list for invalid manifest, got %d tools", len(tools))
	}
}

func TestDiscoverPlugins_MissingExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "missing-exe-plugin")
	if err := os.Mkdir(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create manifest with nonexistent executable
	manifestPath := filepath.Join(pluginDir, "tool.yaml")
	manifest := `
name: test_tool
description: A test tool
executable: ./missing.sh
timeout_ms: 30000
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	// Should skip plugins with missing executables
	tools, err := DiscoverPlugins(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverPlugins failed: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools list for missing executable, got %d tools", len(tools))
	}
}

func TestDiscoverPlugins_NonExecutableFile(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "non-exec-plugin")
	if err := os.Mkdir(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create manifest
	manifestPath := filepath.Join(pluginDir, "tool.yaml")
	manifest := `
name: test_tool
description: A test tool
executable: ./test.sh
timeout_ms: 30000
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	// Create non-executable file
	scriptPath := filepath.Join(pluginDir, "test.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should skip plugins with non-executable files
	tools, err := DiscoverPlugins(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverPlugins failed: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools list for non-executable file, got %d tools", len(tools))
	}
}

func TestDiscoverPlugins_MultiplePlugins(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first plugin
	plugin1Dir := filepath.Join(tmpDir, "plugin1")
	if err := os.Mkdir(plugin1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin1Dir, "tool.yaml"), []byte(`
name: tool1
description: First tool
executable: ./tool1.sh
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin1Dir, "tool1.sh"), []byte("#!/bin/bash\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create second plugin
	plugin2Dir := filepath.Join(tmpDir, "plugin2")
	if err := os.Mkdir(plugin2Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin2Dir, "tool.yaml"), []byte(`
name: tool2
description: Second tool
executable: ./tool2.sh
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin2Dir, "tool2.sh"), []byte("#!/bin/bash\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	tools, err := DiscoverPlugins(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverPlugins failed: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestCheckExecutable_NonExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.sh")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	err := checkExecutable(filePath)
	if err == nil {
		t.Error("expected error for non-executable file")
	}
}

func TestCheckExecutable_Executable(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.sh")
	if err := os.WriteFile(filePath, []byte("#!/bin/bash\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	err := checkExecutable(filePath)
	if err != nil {
		t.Errorf("expected no error for executable file, got: %v", err)
	}
}

func TestCheckExecutable_Nonexistent(t *testing.T) {
	err := checkExecutable("/nonexistent/file")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestDiscoverFromMultipleDirs_Empty(t *testing.T) {
	tools, err := DiscoverFromMultipleDirs([]string{})
	if err != nil {
		t.Fatalf("DiscoverFromMultipleDirs failed: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools list, got %d tools", len(tools))
	}
}

func TestDiscoverFromMultipleDirs_Success(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	// Create plugin in first directory
	plugin1Dir := filepath.Join(tmpDir1, "plugin1")
	if err := os.Mkdir(plugin1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin1Dir, "tool.yaml"), []byte(`
name: tool1
description: First tool
executable: ./tool1.sh
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin1Dir, "tool1.sh"), []byte("#!/bin/bash\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create plugin in second directory
	plugin2Dir := filepath.Join(tmpDir2, "plugin2")
	if err := os.Mkdir(plugin2Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin2Dir, "tool.yaml"), []byte(`
name: tool2
description: Second tool
executable: ./tool2.sh
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin2Dir, "tool2.sh"), []byte("#!/bin/bash\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	tools, err := DiscoverFromMultipleDirs([]string{tmpDir1, tmpDir2})
	if err != nil {
		t.Fatalf("DiscoverFromMultipleDirs failed: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestDiscoverFromMultipleDirs_Deduplication(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	// Create same-named plugin in both directories
	for _, tmpDir := range []string{tmpDir1, tmpDir2} {
		pluginDir := filepath.Join(tmpDir, "plugin")
		if err := os.Mkdir(pluginDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "tool.yaml"), []byte(`
name: duplicate_tool
description: Tool with same name
executable: ./tool.sh
`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "tool.sh"), []byte("#!/bin/bash\necho test"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	tools, err := DiscoverFromMultipleDirs([]string{tmpDir1, tmpDir2})
	if err != nil {
		t.Fatalf("DiscoverFromMultipleDirs failed: %v", err)
	}
	// Should only return one tool (first one wins)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (deduplicated), got %d", len(tools))
	}
	if tools[0].Name() != "duplicate_tool" {
		t.Errorf("expected tool name 'duplicate_tool', got %q", tools[0].Name())
	}
}

func TestDiscoverFromMultipleDirs_NonexistentDirs(t *testing.T) {
	// Should not fail with nonexistent directories
	tools, err := DiscoverFromMultipleDirs([]string{"/nonexistent1", "/nonexistent2"})
	if err != nil {
		t.Fatalf("DiscoverFromMultipleDirs failed: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools list, got %d tools", len(tools))
	}
}
