package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/ralph"
)

func TestRunRalphList_NoLogDir(t *testing.T) {
	// Create a temp directory with no .ralph-logs
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// Should not error when no logs exist
	err := runRalphList([]string{})
	if err != nil {
		t.Errorf("runRalphList() error = %v, want nil", err)
	}
}

func TestRunRalphList_WithLogs(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".ralph-logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create a sample log file
	logFile := filepath.Join(logDir, "test-session.jsonl")
	events := []ralph.LogEvent{
		{
			Timestamp: time.Now().Add(-1 * time.Hour),
			Event:     "session_start",
			SessionID: "test-session",
			Data: map[string]any{
				"prompt":      "Test prompt",
				"sandbox_dir": "/tmp/test",
			},
		},
		{
			Timestamp: time.Now(),
			Event:     "session_end",
			SessionID: "test-session",
			Data: map[string]any{
				"reason":     "completed",
				"iterations": float64(5),
				"total_cost": 0.05,
			},
		},
	}

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, evt := range events {
		data, _ := json.Marshal(evt)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	f.Close()

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// Test with --all flag to show completed sessions
	err = runRalphList([]string{"--all"})
	if err != nil {
		t.Errorf("runRalphList(--all) error = %v, want nil", err)
	}
}

func TestRunRalphList_CustomLogDir(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "custom-logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	err := runRalphList([]string{"--log-dir", logDir})
	if err != nil {
		t.Errorf("runRalphList(--log-dir) error = %v, want nil", err)
	}
}

func TestRunRalphList_RunningSession(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".ralph-logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create a running session (no session_end event)
	logFile := filepath.Join(logDir, "running-session.jsonl")
	events := []ralph.LogEvent{
		{
			Timestamp: time.Now().Add(-10 * time.Minute),
			Event:     "session_start",
			SessionID: "running-session",
			Data: map[string]any{
				"prompt":      "A running task",
				"sandbox_dir": "/tmp/running",
			},
		},
		{
			Timestamp: time.Now().Add(-5 * time.Minute),
			Event:     "iteration_end",
			SessionID: "running-session",
			Iteration: 3,
			Data: map[string]any{
				"cost": 0.03,
			},
		},
	}

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, evt := range events {
		data, _ := json.Marshal(evt)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	f.Close()

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	// Should show running session without --all flag
	err = runRalphList([]string{})
	if err != nil {
		t.Errorf("runRalphList() error = %v, want nil", err)
	}
}

func TestRunRalphList_Help(t *testing.T) {
	err := runRalphList([]string{"--help"})
	if err != nil {
		t.Errorf("runRalphList(--help) error = %v, want nil", err)
	}
}

func TestParseSessionLog_InvalidFile(t *testing.T) {
	_, err := parseSessionLog("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestParseSessionLog_ValidFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.jsonl")

	events := []ralph.LogEvent{
		{
			Timestamp: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
			Event:     "session_start",
			SessionID: "test",
			Data: map[string]any{
				"prompt": "Test task",
			},
		},
		{
			Timestamp: time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC),
			Event:     "session_end",
			SessionID: "test",
			Data: map[string]any{
				"reason":     "completed",
				"iterations": float64(10),
				"total_cost": 0.50,
			},
		},
	}

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, evt := range events {
		data, _ := json.Marshal(evt)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	f.Close()

	info, err := parseSessionLog(logFile)
	if err != nil {
		t.Fatalf("parseSessionLog() error = %v", err)
	}

	if info.Prompt != "Test task" {
		t.Errorf("Prompt = %q, want %q", info.Prompt, "Test task")
	}
	if info.Status != "completed" {
		t.Errorf("Status = %q, want %q", info.Status, "completed")
	}
	if info.Iters != 10 {
		t.Errorf("Iters = %d, want %d", info.Iters, 10)
	}
	if info.Cost != 0.50 {
		t.Errorf("Cost = %f, want %f", info.Cost, 0.50)
	}
}
