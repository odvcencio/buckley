package containers

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/envdetect"
	"gopkg.in/yaml.v3"
)

func TestLoadOverrides_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	generator, _ := NewGenerator()
	err := generator.loadOverrides(tmpDir)

	// Should return error when file doesn't exist
	if err == nil {
		t.Error("Expected error when override file doesn't exist")
	}
}

func TestLoadOverrides_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create overrides file
	overridesDir := filepath.Join(tmpDir, ".buckley", "containers")
	os.MkdirAll(overridesDir, 0755)

	overridesContent := `
services:
  dev-go:
    image: custom/go:latest
custom_services:
  custom-service:
    image: nginx:latest
    ports:
      - "8080:80"
`
	overridePath := filepath.Join(overridesDir, "overrides.yaml")
	if err := os.WriteFile(overridePath, []byte(overridesContent), 0644); err != nil {
		t.Fatal(err)
	}

	generator, _ := NewGenerator()
	if err := generator.loadOverrides(tmpDir); err != nil {
		t.Fatalf("loadOverrides() error = %v", err)
	}

	// Verify overrides were loaded
	if len(generator.overrides) == 0 {
		t.Error("Expected overrides to be loaded")
	}
}

func TestApplyOverrides_ServiceOverride(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "docker-compose.yml")

	// Create overrides file
	overridesDir := filepath.Join(tmpDir, ".buckley", "containers")
	os.MkdirAll(overridesDir, 0755)

	overridesContent := `
services:
  dev-go:
    image: custom/go:1.23
    environment:
      CUSTOM_VAR: custom_value
`
	overridePath := filepath.Join(overridesDir, "overrides.yaml")
	os.WriteFile(overridePath, []byte(overridesContent), 0644)

	// Generate compose with overrides
	profile := &envdetect.EnvironmentProfile{
		Languages: []envdetect.Language{
			{Name: "go", Version: "1.22"},
		},
		DetectedAt: time.Now(),
	}

	generator, _ := NewGenerator()
	generator.Generate(profile, outputPath)

	// Parse compose file
	data, _ := os.ReadFile(outputPath)
	var compose ComposeFile
	yaml.Unmarshal(data, &compose)

	// Verify override was applied
	devGo, exists := compose.Services["dev-go"]
	if !exists {
		t.Fatal("dev-go service not found")
	}

	if devGo.Image != "custom/go:1.23" {
		t.Errorf("Override not applied: expected custom/go:1.23, got %s", devGo.Image)
	}

	if devGo.Environment["CUSTOM_VAR"] != "custom_value" {
		t.Error("Environment variable override not applied")
	}
}

func TestApplyOverrides_CustomService(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "docker-compose.yml")

	// Create overrides file
	overridesDir := filepath.Join(tmpDir, ".buckley", "containers")
	os.MkdirAll(overridesDir, 0755)

	overridesContent := `
custom_services:
  nginx:
    image: nginx:latest
    ports:
      - "8080:80"
    networks:
      - buckley
`
	overridePath := filepath.Join(overridesDir, "overrides.yaml")
	os.WriteFile(overridePath, []byte(overridesContent), 0644)

	// Generate compose with overrides
	profile := &envdetect.EnvironmentProfile{
		Languages: []envdetect.Language{
			{Name: "go", Version: "1.22"},
		},
		DetectedAt: time.Now(),
	}

	generator, _ := NewGenerator()
	generator.Generate(profile, outputPath)

	// Parse compose file
	data, _ := os.ReadFile(outputPath)
	var compose ComposeFile
	yaml.Unmarshal(data, &compose)

	// Verify custom service was added
	nginx, exists := compose.Services["nginx"]
	if !exists {
		t.Fatal("Custom nginx service not found")
	}

	if nginx.Image != "nginx:latest" {
		t.Errorf("Expected nginx:latest, got %s", nginx.Image)
	}

	if len(nginx.Ports) != 1 || nginx.Ports[0] != "8080:80" {
		t.Errorf("Expected port 8080:80, got %v", nginx.Ports)
	}
}

func TestMergeService(t *testing.T) {
	service := &Service{
		Image:       "original:1.0",
		Environment: map[string]string{"VAR1": "value1"},
	}

	override := map[string]any{
		"image": "override:2.0",
		"environment": map[string]any{
			"VAR1": "overridden",
			"VAR2": "value2",
		},
		"ports": []any{"8080:80"},
	}

	mergeService(service, override)

	if service.Image != "override:2.0" {
		t.Errorf("Image not overridden: got %s", service.Image)
	}

	if len(service.Ports) != 1 || service.Ports[0] != "8080:80" {
		t.Errorf("Ports not merged correctly: got %v", service.Ports)
	}
}
