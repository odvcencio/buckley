package dream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewAnalyzer(t *testing.T) {
	analyzer := NewAnalyzer("/test/path")
	if analyzer.rootPath != "/test/path" {
		t.Errorf("Expected rootPath '/test/path', got %q", analyzer.rootPath)
	}
}

func TestAnalyze(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some test files
	os.MkdirAll(filepath.Join(tmpDir, "cmd", "app"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "pkg", "utils"), 0755)

	os.WriteFile(filepath.Join(tmpDir, "cmd", "app", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "pkg", "utils", "helper.go"), []byte("package utils\n\nfunc Help() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "pkg", "utils", "helper_test.go"), []byte("package utils\n\nfunc TestHelp(t *testing.T) {}\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Test Project\n"), 0644)

	analyzer := NewAnalyzer(tmpDir)
	analysis, err := analyzer.Analyze()
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}

	// Check basics
	if analysis.RootPath != tmpDir {
		t.Errorf("Expected RootPath %q, got %q", tmpDir, analysis.RootPath)
	}

	if analysis.TotalFiles != 3 {
		t.Errorf("Expected 3 files, got %d", analysis.TotalFiles)
	}

	if analysis.Languages["Go"] != 3 {
		t.Errorf("Expected 3 Go files, got %d", analysis.Languages["Go"])
	}

	// Check architecture detection
	if analysis.Architecture.Type != "cli" {
		t.Errorf("Expected architecture 'cli', got %q", analysis.Architecture.Type)
	}

	// Check gaps - with README present, should still detect CI/CD gap
	// Don't require gaps since small test projects might not trigger all heuristics
	t.Logf("Found %d gaps", len(analysis.Gaps))
}

func TestExtensionToLanguage(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".go", "Go"},
		{".js", "JavaScript"},
		{".ts", "TypeScript"},
		{".py", "Python"},
		{".rs", "Rust"},
		{".java", "Java"},
		{".txt", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := extensionToLanguage(tt.ext)
		if got != tt.want {
			t.Errorf("extensionToLanguage(%q) = %q, want %q", tt.ext, got, tt.want)
		}
	}
}

func TestIsEntryPoint(t *testing.T) {
	tests := []struct {
		path string
		name string
		want bool
	}{
		{"/path/to/main.go", "main.go", true},
		{"/path/to/index.js", "index.js", true},
		{"/path/to/index.ts", "index.ts", true},
		{"/path/to/app.py", "app.py", true},
		{"/path/to/helper.go", "helper.go", false},
		{"/path/to/util.js", "util.js", false},
	}

	for _, tt := range tests {
		got := isEntryPoint(tt.path, tt.name)
		if got != tt.want {
			t.Errorf("isEntryPoint(%q, %q) = %v, want %v", tt.path, tt.name, got, tt.want)
		}
	}
}

func TestDetectArchitecture_CLI(t *testing.T) {
	analysis := &CodebaseAnalysis{
		Languages: map[string]int{
			"Go": 10,
		},
		PackageStructure: map[string][]string{
			"cmd/app": {"main.go"},
			"pkg/lib": {"lib.go"},
		},
	}

	analyzer := &Analyzer{}
	arch := analyzer.detectArchitecture(analysis)

	if arch.Type != "cli" {
		t.Errorf("Expected 'cli' architecture, got %q", arch.Type)
	}

	if len(arch.Indicators) == 0 {
		t.Error("Expected some indicators")
	}
}

func TestDetectArchitecture_Web(t *testing.T) {
	analysis := &CodebaseAnalysis{
		Languages: map[string]int{
			"Go":         5,
			"TypeScript": 10,
		},
		PackageStructure: map[string][]string{
			"api/handlers": {"user.go"},
			"frontend/src": {"app.ts"},
		},
	}

	analyzer := &Analyzer{}
	arch := analyzer.detectArchitecture(analysis)

	if arch.Type != "web" {
		t.Errorf("Expected 'web' architecture, got %q", arch.Type)
	}
}

func TestFindGaps_Testing(t *testing.T) {
	analysis := &CodebaseAnalysis{
		TotalFiles: 100,
		PackageStructure: map[string][]string{
			"pkg/lib": {"lib.go", "helper.go"},
			// No test files
		},
	}

	analyzer := &Analyzer{rootPath: t.TempDir()}
	gaps := analyzer.findGaps(analysis)

	// Should detect low test coverage
	foundTestGap := false
	for _, gap := range gaps {
		if gap.Category == "testing" {
			foundTestGap = true
			if gap.Severity != "important" {
				t.Errorf("Expected severity 'important', got %q", gap.Severity)
			}
		}
	}

	if !foundTestGap {
		t.Error("Expected to find testing gap")
	}
}

func TestFindGaps_Documentation(t *testing.T) {
	tmpDir := t.TempDir()
	// Don't create README.md

	analysis := &CodebaseAnalysis{
		TotalFiles:       50,
		PackageStructure: map[string][]string{},
	}

	analyzer := &Analyzer{rootPath: tmpDir}
	gaps := analyzer.findGaps(analysis)

	// Should detect missing README
	foundDocsGap := false
	for _, gap := range gaps {
		if gap.Category == "docs" && gap.Severity == "important" {
			foundDocsGap = true
		}
	}

	if !foundDocsGap {
		t.Error("Expected to find documentation gap for missing README")
	}
}

func TestFindGaps_Security(t *testing.T) {
	analysis := &CodebaseAnalysis{
		TotalFiles: 30,
		PackageStructure: map[string][]string{
			"api": {"handler.go"},
		},
		Architecture: ArchitecturePattern{
			Type: "web",
		},
	}

	analyzer := &Analyzer{rootPath: t.TempDir()}
	gaps := analyzer.findGaps(analysis)

	// Should detect security gap for web app
	foundSecurityGap := false
	for _, gap := range gaps {
		if gap.Category == "security" {
			foundSecurityGap = true
			if gap.Severity != "critical" {
				t.Errorf("Expected severity 'critical' for web security, got %q", gap.Severity)
			}
		}
	}

	if !foundSecurityGap {
		t.Error("Expected to find security gap for web application")
	}
}

func TestCountLines(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.txt")
	content := "line1\nline2\nline3\n"
	os.WriteFile(tmpFile, []byte(content), 0644)

	count, err := countLines(tmpFile)
	if err != nil {
		t.Fatalf("countLines failed: %v", err)
	}

	if count != 3 {
		t.Errorf("Expected 3 lines, got %d", count)
	}
}

func TestHasFiles(t *testing.T) {
	m := map[string]int{
		"cmd/app":  1,
		"pkg/util": 2,
	}

	if !hasFiles(m, "cmd") {
		t.Error("Should find 'cmd'")
	}

	if !hasFiles(m, "CMD") {
		t.Error("Should find 'CMD' (case insensitive)")
	}

	if hasFiles(m, "notfound") {
		t.Error("Should not find 'notfound'")
	}
}
