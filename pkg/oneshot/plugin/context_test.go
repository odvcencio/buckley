package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextGathererFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello world"), 0644)

	gatherer := NewContextGatherer(tmpDir, nil)
	sources := []ContextSource{
		{Type: "file", Path: "test.txt"},
	}

	content, err := gatherer.Gather(sources)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if !strings.Contains(content, "hello world") {
		t.Errorf("content should contain file contents, got %q", content)
	}
	if !strings.Contains(content, "<file") {
		t.Errorf("content should be wrapped in <file> tags, got %q", content)
	}
}

func TestContextGathererGlob(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("file a"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("file b"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "c.md"), []byte("file c"), 0644)

	gatherer := NewContextGatherer(tmpDir, nil)
	sources := []ContextSource{
		{Type: "glob", Path: "*.txt"},
	}

	content, err := gatherer.Gather(sources)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if !strings.Contains(content, "file a") {
		t.Error("should contain file a")
	}
	if !strings.Contains(content, "file b") {
		t.Error("should contain file b")
	}
	if strings.Contains(content, "file c") {
		t.Error("should not contain file c (not .txt)")
	}
}

func TestContextGathererOptional(t *testing.T) {
	tmpDir := t.TempDir()

	gatherer := NewContextGatherer(tmpDir, nil)
	sources := []ContextSource{
		{Type: "file", Path: "nonexistent.txt", Optional: true},
	}

	content, err := gatherer.Gather(sources)
	if err != nil {
		t.Fatalf("optional source should not error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content for missing optional file, got %q", content)
	}
}

func TestContextGathererMaxBytes(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a large file
	largeContent := strings.Repeat("x", 1000)
	os.WriteFile(filepath.Join(tmpDir, "large.txt"), []byte(largeContent), 0644)

	gatherer := NewContextGatherer(tmpDir, nil)
	sources := []ContextSource{
		{Type: "file", Path: "large.txt", MaxBytes: 100},
	}

	content, err := gatherer.Gather(sources)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Content should be truncated (but includes XML tags)
	if len(content) > 200 { // Allow for tags
		t.Errorf("content should be truncated, got len=%d", len(content))
	}

	// Audit should show truncation
	audit := gatherer.Audit()
	if !audit.HasTruncation() {
		t.Error("audit should show truncation")
	}
}

func TestContextGathererEnv(t *testing.T) {
	tmpDir := t.TempDir()

	// Set test env var
	os.Setenv("BUCKLEY_TEST_VAR", "test_value")
	defer os.Unsetenv("BUCKLEY_TEST_VAR")

	gatherer := NewContextGatherer(tmpDir, nil)
	sources := []ContextSource{
		{Type: "env", Path: "BUCKLEY_TEST_VAR"},
	}

	content, err := gatherer.Gather(sources)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if !strings.Contains(content, "BUCKLEY_TEST_VAR=test_value") {
		t.Errorf("content should contain env var, got %q", content)
	}
}

func TestContextGathererFlagInterpolation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create file with flag-based name
	os.WriteFile(filepath.Join(tmpDir, "v1.0.txt"), []byte("version content"), 0644)

	flags := map[string]string{"version": "v1.0"}
	gatherer := NewContextGatherer(tmpDir, flags)
	sources := []ContextSource{
		{Type: "file", Path: "${FLAG:version}.txt"},
	}

	content, err := gatherer.Gather(sources)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if !strings.Contains(content, "version content") {
		t.Errorf("should interpolate flag in path, got %q", content)
	}
}

func TestIsSensitiveEnv(t *testing.T) {
	tests := []struct {
		env      string
		expected bool
	}{
		{"PATH=/usr/bin", false},
		{"HOME=/home/user", false},
		{"API_KEY=secret123", true},
		{"AWS_SECRET_ACCESS_KEY=xxx", true},
		{"PASSWORD=hunter2", true},
		{"AUTH_TOKEN=abc", true},
		{"DEBUG=true", false},
	}

	for _, tt := range tests {
		if got := isSensitiveEnv(tt.env); got != tt.expected {
			t.Errorf("isSensitiveEnv(%q) = %v, want %v", tt.env, got, tt.expected)
		}
	}
}
