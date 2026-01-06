package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewChecker verifies that NewChecker creates a checker with default dependencies
func TestNewChecker(t *testing.T) {
	checker := NewChecker()
	if checker == nil {
		t.Fatal("NewChecker returned nil")
	}
	if len(checker.required) == 0 {
		t.Error("NewChecker created checker with no dependencies")
	}
	// Should have at least OpenRouter and Git dependencies
	if len(checker.required) < 2 {
		t.Errorf("Expected at least 2 default dependencies, got %d", len(checker.required))
	}
}

// TestCheckerCheckAll verifies dependency checking logic
func TestCheckerCheckAll(t *testing.T) {
	tests := []struct {
		name          string
		dependencies  []Dependency
		expectedCount int
		expectError   bool
	}{
		{
			name: "all dependencies satisfied",
			dependencies: []Dependency{
				{
					Name: "test-dep-1",
					CheckFunc: func() bool {
						return true
					},
				},
				{
					Name: "test-dep-2",
					CheckFunc: func() bool {
						return true
					},
				},
			},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "some dependencies missing",
			dependencies: []Dependency{
				{
					Name: "satisfied-dep",
					CheckFunc: func() bool {
						return true
					},
				},
				{
					Name: "missing-dep",
					CheckFunc: func() bool {
						return false
					},
				},
			},
			expectedCount: 1,
			expectError:   false,
		},
		{
			name: "all dependencies missing",
			dependencies: []Dependency{
				{
					Name: "missing-dep-1",
					CheckFunc: func() bool {
						return false
					},
				},
				{
					Name: "missing-dep-2",
					CheckFunc: func() bool {
						return false
					},
				},
			},
			expectedCount: 2,
			expectError:   false,
		},
		{
			name: "dependency with nil CheckFunc is skipped",
			dependencies: []Dependency{
				{
					Name:      "no-check-func",
					CheckFunc: nil,
				},
				{
					Name: "missing-dep",
					CheckFunc: func() bool {
						return false
					},
				},
			},
			expectedCount: 1,
			expectError:   false,
		},
		{
			name:          "empty dependency list",
			dependencies:  []Dependency{},
			expectedCount: 0,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &Checker{required: tt.dependencies}
			missing, err := checker.CheckAll()

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if len(missing) != tt.expectedCount {
				t.Errorf("Expected %d missing dependencies, got %d", tt.expectedCount, len(missing))
			}
		})
	}
}

// TestOpenRouterDependency tests the OpenRouter dependency configuration
func TestOpenRouterDependency(t *testing.T) {
	dep := openRouterDependency()

	if dep.Name == "" {
		t.Error("OpenRouter dependency has empty name")
	}
	if dep.Type != "env_var" {
		t.Errorf("Expected type 'env_var', got '%s'", dep.Type)
	}
	if dep.CheckFunc == nil {
		t.Error("OpenRouter dependency has nil CheckFunc")
	}
	if dep.InstallFunc == nil {
		t.Error("OpenRouter dependency has nil InstallFunc")
	}
	if dep.Prompt == "" {
		t.Error("OpenRouter dependency has empty prompt")
	}
	if dep.DocsLink == "" {
		t.Error("OpenRouter dependency has empty docs link")
	}
}

// TestOpenRouterCheckFunc tests the OpenRouter dependency check logic
func TestOpenRouterCheckFunc(t *testing.T) {
	// Save original env and restore after test
	originalKey := os.Getenv("OPENROUTER_API_KEY")
	originalHome := os.Getenv("HOME")
	defer func() {
		if originalKey != "" {
			os.Setenv("OPENROUTER_API_KEY", originalKey)
		} else {
			os.Unsetenv("OPENROUTER_API_KEY")
		}
		os.Setenv("HOME", originalHome)
	}()

	// Use a temp directory to ensure no config file interferes
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)

	tests := []struct {
		name       string
		envValue   string
		setupEnv   bool
		wantResult bool
	}{
		{
			name:       "key present in environment",
			envValue:   "test-api-key-12345",
			setupEnv:   true,
			wantResult: true,
		},
		{
			name:       "empty key in environment",
			envValue:   "",
			setupEnv:   true,
			wantResult: false,
		},
		{
			name:       "whitespace-only key in environment",
			envValue:   "   ",
			setupEnv:   true,
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupEnv {
				os.Setenv("OPENROUTER_API_KEY", tt.envValue)
			} else {
				os.Unsetenv("OPENROUTER_API_KEY")
			}

			dep := openRouterDependency()
			result := dep.CheckFunc()

			if result != tt.wantResult {
				t.Errorf("CheckFunc() = %v, want %v", result, tt.wantResult)
			}
		})
	}
}

// TestGitDependency tests the Git dependency configuration
func TestGitDependency(t *testing.T) {
	dep := gitDependency()

	if dep.Name == "" {
		t.Error("Git dependency has empty name")
	}
	if dep.Type != "binary" {
		t.Errorf("Expected type 'binary', got '%s'", dep.Type)
	}
	if dep.CheckFunc == nil {
		t.Error("Git dependency has nil CheckFunc")
	}
	if dep.Prompt == "" {
		t.Error("Git dependency has empty prompt")
	}
	if dep.DocsLink == "" {
		t.Error("Git dependency has empty docs link")
	}
}

// TestGitCheckFunc tests the Git dependency check logic
func TestGitCheckFunc(t *testing.T) {
	dep := gitDependency()
	result := dep.CheckFunc()

	// Verify that CheckFunc actually checks for git binary
	_, err := exec.LookPath("git")
	expectedResult := err == nil

	if result != expectedResult {
		t.Errorf("CheckFunc() = %v, but git availability is %v", result, expectedResult)
	}
}

// TestValidateAPIKey tests API key validation logic
func TestValidateAPIKey(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantError bool
	}{
		{
			name:      "valid key",
			key:       "example-key",
			wantError: false,
		},
		{
			name:      "long key",
			key:       "1234567890abcdefghijklmnopqrstuvwxyz",
			wantError: false,
		},
		{
			name:      "minimum length key",
			key:       "12345678",
			wantError: false,
		},
		{
			name:      "too short key",
			key:       "1234567",
			wantError: true,
		},
		{
			name:      "empty key",
			key:       "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAPIKey(tt.key)
			if tt.wantError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.wantError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

// TestPersistAPIKey tests API key persistence to config file
func TestPersistAPIKey(t *testing.T) {
	// Create temporary home directory
	tmpHome := t.TempDir()

	// Save and restore original HOME
	originalHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", originalHome)
	}()
	os.Setenv("HOME", tmpHome)

	testKey := "example-key" // gitleaks:allow (test fixture)
	err := persistAPIKey(testKey)
	if err != nil {
		t.Fatalf("persistAPIKey failed: %v", err)
	}

	// Verify directory was created
	buckleyDir := filepath.Join(tmpHome, ".buckley")
	if _, err := os.Stat(buckleyDir); os.IsNotExist(err) {
		t.Error(".buckley directory was not created")
	}

	// Verify file was created with correct content
	envPath := filepath.Join(buckleyDir, "config.env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("Failed to read config.env: %v", err)
	}

	expectedContent := "export OPENROUTER_API_KEY=\"" + testKey + "\"\n"
	if string(data) != expectedContent {
		t.Errorf("File content = %q, want %q", string(data), expectedContent)
	}

	// Verify file permissions are restrictive (0600)
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("Failed to stat config.env: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("File permissions = %o, want 0600", mode)
	}
}

// TestCheckConfigEnvFile tests reading API key from config file
func TestCheckConfigEnvFile(t *testing.T) {
	// Create temporary home directory
	tmpHome := t.TempDir()

	// Save and restore original HOME
	originalHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", originalHome)
	}()
	os.Setenv("HOME", tmpHome)

	tests := []struct {
		name        string
		fileContent string
		setupFile   bool
		wantKey     string
	}{
		{
			name:        "file with export and quotes",
			fileContent: "export OPENROUTER_API_KEY=\"test-key-123\"\n",
			setupFile:   true,
			wantKey:     "test-key-123",
		},
		{
			name:        "file with export and single quotes",
			fileContent: "export OPENROUTER_API_KEY='test-key-456'\n",
			setupFile:   true,
			wantKey:     "test-key-456",
		},
		{
			name:        "file without export",
			fileContent: "OPENROUTER_API_KEY=\"test-key-789\"\n",
			setupFile:   true,
			wantKey:     "test-key-789",
		},
		{
			name:        "file without quotes",
			fileContent: "OPENROUTER_API_KEY=test-key-abc\n",
			setupFile:   true,
			wantKey:     "test-key-abc",
		},
		{
			name: "file with comments and other vars",
			fileContent: `# Config file
# OpenRouter settings
export OPENROUTER_API_KEY="test-key-def"
OTHER_VAR="something"
`,
			setupFile: true,
			wantKey:   "test-key-def",
		},
		{
			name:        "file with multiple lines",
			fileContent: "SOME_VAR=value1\nexport OPENROUTER_API_KEY=\"test-key-multi\"\nANOTHER_VAR=value2\n",
			setupFile:   true,
			wantKey:     "test-key-multi",
		},
		{
			name:      "file does not exist",
			setupFile: false,
			wantKey:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up any previous test files
			buckleyDir := filepath.Join(tmpHome, ".buckley")
			os.RemoveAll(buckleyDir)

			if tt.setupFile {
				// Create directory and file
				if err := os.MkdirAll(buckleyDir, 0o700); err != nil {
					t.Fatalf("Failed to create .buckley dir: %v", err)
				}
				envPath := filepath.Join(buckleyDir, "config.env")
				if err := os.WriteFile(envPath, []byte(tt.fileContent), 0o600); err != nil {
					t.Fatalf("Failed to write config.env: %v", err)
				}
			}

			result := checkConfigEnvFile()
			if result != tt.wantKey {
				t.Errorf("checkConfigEnvFile() = %q, want %q", result, tt.wantKey)
			}
		})
	}
}

// TestCheckConfigEnvFileInvalidHome tests handling of invalid home directory
func TestCheckConfigEnvFileInvalidHome(t *testing.T) {
	// Save and restore original HOME
	originalHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", originalHome)
	}()

	// Unset HOME to simulate error condition
	os.Unsetenv("HOME")

	result := checkConfigEnvFile()
	if result != "" {
		t.Errorf("Expected empty string when HOME is unset, got %q", result)
	}
}

// TestRunWizardEmptyList tests that RunWizard handles empty dependency list
func TestRunWizardEmptyList(t *testing.T) {
	checker := NewChecker()
	err := checker.RunWizard([]Dependency{})
	if err != nil {
		t.Errorf("RunWizard with empty list returned error: %v", err)
	}
}

// TestDependencyStruct tests the Dependency type structure
func TestDependencyStruct(t *testing.T) {
	dep := Dependency{
		Name: "TestDep",
		Type: "test",
		CheckFunc: func() bool {
			return true
		},
		InstallFunc: func() error {
			return nil
		},
		Prompt:   "Test prompt",
		DocsLink: "https://example.com",
	}

	if dep.Name != "TestDep" {
		t.Errorf("Name = %q, want %q", dep.Name, "TestDep")
	}
	if dep.Type != "test" {
		t.Errorf("Type = %q, want %q", dep.Type, "test")
	}
	if dep.CheckFunc == nil {
		t.Error("CheckFunc is nil")
	}
	if dep.InstallFunc == nil {
		t.Error("InstallFunc is nil")
	}
	if !dep.CheckFunc() {
		t.Error("CheckFunc returned false, expected true")
	}
	if err := dep.InstallFunc(); err != nil {
		t.Errorf("InstallFunc returned error: %v", err)
	}
	if dep.Prompt != "Test prompt" {
		t.Errorf("Prompt = %q, want %q", dep.Prompt, "Test prompt")
	}
	if dep.DocsLink != "https://example.com" {
		t.Errorf("DocsLink = %q, want %q", dep.DocsLink, "https://example.com")
	}
}

// TestConfirmDefaultYes is difficult to test without mocking stdin,
// so we'll test indirectly through integration tests or skip.

// TestReadSecretInput is difficult to test without a terminal,
// so we'll skip direct testing of this function.

// TestCheckerStructure tests the Checker type
func TestCheckerStructure(t *testing.T) {
	checker := &Checker{
		required: []Dependency{
			{
				Name: "dep1",
				CheckFunc: func() bool {
					return true
				},
			},
			{
				Name: "dep2",
				CheckFunc: func() bool {
					return false
				},
			},
		},
	}

	if len(checker.required) != 2 {
		t.Errorf("Expected 2 dependencies, got %d", len(checker.required))
	}

	// Test that we can access and use dependencies
	if checker.required[0].Name != "dep1" {
		t.Errorf("First dependency name = %q, want %q", checker.required[0].Name, "dep1")
	}
	if !checker.required[0].CheckFunc() {
		t.Error("First dependency check should return true")
	}
	if checker.required[1].CheckFunc() {
		t.Error("Second dependency check should return false")
	}
}

// TestPersistAPIKeyErrorCases tests error handling in persistAPIKey
func TestPersistAPIKeyErrorCases(t *testing.T) {
	// Test with invalid home directory path
	originalHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", originalHome)
	}()

	// Set HOME to a path that's likely to cause issues
	// (This is system-dependent, so we'll just verify the function handles errors gracefully)
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)

	// Create a file where the directory should be to cause mkdir to fail
	badPath := filepath.Join(tmpHome, ".buckley")
	if err := os.WriteFile(badPath, []byte("block"), 0o644); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	err := persistAPIKey("test-key")
	if err == nil {
		t.Error("Expected error when .buckley path is blocked by a file")
	}
	if !strings.Contains(err.Error(), "failed to create") && !strings.Contains(err.Error(), "failed to write") {
		t.Errorf("Expected error message about creation/writing, got: %v", err)
	}
}

// TestIntegrationOpenRouterDependencyWithConfigFile tests the integration
// of environment variable checking and config file checking
func TestIntegrationOpenRouterDependencyWithConfigFile(t *testing.T) {
	// Create temporary home directory
	tmpHome := t.TempDir()

	// Save and restore original env
	originalHome := os.Getenv("HOME")
	originalKey := os.Getenv("OPENROUTER_API_KEY")
	defer func() {
		os.Setenv("HOME", originalHome)
		if originalKey != "" {
			os.Setenv("OPENROUTER_API_KEY", originalKey)
		} else {
			os.Unsetenv("OPENROUTER_API_KEY")
		}
	}()

	os.Setenv("HOME", tmpHome)
	os.Unsetenv("OPENROUTER_API_KEY")

	// Create config file
	buckleyDir := filepath.Join(tmpHome, ".buckley")
	if err := os.MkdirAll(buckleyDir, 0o700); err != nil {
		t.Fatalf("Failed to create .buckley dir: %v", err)
	}
	envPath := filepath.Join(buckleyDir, "config.env")
	testKey := "test-key-from-file"
	content := "export OPENROUTER_API_KEY=\"" + testKey + "\"\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write config.env: %v", err)
	}

	// Test that OpenRouter dependency check finds the key in config file
	dep := openRouterDependency()
	if !dep.CheckFunc() {
		t.Error("OpenRouter check should pass when key exists in config file")
	}

	// Verify that checkConfigEnvFile returns the correct key
	key := checkConfigEnvFile()
	if key != testKey {
		t.Errorf("checkConfigEnvFile() = %q, want %q", key, testKey)
	}

	// Test that environment variable takes precedence
	envKey := "test-key-from-env"
	os.Setenv("OPENROUTER_API_KEY", envKey)
	if !dep.CheckFunc() {
		t.Error("OpenRouter check should pass when key exists in environment")
	}
}
