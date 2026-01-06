package containers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsManager_LoadHostEnv(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .env file
	envContent := `
# Database config
DB_HOST=localhost
DB_PORT=5432
DB_PASSWORD=secret123

# API config
API_KEY=super_secret_key
DEBUG=true
`
	envPath := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envPath, []byte(envContent), 0644); err != nil {
		t.Fatal(err)
	}

	sm := NewSecretsManager(tmpDir)
	env := sm.loadHostEnv()

	// Verify values were loaded
	if env["DB_HOST"] != "localhost" {
		t.Errorf("Expected DB_HOST=localhost, got %s", env["DB_HOST"])
	}

	if env["DB_PORT"] != "5432" {
		t.Errorf("Expected DB_PORT=5432, got %s", env["DB_PORT"])
	}

	if env["DB_PASSWORD"] != "secret123" {
		t.Errorf("Expected DB_PASSWORD=secret123, got %s", env["DB_PASSWORD"])
	}

	if env["DEBUG"] != "true" {
		t.Errorf("Expected DEBUG=true, got %s", env["DEBUG"])
	}
}

func TestSecretsManager_FilterSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSecretsManager(tmpDir)

	env := map[string]string{
		"DB_HOST":     "localhost",
		"DB_PASSWORD": "secret123",
		"API_KEY":     "super_secret",
		"API_TOKEN":   "token123",
		"DEBUG":       "true",
		"PATH":        "/usr/bin",
		"HOME":        "/home/user",
		"SECRET_KEY":  "very_secret",
	}

	filtered := sm.filterSecrets(env)

	// Safe values should be present
	if filtered["DB_HOST"] != "localhost" {
		t.Error("Expected DB_HOST to be included")
	}

	if filtered["DEBUG"] != "true" {
		t.Error("Expected DEBUG to be included")
	}

	if filtered["PATH"] != "/usr/bin" {
		t.Error("Expected PATH to be included (safe pattern)")
	}

	if filtered["HOME"] != "/home/user" {
		t.Error("Expected HOME to be included (safe pattern)")
	}

	// Sensitive values should be filtered out
	if _, exists := filtered["DB_PASSWORD"]; exists {
		t.Error("Expected DB_PASSWORD to be filtered out")
	}

	if _, exists := filtered["API_KEY"]; exists {
		t.Error("Expected API_KEY to be filtered out")
	}

	if _, exists := filtered["API_TOKEN"]; exists {
		t.Error("Expected API_TOKEN to be filtered out")
	}

	if _, exists := filtered["SECRET_KEY"]; exists {
		t.Error("Expected SECRET_KEY to be filtered out")
	}
}

func TestSecretsManager_GenerateEnvFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .env file with mixed content
	envContent := `
DB_HOST=localhost
DB_PASSWORD=secret123
DEBUG=true
API_KEY=super_secret
`
	envPath := filepath.Join(tmpDir, ".env")
	os.WriteFile(envPath, []byte(envContent), 0644)

	sm := NewSecretsManager(tmpDir)
	if err := sm.GenerateEnvFile(); err != nil {
		t.Fatalf("GenerateEnvFile() error = %v", err)
	}

	// Verify .env.container was created
	containerEnvPath := filepath.Join(tmpDir, ".env.container")
	if _, err := os.Stat(containerEnvPath); os.IsNotExist(err) {
		t.Fatal(".env.container file was not created")
	}

	// Read and verify content
	content, err := os.ReadFile(containerEnvPath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)

	// Should contain safe values
	if !strings.Contains(contentStr, "DB_HOST=localhost") {
		t.Error("Expected DB_HOST to be in container env")
	}

	if !strings.Contains(contentStr, "DEBUG=true") {
		t.Error("Expected DEBUG to be in container env")
	}

	// Should NOT contain sensitive values
	if strings.Contains(contentStr, "DB_PASSWORD") {
		t.Error("DB_PASSWORD should not be in container env")
	}

	if strings.Contains(contentStr, "API_KEY") {
		t.Error("API_KEY should not be in container env")
	}

	// Should contain header comment
	if !strings.Contains(contentStr, "# Container environment file") {
		t.Error("Expected header comment in container env")
	}
}

func TestSecretsManager_ValidateSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSecretsManager(tmpDir)

	// Set some environment variables
	os.Setenv("TEST_SECRET_1", "value1")
	defer os.Unsetenv("TEST_SECRET_1")

	required := []string{"TEST_SECRET_1", "TEST_SECRET_2", "TEST_SECRET_3"}
	missing := sm.ValidateSecrets(required)

	// Should have 2 missing secrets
	if len(missing) != 2 {
		t.Errorf("Expected 2 missing secrets, got %d", len(missing))
	}

	// Verify the missing ones
	expectedMissing := map[string]bool{
		"TEST_SECRET_2": true,
		"TEST_SECRET_3": true,
	}

	for _, secret := range missing {
		if !expectedMissing[secret] {
			t.Errorf("Unexpected missing secret: %s", secret)
		}
	}

	// TEST_SECRET_1 should not be in missing
	for _, secret := range missing {
		if secret == "TEST_SECRET_1" {
			t.Error("TEST_SECRET_1 should not be in missing list")
		}
	}
}

func TestSecretsManager_GetRequiredSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSecretsManager(tmpDir)

	tests := []struct {
		name     string
		services []string
		want     []string
	}{
		{
			name:     "Postgres",
			services: []string{"postgres"},
			want:     []string{"POSTGRES_PASSWORD"},
		},
		{
			name:     "MySQL",
			services: []string{"mysql"},
			want:     []string{"MYSQL_ROOT_PASSWORD"},
		},
		{
			name:     "MongoDB",
			services: []string{"mongodb"},
			want:     []string{"MONGO_INITDB_ROOT_PASSWORD"},
		},
		{
			name:     "Redis (no secrets)",
			services: []string{"redis"},
			want:     []string{},
		},
		{
			name:     "Multiple services",
			services: []string{"postgres", "redis", "mongodb"},
			want:     []string{"POSTGRES_PASSWORD", "MONGO_INITDB_ROOT_PASSWORD"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sm.GetRequiredSecrets(tt.services)

			if len(got) != len(tt.want) {
				t.Errorf("GetRequiredSecrets() = %v, want %v", got, tt.want)
				return
			}

			// Check all expected secrets are present
			gotMap := make(map[string]bool)
			for _, s := range got {
				gotMap[s] = true
			}

			for _, want := range tt.want {
				if !gotMap[want] {
					t.Errorf("Expected secret %s not found in result", want)
				}
			}
		})
	}
}

func TestSecretsManager_LoadHostEnv_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSecretsManager(tmpDir)

	// Should not error if .env doesn't exist
	env := sm.loadHostEnv()

	// Should still have environment variables from the system
	if len(env) == 0 {
		t.Error("Expected environment variables from system")
	}
}

func TestSecretsManager_WriteEnvFile_QuotedValues(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSecretsManager(tmpDir)

	env := map[string]string{
		"SIMPLE":       "value",
		"WITH_SPACES":  "value with spaces",
		"WITH_SPECIAL": "value=with=equals",
	}

	if err := sm.writeEnvFile("test.env", env); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}

	// Read back the file
	content, _ := os.ReadFile(filepath.Join(tmpDir, "test.env"))
	contentStr := string(content)

	// Value with spaces should be quoted
	if !strings.Contains(contentStr, `WITH_SPACES="value with spaces"`) {
		t.Error("Expected quoted value for WITH_SPACES")
	}

	// Simple value should not be quoted
	if !strings.Contains(contentStr, "SIMPLE=value") {
		t.Error("Expected unquoted value for SIMPLE")
	}
}

func TestFilterSecrets_CaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSecretsManager(tmpDir)

	env := map[string]string{
		"api_key":     "secret1",
		"API_KEY":     "secret2",
		"Api_Token":   "secret3",
		"db_password": "secret4",
		"safe_value":  "safe",
	}

	filtered := sm.filterSecrets(env)

	// All variations of sensitive keys should be filtered
	if _, exists := filtered["api_key"]; exists {
		t.Error("lowercase api_key should be filtered")
	}

	if _, exists := filtered["API_KEY"]; exists {
		t.Error("uppercase API_KEY should be filtered")
	}

	if _, exists := filtered["Api_Token"]; exists {
		t.Error("mixed case Api_Token should be filtered")
	}

	if _, exists := filtered["db_password"]; exists {
		t.Error("db_password should be filtered")
	}

	// Safe value should remain
	if filtered["safe_value"] != "safe" {
		t.Error("safe_value should not be filtered")
	}
}
