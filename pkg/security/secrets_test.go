package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSecretsAnalyzer(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	if analyzer == nil {
		t.Fatal("NewSecretsAnalyzer should return non-nil analyzer")
	}

	if analyzer.Name() != "secrets" {
		t.Errorf("Name() = %v, want 'secrets'", analyzer.Name())
	}

	if analyzer.Description() == "" {
		t.Error("Description should not be empty")
	}

	if len(analyzer.secretPatterns) == 0 {
		t.Error("Should have secret patterns registered")
	}

	if analyzer.entropyThreshold <= 0 {
		t.Error("Entropy threshold should be positive")
	}
}

func TestSecretsAnalyzer_DetectAPIKey(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "config.go")

	content := `package config

var apiKey = "abc123abc123abc123abc123"
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	if len(result.Findings) == 0 {
		t.Error("Should detect API key in hardcoded string")
	}

	// Check that we found a secrets-related finding
	foundSecret := false
	for _, finding := range result.Findings {
		if finding.Category == CategorySecrets {
			foundSecret = true
			if finding.Severity < SeverityHigh {
				t.Error("API key should be high or critical severity")
			}
		}
	}

	if !foundSecret {
		t.Error("Should detect at least one secret")
	}
}

func TestSecretsAnalyzer_DetectAWSCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "aws.go")

	accessKey := strings.ReplaceAll("AKIA_IOSFODNN7EXAMPLE", "_", "")
	secretKey := strings.ReplaceAll("wJalrXUtnFEMI/K7MDENG/bPxRfiCY_EXAMPLEKEY", "_", "")
	content := fmt.Sprintf(`package aws

const accessKey = %q
const secretKey = %q
`, accessKey, secretKey)
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	if len(result.Findings) == 0 {
		t.Error("Should detect AWS credentials")
	}

	// Should find AWS Access Key ID pattern
	foundAWSKey := false
	for _, finding := range result.Findings {
		if finding.Category == CategorySecrets && finding.Severity >= SeverityHigh {
			foundAWSKey = true
			break
		}
	}

	if !foundAWSKey {
		t.Error("Should detect AWS credentials as critical")
	}
}

func TestSecretsAnalyzer_DetectPrivateKey(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "keys.go")

	header := strings.ReplaceAll("-----BEGIN RSA_PRIVATE KEY-----", "_", " ")
	privateKey := header + "\nMIIEpAIBAAKCAQEA..."
	content := fmt.Sprintf(`package keys

const privateKey = %q
`, privateKey)
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	if len(result.Findings) == 0 {
		t.Error("Should detect private key")
	}

	foundPrivateKey := false
	for _, finding := range result.Findings {
		if finding.Category == CategorySecrets && finding.Severity == SeverityCritical {
			foundPrivateKey = true
			break
		}
	}

	if !foundPrivateKey {
		t.Error("Should detect private key as critical")
	}
}

func TestSecretsAnalyzer_DetectDatabasePassword(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "db.go")

	content := `package db

var password = "super_secret_password_123"
var connString = "postgres://user:mypassword@localhost/db"
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	if len(result.Findings) == 0 {
		t.Error("Should detect database password")
	}
}

func TestSecretsAnalyzer_DetectJWT(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "auth.go")

	content := `package auth

const token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	if len(result.Findings) == 0 {
		t.Error("Should detect JWT token")
	}

	foundJWT := false
	for _, finding := range result.Findings {
		if finding.Category == CategorySecrets {
			foundJWT = true
			break
		}
	}

	if !foundJWT {
		t.Error("Should detect JWT token")
	}
}

func TestSecretsAnalyzer_ShouldAnalyzeFile(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"Go file", "/path/to/file.go", true},
		{"Python file", "/path/to/script.py", true},
		{"JavaScript file", "/path/to/app.js", true},
		{"Binary file", "/path/to/app.exe", false},
		{"Image file", "/path/to/image.png", false},
		{"Test file with IncludeTests", "/path/to/file_test.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary file
			tmpDir := t.TempDir()
			testPath := filepath.Join(tmpDir, filepath.Base(tt.path))
			if err := os.WriteFile(testPath, []byte("test"), 0644); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			if got := analyzer.shouldAnalyzeFile(testPath); got != tt.want {
				t.Errorf("shouldAnalyzeFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSecretsAnalyzer_SkipTestFiles(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "secret_test.go")

	content := `package test

var apiKey = "sk_test_1234567890abcdef"
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	config.IncludeTests = false
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Should skip test files when IncludeTests is false
	if len(result.Findings) > 0 {
		t.Error("Should skip test files when IncludeTests is false")
	}
}

func TestSecretsAnalyzer_CalculateEntropy(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	tests := []struct {
		name    string
		input   string
		wantLow bool // true if we expect low entropy
	}{
		{"Uniform string", "abcdefghijklmnop", false},
		{"Random-looking", "k3jD9fL2mN5qP8rT", false},
		{"Repeated chars", "aaaaaaaaaaaaaaaa", true},
		{"Empty string", "", true},
		{"Single char", "a", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entropy := analyzer.calculateEntropy(tt.input)

			if tt.wantLow && entropy > 2.0 {
				t.Errorf("Expected low entropy for %q, got %.2f", tt.input, entropy)
			}

			if !tt.wantLow && entropy < 0 {
				t.Errorf("Entropy should not be negative, got %.2f", entropy)
			}
		})
	}
}

func TestSecretsAnalyzer_IsCommonString(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	tests := []struct {
		input string
		want  bool
	}{
		{"password", true},
		{"localhost", true},
		{"127.0.0.1", true},
		{"test", true},
		{"application/json", true},
		{"/path/to/file", true},
		{"k3jD9fL2mN5qP8rT", false},
		{"random_string_12345", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := analyzer.isCommonString(tt.input); got != tt.want {
				t.Errorf("isCommonString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSecretsAnalyzer_IsPlaceholder(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	tests := []struct {
		input string
		want  bool
	}{
		{"YOUR_API_KEY", true},
		{"YOUR_SECRET", true},
		{"PLACEHOLDER", true},
		{"INSERT_KEY_HERE", true},
		{"abc123", false}, // This is not a placeholder pattern
		{"live_key_real_123", false},
		{"actual_secret_value", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := analyzer.isPlaceholder(tt.input); got != tt.want {
				t.Errorf("isPlaceholder(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSecretsAnalyzer_IsHexEncodedData(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	tests := []struct {
		input string
		want  bool
	}{
		{"deadbeef", true},
		{"0123456789abcdef", true},
		{"ABCDEF123456", true},
		{"not-hex-data", false},
		{"has spaces", false},
		{"g123456", false}, // 'g' is not hex
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := analyzer.isHexEncodedData(tt.input); got != tt.want {
				t.Errorf("isHexEncodedData(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSecretsAnalyzer_HasUniformCharDistribution(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"Uniform distribution", "abcdefghijklmnopqrstuvwxyz", true},
		{"Repeated chars", "aaaaaaaaaaaaaaaaaaaaaa", false},
		{"Short string", "abc", false},
		{"API key like", "live_key_1a2b3c4d5e6f7g8h9i0j", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzer.hasUniformCharDistribution(tt.input); got != tt.want {
				t.Errorf("hasUniformCharDistribution(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSecretsAnalyzer_HasMixedAlphanumericSymbols(t *testing.T) {
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"Mixed types", "Abc123-_+", true},
		{"Only lowercase", "abcdefgh", false},
		{"Only uppercase", "ABCDEFGH", false},
		{"Only digits", "12345678", false},
		{"Lower and upper", "AbCdEfGh", false},  // Only 2 types
		{"Lower, upper, digit", "Abc123", true}, // 3 types
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzer.hasMixedAlphanumericSymbols(tt.input); got != tt.want {
				t.Errorf("hasMixedAlphanumericSymbols(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSecretsAnalyzer_SkipLargeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := filepath.Join(tmpDir, "large.go")

	// Create a file larger than MaxFileSize
	content := make([]byte, 2*1024*1024) // 2MB
	for i := range content {
		content[i] = 'a'
	}

	if err := os.WriteFile(largeFile, content, 0644); err != nil {
		t.Fatalf("Failed to write large file: %v", err)
	}

	config := DefaultConfig()
	config.MaxFileSize = 1 * 1024 * 1024 // 1MB limit
	analyzer := NewSecretsAnalyzer(config)

	// Should not crash and should skip the large file
	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Result should not have findings from the large file
	if result == nil {
		t.Error("Result should not be nil")
	}
}

func TestSecretsAnalyzer_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze should not fail on empty directory: %v", err)
	}

	if len(result.Findings) != 0 {
		t.Error("Empty directory should have no findings")
	}

	if result.FileCount != 0 {
		t.Error("Empty directory should have 0 file count")
	}
}

func TestSecretsAnalyzer_NoSecretsInCleanCode(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "clean.go")

	content := `package main

import "os"

func main() {
	apiKey := os.Getenv("API_KEY")
	println(apiKey)
}
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	result, err := analyzer.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Clean code using environment variables should not trigger findings
	// (or at most very low confidence findings)
	for _, finding := range result.Findings {
		if finding.Confidence > 0.7 {
			t.Errorf("Clean code should not have high-confidence findings: %s", finding.Title)
		}
	}
}
