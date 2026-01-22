package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReasoningLogger_Write(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewReasoningLogger(dir)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Close()

	if err := logger.Write("test reasoning content"); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	logger.Close()

	// Find the log file
	files, _ := filepath.Glob(filepath.Join(dir, "reasoning-*.log"))
	if len(files) != 1 {
		t.Fatalf("expected 1 log file, got %d", len(files))
	}

	content, _ := os.ReadFile(files[0])
	if !strings.Contains(string(content), "test reasoning content") {
		t.Errorf("expected content in log file, got: %s", content)
	}
}

func TestReasoningLogger_WriteBlock(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewReasoningLogger(dir)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Close()

	if err := logger.WriteBlock("gpt-4", "session-123", "thinking about stuff"); err != nil {
		t.Fatalf("failed to write block: %v", err)
	}
	logger.Close()

	files, _ := filepath.Glob(filepath.Join(dir, "reasoning-*.log"))
	if len(files) != 1 {
		t.Fatalf("expected 1 log file, got %d", len(files))
	}

	content, _ := os.ReadFile(files[0])
	if !strings.Contains(string(content), "model=gpt-4") {
		t.Errorf("expected model in log file, got: %s", content)
	}
	if !strings.Contains(string(content), "session=session-123") {
		t.Errorf("expected session in log file, got: %s", content)
	}
	if !strings.Contains(string(content), "thinking about stuff") {
		t.Errorf("expected content in log file, got: %s", content)
	}
}

func TestReasoningLogger_DateRotation(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewReasoningLogger(dir)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Close()

	expectedName := "reasoning-" + time.Now().Format("2006-01-02") + ".log"
	expectedPath := filepath.Join(dir, expectedName)

	if logger.Path() != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, logger.Path())
	}
}

func TestReasoningLogger_NilSafe(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewReasoningLogger(dir)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	// Close and then try to write - should not panic
	logger.Close()

	// Write after close should be safe (file is nil)
	err = logger.Write("after close")
	if err != nil {
		t.Errorf("expected nil error after close, got: %v", err)
	}
}
