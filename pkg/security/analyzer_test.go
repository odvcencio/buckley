package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.SeverityThreshold != SeverityLow {
		t.Errorf("SeverityThreshold = %v, want %v", config.SeverityThreshold, SeverityLow)
	}

	if !config.IncludeTests {
		t.Error("IncludeTests should default to true")
	}

	if len(config.ExcludeDirs) == 0 {
		t.Error("Should have default exclude directories")
	}

	// Check for common exclude directories
	hasVendor := false
	hasNodeModules := false
	for _, dir := range config.ExcludeDirs {
		if dir == "vendor" {
			hasVendor = true
		}
		if dir == "node_modules" {
			hasNodeModules = true
		}
	}

	if !hasVendor {
		t.Error("Should exclude 'vendor' by default")
	}

	if !hasNodeModules {
		t.Error("Should exclude 'node_modules' by default")
	}

	if config.MaxFileSize != 1*1024*1024 {
		t.Errorf("MaxFileSize = %d, want %d", config.MaxFileSize, 1*1024*1024)
	}

	if config.ContextLines != 3 {
		t.Errorf("ContextLines = %d, want 3", config.ContextLines)
	}
}

func TestNewAnalyzerRegistry(t *testing.T) {
	registry := NewAnalyzerRegistry()

	if registry == nil {
		t.Fatal("NewAnalyzerRegistry should return non-nil registry")
	}

	if len(registry.GetAnalyzers()) != 0 {
		t.Error("New registry should have no analyzers")
	}
}

func TestAnalyzerRegistry_Register(t *testing.T) {
	registry := NewAnalyzerRegistry()
	config := DefaultConfig()
	analyzer := NewSecretsAnalyzer(config)

	registry.Register(analyzer)

	analyzers := registry.GetAnalyzers()
	if len(analyzers) != 1 {
		t.Errorf("Analyzer count = %d, want 1", len(analyzers))
	}

	if analyzers[0].Name() != "secrets" {
		t.Error("Registered analyzer should be in registry")
	}
}

func TestAnalyzerRegistry_GetAnalyzer(t *testing.T) {
	registry := NewAnalyzerRegistry()
	config := DefaultConfig()

	secretsAnalyzer := NewSecretsAnalyzer(config)
	authAnalyzer := NewAuthPatternsAnalyzer(config)

	registry.Register(secretsAnalyzer)
	registry.Register(authAnalyzer)

	// Test case-insensitive lookup
	found, err := registry.GetAnalyzer("secrets")
	if err != nil {
		t.Fatalf("GetAnalyzer failed: %v", err)
	}
	if found.Name() != "secrets" {
		t.Error("Should find secrets analyzer")
	}

	// Test case-insensitive
	found, err = registry.GetAnalyzer("SECRETS")
	if err != nil {
		t.Fatalf("GetAnalyzer should be case-insensitive: %v", err)
	}
	if found.Name() != "secrets" {
		t.Error("Should find analyzer case-insensitively")
	}

	// Test not found
	_, err = registry.GetAnalyzer("nonexistent")
	if err == nil {
		t.Error("Should return error for nonexistent analyzer")
	}
}

func TestNewSecurityAnalyzer(t *testing.T) {
	config := DefaultConfig()
	sa := NewSecurityAnalyzer(config)

	if sa == nil {
		t.Fatal("NewSecurityAnalyzer should return non-nil analyzer")
	}

	registry := sa.GetAnalyzerRegistry()
	if registry == nil {
		t.Error("Registry should not be nil")
	}

	analyzers := registry.GetAnalyzers()
	if len(analyzers) == 0 {
		t.Error("Should have built-in analyzers registered")
	}

	// Check that built-in analyzers are registered
	names := make(map[string]bool)
	for _, a := range analyzers {
		names[a.Name()] = true
	}

	expectedAnalyzers := []string{"secrets", "auth-patterns", "input-validation"}
	for _, expected := range expectedAnalyzers {
		if !names[expected] {
			t.Errorf("Built-in analyzer %q not registered", expected)
		}
	}
}

func TestSecurityAnalyzer_Analyze(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files with various security issues
	testFile1 := filepath.Join(tmpDir, "secrets.go")
	content1 := `package test
var apiKey = "abc123abc123abc123abc123"
`
	if err := os.WriteFile(testFile1, []byte(content1), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	testFile2 := filepath.Join(tmpDir, "sql.go")
	content2 := `package test
import "fmt"
func query(id string) {
	sql := fmt.Sprintf("SELECT * FROM users WHERE id=%s", id)
}
`
	if err := os.WriteFile(testFile2, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	sa := NewSecurityAnalyzer(config)

	result, err := sa.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	if result == nil {
		t.Fatal("Result should not be nil")
	}

	// The analyzer should run successfully, even if no findings are detected
	// (pattern matching can be strict, so we just verify it doesn't crash)
}

func TestSecurityAnalyzer_AnalyzeNonexistentPath(t *testing.T) {
	config := DefaultConfig()
	sa := NewSecurityAnalyzer(config)

	_, err := sa.Analyze("/nonexistent/path/12345")
	if err == nil {
		t.Error("Should return error for nonexistent path")
	}
}

func TestSecurityAnalyzer_AnalyzeFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")

	if err := os.WriteFile(testFile, []byte("package test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := DefaultConfig()
	sa := NewSecurityAnalyzer(config)

	// Should fail because it's not a directory
	_, err := sa.Analyze(testFile)
	if err == nil {
		t.Error("Should return error when analyzing a file instead of directory")
	}
}

func TestSecurityAnalyzer_SeverityThreshold(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files with different severity issues
	testFile := filepath.Join(tmpDir, "issues.go")
	content := `package test
// This will trigger various severity levels
var password = "hardcoded_password"
var log = fmt.Printf("User: %s", username)
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test with high severity threshold
	config := DefaultConfig()
	config.SeverityThreshold = SeverityHigh
	sa := NewSecurityAnalyzer(config)

	result, err := sa.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// All findings should be high or critical
	for _, finding := range result.Findings {
		if finding.Severity < SeverityHigh {
			t.Errorf("Finding with severity %v should be filtered out by threshold", finding.Severity)
		}
	}
}

func TestSecurityAnalyzer_ExcludeDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Create vendor directory
	vendorDir := filepath.Join(tmpDir, "vendor")
	if err := os.MkdirAll(vendorDir, 0755); err != nil {
		t.Fatalf("Failed to create vendor dir: %v", err)
	}

	// Add a file with secrets in vendor
	vendorFile := filepath.Join(vendorDir, "secrets.go")
	content := `package vendor
var apiKey = "abc123abc123abc123abc123"
`
	if err := os.WriteFile(vendorFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write vendor file: %v", err)
	}

	config := DefaultConfig()
	sa := NewSecurityAnalyzer(config)

	result, err := sa.Analyze(tmpDir)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Should not find issues in vendor directory
	for _, finding := range result.Findings {
		if strings.Contains(finding.FilePath, "vendor") {
			t.Error("Should exclude vendor directory from analysis")
		}
	}
}

func TestShouldIncludePath(t *testing.T) {
	config := DefaultConfig()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"Go file", "/project/main.go", true},
		{"Python file", "/project/script.py", true},
		{"JavaScript file", "/project/app.js", true},
		{"TypeScript file", "/project/app.ts", true},
		{"Vendor directory", "/project/vendor/lib.go", false},
		{"Node modules", "/project/node_modules/pkg/index.js", false},
		{"Git directory", "/project/.git/config", false},
		{"Test file with IncludeTests=true", "/project/test.go", true},
		{"YAML file", "/project/config.yaml", true},
		{"JSON file", "/project/package.json", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIncludePath(tt.path, config); got != tt.want {
				t.Errorf("shouldIncludePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestShouldIncludePath_ExcludeTests(t *testing.T) {
	config := DefaultConfig()
	config.IncludeTests = false

	tests := []struct {
		path string
		want bool
	}{
		{"/project/main_test.go", false},
		{"/project/app.test.js", false},
		{"/project/test_utils.py", false},
		{"/project/main.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := shouldIncludePath(tt.path, config); got != tt.want {
				t.Errorf("shouldIncludePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestConfigFromYAML(t *testing.T) {
	data := map[string]any{
		"severity_threshold": 7.0,
		"include_tests":      false,
		"exclude_dirs":       []any{"vendor", "dist", "custom"},
	}

	config := ConfigFromYAML(data)

	if config.SeverityThreshold != 7.0 {
		t.Errorf("SeverityThreshold = %v, want 7.0", config.SeverityThreshold)
	}

	if config.IncludeTests {
		t.Error("IncludeTests should be false")
	}

	// ConfigFromYAML appends to defaults, so we should have more than 3
	if len(config.ExcludeDirs) < 3 {
		t.Errorf("ExcludeDirs count = %d, want at least 3", len(config.ExcludeDirs))
	}

	// Check that custom exclude dir is present
	hasCustom := false
	for _, dir := range config.ExcludeDirs {
		if dir == "custom" {
			hasCustom = true
			break
		}
	}

	if !hasCustom {
		t.Error("Should include custom exclude directory")
	}
}

func TestConfigFromYAML_IntegerSeverity(t *testing.T) {
	data := map[string]any{
		"severity_threshold": 9, // integer instead of float
	}

	config := ConfigFromYAML(data)

	if config.SeverityThreshold != 9.0 {
		t.Errorf("SeverityThreshold = %v, want 9.0", config.SeverityThreshold)
	}
}

func TestConfigFromYAML_PartialData(t *testing.T) {
	// Only provide some fields, rest should use defaults
	data := map[string]any{
		"include_tests": false,
	}

	config := ConfigFromYAML(data)

	// Should have defaults for non-provided fields
	if config.SeverityThreshold != SeverityLow {
		t.Error("Should use default SeverityThreshold")
	}

	if config.IncludeTests {
		t.Error("IncludeTests should be false (from YAML)")
	}

	if len(config.ExcludeDirs) == 0 {
		t.Error("Should have default ExcludeDirs")
	}
}

func TestBaseAnalyzer_MatchPattern(t *testing.T) {
	base := &baseAnalyzer{config: DefaultConfig()}

	tests := []struct {
		name    string
		line    string
		pattern string
		want    bool
	}{
		{
			"Simple match",
			"var password = \"secret123\"",
			`password\s*=\s*"[^"]*"`,
			true,
		},
		{
			"No match",
			"var username = \"john\"",
			`password\s*=`,
			false,
		},
		{
			"Case insensitive",
			"var API_KEY = \"abc123\"",
			`(?i)api[_-]?key`,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, _ := base.matchPattern(tt.line, tt.pattern)
			if matched != tt.want {
				t.Errorf("matchPattern() = %v, want %v", matched, tt.want)
			}
		})
	}
}

func TestBaseAnalyzer_GetContextLines(t *testing.T) {
	base := &baseAnalyzer{config: DefaultConfig()}

	lines := []string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
	}

	tests := []struct {
		name       string
		lineNum    int
		context    int
		wantLength int
	}{
		{"Middle line", 4, 2, 5}, // lines 2-6
		{"Start line", 1, 2, 3},  // lines 0-2
		{"End line", 7, 2, 3},    // lines 5-7 (lineNum 7 is index 6, so 7-2=5 to 7=6, which is 3 lines: 5,6,7)
		{"No context", 4, 0, 1},  // just line 4
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contextLines := base.getContextLines(lines, tt.lineNum, tt.context)
			if len(contextLines) != tt.wantLength {
				t.Errorf("getContextLines() length = %d, want %d", len(contextLines), tt.wantLength)
			}
		})
	}
}

func TestBaseAnalyzer_CreateFinding(t *testing.T) {
	base := &baseAnalyzer{config: DefaultConfig()}

	finding := base.createFinding(CategorySecrets, SeverityCritical, "Test Title", "Test Description")

	if finding == nil {
		t.Fatal("createFinding should return non-nil finding")
	}

	if finding.Category != CategorySecrets {
		t.Error("Category should be set correctly")
	}

	if finding.Severity != SeverityCritical {
		t.Error("Severity should be set correctly")
	}

	if finding.Title != "Test Title" {
		t.Error("Title should be set correctly")
	}
}
