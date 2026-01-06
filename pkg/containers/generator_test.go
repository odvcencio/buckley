package containers

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/envdetect"
	"gopkg.in/yaml.v3"
)

func TestGenerator_GenerateGoProject(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "docker-compose.yml")

	profile := &envdetect.EnvironmentProfile{
		Languages: []envdetect.Language{
			{
				Name:    "go",
				Version: "1.22",
			},
		},
		DetectedAt: time.Now(),
	}

	generator, err := NewGenerator()
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}

	if err := generator.Generate(profile, outputPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("Compose file was not created")
	}

	// Parse and verify compose file
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}

	var compose ComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatalf("Failed to parse compose file: %v", err)
	}

	// Verify version
	if compose.Version != "3.9" {
		t.Errorf("Expected version 3.9, got %s", compose.Version)
	}

	// Verify dev-go service exists
	if _, exists := compose.Services["dev-go"]; !exists {
		t.Fatal("dev-go service not found")
	}

	devGo := compose.Services["dev-go"]
	if devGo.Image != "buckley/go:1.22" {
		t.Errorf("Expected image buckley/go:1.22, got %s", devGo.Image)
	}

	if devGo.WorkingDir != "/workspace" {
		t.Errorf("Expected working_dir /workspace, got %s", devGo.WorkingDir)
	}

	// Verify network
	if _, exists := compose.Networks["buckley"]; !exists {
		t.Error("buckley network not found")
	}

	// Verify cache volume
	if _, exists := compose.Volumes["go-cache"]; !exists {
		t.Error("go-cache volume not found")
	}
}

func TestGenerator_GenerateWithPostgres(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "docker-compose.yml")

	profile := &envdetect.EnvironmentProfile{
		Languages: []envdetect.Language{
			{Name: "go", Version: "1.22"},
		},
		Services: []envdetect.Service{
			{
				Type:    "postgres",
				Version: "16",
				Ports:   []envdetect.Port{{Host: 5432, Container: 5432, Protocol: "tcp"}},
				Env: map[string]string{
					"POSTGRES_USER": "test_user",
					"POSTGRES_DB":   "test_db",
				},
				Volumes: []envdetect.Volume{
					{Name: "postgres_data", Path: "/var/lib/postgresql/data", Type: "named"},
				},
			},
		},
		DetectedAt: time.Now(),
	}

	generator, _ := NewGenerator()
	if err := generator.Generate(profile, outputPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Parse compose file
	data, _ := os.ReadFile(outputPath)
	var compose ComposeFile
	yaml.Unmarshal(data, &compose)

	// Verify postgres service
	if _, exists := compose.Services["postgres"]; !exists {
		t.Fatal("postgres service not found")
	}

	pg := compose.Services["postgres"]
	if pg.Image != "postgres:16" {
		t.Errorf("Expected image postgres:16, got %s", pg.Image)
	}

	if len(pg.Ports) != 1 || pg.Ports[0] != "5432:5432" {
		t.Errorf("Expected port 5432:5432, got %v", pg.Ports)
	}

	if pg.Environment["POSTGRES_USER"] != "test_user" {
		t.Errorf("Expected POSTGRES_USER=test_user, got %s", pg.Environment["POSTGRES_USER"])
	}

	// Verify healthcheck
	if pg.Healthcheck == nil {
		t.Fatal("Expected healthcheck for postgres")
	}

	if len(pg.Healthcheck.Test) == 0 {
		t.Error("Expected healthcheck test command")
	}

	// Verify volume
	if _, exists := compose.Volumes["postgres_data"]; !exists {
		t.Error("postgres_data volume not found")
	}
}

func TestGenerator_GenerateMultiLanguage(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "docker-compose.yml")

	profile := &envdetect.EnvironmentProfile{
		Languages: []envdetect.Language{
			{Name: "go", Version: "1.22"},
			{Name: "node", Version: "20"},
		},
		DetectedAt: time.Now(),
	}

	generator, _ := NewGenerator()
	if err := generator.Generate(profile, outputPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Parse compose file
	data, _ := os.ReadFile(outputPath)
	var compose ComposeFile
	yaml.Unmarshal(data, &compose)

	// Verify both dev services exist
	if _, exists := compose.Services["dev-go"]; !exists {
		t.Error("dev-go service not found")
	}

	if _, exists := compose.Services["dev-node"]; !exists {
		t.Error("dev-node service not found")
	}

	// Verify both cache volumes exist
	if _, exists := compose.Volumes["go-cache"]; !exists {
		t.Error("go-cache volume not found")
	}

	if _, exists := compose.Volumes["node-cache"]; !exists {
		t.Error("node-cache volume not found")
	}
}

func TestGenerator_GenerateWithRedis(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "docker-compose.yml")

	profile := &envdetect.EnvironmentProfile{
		Languages: []envdetect.Language{
			{Name: "go", Version: "1.22"},
		},
		Services: []envdetect.Service{
			{
				Type:    "redis",
				Version: "7",
				Ports:   []envdetect.Port{{Host: 6379, Container: 6379, Protocol: "tcp"}},
			},
		},
		DetectedAt: time.Now(),
	}

	generator, _ := NewGenerator()
	generator.Generate(profile, outputPath)

	// Parse compose file
	data, _ := os.ReadFile(outputPath)
	var compose ComposeFile
	yaml.Unmarshal(data, &compose)

	// Verify redis service
	redis, exists := compose.Services["redis"]
	if !exists {
		t.Fatal("redis service not found")
	}

	if redis.Image != "redis:7" {
		t.Errorf("Expected image redis:7, got %s", redis.Image)
	}

	// Verify redis healthcheck
	if redis.Healthcheck == nil {
		t.Fatal("Expected healthcheck for redis")
	}

	if redis.Healthcheck.Test[0] != "CMD" || redis.Healthcheck.Test[1] != "redis-cli" {
		t.Errorf("Unexpected healthcheck test: %v", redis.Healthcheck.Test)
	}
}

func TestGenerator_EmptyProfile(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "docker-compose.yml")

	profile := &envdetect.EnvironmentProfile{
		Languages:  []envdetect.Language{},
		Services:   []envdetect.Service{},
		DetectedAt: time.Now(),
	}

	generator, _ := NewGenerator()
	if err := generator.Generate(profile, outputPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Parse compose file
	data, _ := os.ReadFile(outputPath)
	var compose ComposeFile
	yaml.Unmarshal(data, &compose)

	// Should have network but no services
	if len(compose.Services) != 0 {
		t.Errorf("Expected 0 services, got %d", len(compose.Services))
	}

	if _, exists := compose.Networks["buckley"]; !exists {
		t.Error("buckley network should still exist")
	}
}

func TestGenerateDevService(t *testing.T) {
	generator, _ := NewGenerator()

	lang := envdetect.Language{
		Name:    "go",
		Version: "1.22",
	}

	service := generator.generateDevService(lang)

	if service.Image != "buckley/go:1.22" {
		t.Errorf("Expected image buckley/go:1.22, got %s", service.Image)
	}

	if service.ContainerName != "buckley-dev-go" {
		t.Errorf("Expected container_name buckley-dev-go, got %s", service.ContainerName)
	}

	if service.WorkingDir != "/workspace" {
		t.Errorf("Expected working_dir /workspace, got %s", service.WorkingDir)
	}

	if len(service.Volumes) < 2 {
		t.Error("Expected at least 2 volume mounts")
	}

	if service.Restart != "unless-stopped" {
		t.Errorf("Expected restart unless-stopped, got %s", service.Restart)
	}

	if service.Healthcheck == nil {
		t.Error("Expected healthcheck to be configured")
	}

	if len(service.Command) != 2 || service.Command[0] != "sleep" {
		t.Errorf("Expected command [sleep infinity], got %v", service.Command)
	}
}

func TestGenerateBackingService_Postgres(t *testing.T) {
	generator, _ := NewGenerator()

	svc := envdetect.Service{
		Type:    "postgres",
		Version: "16",
		Ports:   []envdetect.Port{{Host: 5432, Container: 5432}},
		Env: map[string]string{
			"POSTGRES_USER": "test",
		},
		Volumes: []envdetect.Volume{
			{Name: "pg_data", Path: "/var/lib/postgresql/data"},
		},
	}

	service := generator.generateBackingService(svc)

	if service.Image != "postgres:16" {
		t.Errorf("Expected image postgres:16, got %s", service.Image)
	}

	if service.Healthcheck == nil {
		t.Fatal("Expected healthcheck for postgres")
	}

	if service.Healthcheck.Test[0] != "CMD-SHELL" {
		t.Errorf("Expected CMD-SHELL healthcheck, got %v", service.Healthcheck.Test)
	}
}

func TestGenerateBackingService_MongoDB(t *testing.T) {
	generator, _ := NewGenerator()

	svc := envdetect.Service{
		Type:    "mongodb",
		Version: "6",
		Ports:   []envdetect.Port{{Host: 27017, Container: 27017}},
	}

	service := generator.generateBackingService(svc)

	if service.Image != "mongodb:6" {
		t.Errorf("Expected image mongodb:6, got %s", service.Image)
	}

	if service.Healthcheck == nil {
		t.Fatal("Expected healthcheck for mongodb")
	}
}
