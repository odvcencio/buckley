package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindComposeFile_NoSpec(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a standard docker-compose.yml
	composeFile := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte("version: '3.9'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found, err := FindComposeFile(tmpDir)
	if err != nil {
		t.Fatalf("FindComposeFile() error = %v", err)
	}

	if found != composeFile {
		t.Errorf("FindComposeFile() = %v, want %v", found, composeFile)
	}
}

func TestFindComposeFile_WithSpec(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .buckley/container.yaml with compose_file
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	configContent := `compose_file: custom-compose.yml`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the custom compose file
	customCompose := filepath.Join(tmpDir, "custom-compose.yml")
	if err := os.WriteFile(customCompose, []byte("version: '3.9'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found, err := FindComposeFile(tmpDir)
	if err != nil {
		t.Fatalf("FindComposeFile() error = %v", err)
	}

	if found != customCompose {
		t.Errorf("FindComposeFile() = %v, want %v", found, customCompose)
	}
}

func TestFindComposeFile_WorktreeFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create docker-compose.worktree.yml (priority file)
	worktreeCompose := filepath.Join(tmpDir, "docker-compose.worktree.yml")
	if err := os.WriteFile(worktreeCompose, []byte("version: '3.9'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also create standard docker-compose.yml (should be ignored)
	standardCompose := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(standardCompose, []byte("version: '3.9'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found, err := FindComposeFile(tmpDir)
	if err != nil {
		t.Fatalf("FindComposeFile() error = %v", err)
	}

	// Should prefer worktree file
	if found != worktreeCompose {
		t.Errorf("FindComposeFile() = %v, want %v", found, worktreeCompose)
	}
}

func TestFindComposeFile_ComposeYamlVariants(t *testing.T) {
	tests := []struct {
		name     string
		filename string
	}{
		{
			name:     "docker-compose.yml",
			filename: "docker-compose.yml",
		},
		{
			name:     "compose.yaml",
			filename: "compose.yaml",
		},
		{
			name:     "compose.yml",
			filename: "compose.yml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			composeFile := filepath.Join(tmpDir, tt.filename)
			if err := os.WriteFile(composeFile, []byte("version: '3.9'\n"), 0644); err != nil {
				t.Fatal(err)
			}

			found, err := FindComposeFile(tmpDir)
			if err != nil {
				t.Fatalf("FindComposeFile() error = %v", err)
			}

			if found != composeFile {
				t.Errorf("FindComposeFile() = %v, want %v", found, composeFile)
			}
		})
	}
}

func TestFindComposeFile_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := FindComposeFile(tmpDir)
	if err == nil {
		t.Error("FindComposeFile() expected error for missing compose file")
	}
}

func TestFindComposeFile_EmptyPath(t *testing.T) {
	_, err := FindComposeFile("")
	if err == nil {
		t.Error("FindComposeFile() expected error for empty path")
	}
}

func TestFindComposeFile_Priority(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple compose files
	files := []string{
		"docker-compose.worktree.yml",
		"docker-compose.yml",
		"compose.yaml",
		"compose.yml",
	}

	for _, f := range files {
		path := filepath.Join(tmpDir, f)
		if err := os.WriteFile(path, []byte("version: '3.9'\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	found, err := FindComposeFile(tmpDir)
	if err != nil {
		t.Fatalf("FindComposeFile() error = %v", err)
	}

	// Should find worktree file first
	expected := filepath.Join(tmpDir, "docker-compose.worktree.yml")
	if found != expected {
		t.Errorf("FindComposeFile() = %v, want %v (should prioritize worktree file)", found, expected)
	}
}

func TestFindComposeFile_WithSpecNoFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .buckley/container.yaml but don't specify compose_file
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	configContent := `driver: docker
base_image: golang:1.22
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a fallback compose file
	composeFile := filepath.Join(tmpDir, "docker-compose.worktree.yml")
	if err := os.WriteFile(composeFile, []byte("version: '3.9'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found, err := FindComposeFile(tmpDir)
	if err != nil {
		t.Fatalf("FindComposeFile() error = %v", err)
	}

	if found != composeFile {
		t.Errorf("FindComposeFile() = %v, want %v", found, composeFile)
	}
}

func TestFindComposeFile_SpecWithNonexistentFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .buckley/container.yaml with non-existent compose_file
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	configContent := `compose_file: nonexistent.yml`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create fallback file
	fallbackFile := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(fallbackFile, []byte("version: '3.9'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found, err := FindComposeFile(tmpDir)
	if err != nil {
		t.Fatalf("FindComposeFile() error = %v", err)
	}

	// Should fall back to standard file
	if found != fallbackFile {
		t.Errorf("FindComposeFile() = %v, want %v", found, fallbackFile)
	}
}
