package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadContainerSpec_NoFile(t *testing.T) {
	tmpDir := t.TempDir()

	spec, err := LoadContainerSpec(tmpDir)
	if err != nil {
		t.Errorf("LoadContainerSpec() unexpected error = %v", err)
	}
	if spec != nil {
		t.Error("LoadContainerSpec() expected nil for non-existent file")
	}
}

func TestLoadContainerSpec_ValidSpec(t *testing.T) {
	tmpDir := t.TempDir()
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	configContent := `driver: docker
name: test-container
base_image: golang:1.22
workdir: /app
mount_workspace: true
mounts:
  - /host/path:/container/path
ports:
  - "8080:8080"
env:
  FOO: bar
  BAZ: "${HOME}"
commands:
  - go mod download
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadContainerSpec(tmpDir)
	if err != nil {
		t.Fatalf("LoadContainerSpec() error = %v", err)
	}
	if spec == nil {
		t.Fatal("LoadContainerSpec() returned nil")
	}

	// Verify fields
	if spec.Driver != "docker" {
		t.Errorf("Driver = %v, want docker", spec.Driver)
	}
	if spec.Name != "test-container" {
		t.Errorf("Name = %v, want test-container", spec.Name)
	}
	if spec.BaseImage != "golang:1.22" {
		t.Errorf("BaseImage = %v, want golang:1.22", spec.BaseImage)
	}
	if spec.Workdir != "/app" {
		t.Errorf("Workdir = %v, want /app", spec.Workdir)
	}
	if spec.MountWorkspace == nil || !*spec.MountWorkspace {
		t.Error("MountWorkspace should be true")
	}
	if len(spec.Mounts) != 1 {
		t.Errorf("Mounts length = %d, want 1", len(spec.Mounts))
	}
	if len(spec.Ports) != 1 {
		t.Errorf("Ports length = %d, want 1", len(spec.Ports))
	}
	if len(spec.Env) != 2 {
		t.Errorf("Env length = %d, want 2", len(spec.Env))
	}
	if spec.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %v, want bar", spec.Env["FOO"])
	}
	if len(spec.Commands) != 1 {
		t.Errorf("Commands length = %d, want 1", len(spec.Commands))
	}
}

func TestLoadContainerSpec_MinimalSpec(t *testing.T) {
	tmpDir := t.TempDir()
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	configContent := `base_image: alpine:latest`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadContainerSpec(tmpDir)
	if err != nil {
		t.Fatalf("LoadContainerSpec() error = %v", err)
	}
	if spec == nil {
		t.Fatal("LoadContainerSpec() returned nil")
	}

	// Verify defaults are applied
	if spec.Driver != "compose" {
		t.Errorf("Driver = %v, want compose (default)", spec.Driver)
	}
	if spec.Workdir != "/workspace" {
		t.Errorf("Workdir = %v, want /workspace (default)", spec.Workdir)
	}
	if spec.Env == nil {
		t.Error("Env should be initialized to empty map")
	}
}

func TestLoadContainerSpec_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	invalidYAML := `driver: docker
name: [invalid yaml structure
`

	if err := os.WriteFile(configPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadContainerSpec(tmpDir)
	if err == nil {
		t.Error("LoadContainerSpec() expected error for invalid YAML")
	}
	if spec != nil {
		t.Error("LoadContainerSpec() should return nil on error")
	}
}

func TestContainerSpec_applyDefaults(t *testing.T) {
	tests := []struct {
		name        string
		input       ContainerSpec
		wantDriver  string
		wantWorkdir string
	}{
		{
			name:        "empty spec applies all defaults",
			input:       ContainerSpec{},
			wantDriver:  "compose",
			wantWorkdir: "/workspace",
		},
		{
			name: "custom values preserved",
			input: ContainerSpec{
				Driver:  "docker",
				Workdir: "/app",
			},
			wantDriver:  "docker",
			wantWorkdir: "/app",
		},
		{
			name: "partial defaults",
			input: ContainerSpec{
				Driver: "docker",
			},
			wantDriver:  "docker",
			wantWorkdir: "/workspace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.input
			spec.applyDefaults()

			if spec.Driver != tt.wantDriver {
				t.Errorf("Driver = %v, want %v", spec.Driver, tt.wantDriver)
			}
			if spec.Workdir != tt.wantWorkdir {
				t.Errorf("Workdir = %v, want %v", spec.Workdir, tt.wantWorkdir)
			}
			if spec.Env == nil {
				t.Error("Env should be initialized")
			}
		})
	}
}

func TestContainerSpec_workspaceMountEnabled(t *testing.T) {
	tests := []struct {
		name           string
		mountWorkspace *bool
		want           bool
	}{
		{
			name:           "nil defaults to true",
			mountWorkspace: nil,
			want:           true,
		},
		{
			name:           "explicit true",
			mountWorkspace: boolPtr(true),
			want:           true,
		},
		{
			name:           "explicit false",
			mountWorkspace: boolPtr(false),
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &ContainerSpec{
				MountWorkspace: tt.mountWorkspace,
			}

			got := spec.workspaceMountEnabled()
			if got != tt.want {
				t.Errorf("workspaceMountEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadContainerSpec_WithComposeFile(t *testing.T) {
	tmpDir := t.TempDir()
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	configContent := `driver: compose
compose_file: docker-compose.dev.yml
workdir: /workspace
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadContainerSpec(tmpDir)
	if err != nil {
		t.Fatalf("LoadContainerSpec() error = %v", err)
	}
	if spec == nil {
		t.Fatal("LoadContainerSpec() returned nil")
	}

	if spec.ComposeFile != "docker-compose.dev.yml" {
		t.Errorf("ComposeFile = %v, want docker-compose.dev.yml", spec.ComposeFile)
	}
}

func TestLoadContainerSpec_WithDevcontainer(t *testing.T) {
	tmpDir := t.TempDir()
	buckleyDir := filepath.Join(tmpDir, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(buckleyDir, "container.yaml")
	configContent := `driver: compose
devcontainer: .devcontainer/devcontainer.json
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadContainerSpec(tmpDir)
	if err != nil {
		t.Fatalf("LoadContainerSpec() error = %v", err)
	}
	if spec == nil {
		t.Fatal("LoadContainerSpec() returned nil")
	}

	if spec.Devcontainer != ".devcontainer/devcontainer.json" {
		t.Errorf("Devcontainer = %v, want .devcontainer/devcontainer.json", spec.Devcontainer)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
