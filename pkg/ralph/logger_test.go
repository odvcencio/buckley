// pkg/ralph/logger_test.go
package ralph

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogger_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Log session start
	logger.LogSessionStart("sess-123", "Build a todo app", dir)

	// Log iteration
	logger.LogIterationStart(1)
	logger.LogToolCall("write_file", map[string]any{"path": "main.go"})
	logger.LogToolResult("write_file", true, "")
	logger.LogIterationEnd(1, 500, 0.01)

	// Log session end
	logger.LogSessionEnd("user_stop", 1, 0.01)

	logger.Close()

	// Read and verify
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	var events []LogEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt LogEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		events = append(events, evt)
	}

	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d", len(events))
	}

	if events[0].Event != "session_start" {
		t.Errorf("expected session_start, got %s", events[0].Event)
	}
	if events[5].Event != "session_end" {
		t.Errorf("expected session_end, got %s", events[5].Event)
	}
}

func TestLogger_SessionIDPropagation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.LogSessionStart("sess-456", "Test prompt", dir)
	logger.LogIterationStart(1)
	logger.LogToolCall("read_file", map[string]any{"path": "foo.txt"})
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	var events []LogEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt LogEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		events = append(events, evt)
	}

	// All events should have the session ID propagated
	for i, evt := range events {
		if evt.SessionID != "sess-456" {
			t.Errorf("event %d: expected session_id sess-456, got %s", i, evt.SessionID)
		}
	}
}

func TestLogger_Truncation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Create a long output string (over 1000 chars)
	longOutput := strings.Repeat("x", 1500)
	logger.LogToolResult("test_tool", true, longOutput)
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	output := evt.Data["output"].(string)
	// Should be truncated to 1000 chars + "..."
	if len(output) != 1003 {
		t.Errorf("expected output length 1003, got %d", len(output))
	}
	if !strings.HasSuffix(output, "...") {
		t.Errorf("expected truncated output to end with '...'")
	}
}

func TestLogger_ModelResponseTruncation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Create a long content string (over 500 chars)
	longContent := strings.Repeat("y", 700)
	logger.LogModelResponse(longContent, 100)
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	content := evt.Data["content"].(string)
	// Should be truncated to 500 chars + "..."
	if len(content) != 503 {
		t.Errorf("expected content length 503, got %d", len(content))
	}
	if !strings.HasSuffix(content, "...") {
		t.Errorf("expected truncated content to end with '...'")
	}
}

func TestLogger_StateChange(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.LogStateChange(StateInit, StateRunning, "starting execution")
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if evt.Event != "state_change" {
		t.Errorf("expected state_change event, got %s", evt.Event)
	}
	if evt.Data["from"] != "init" {
		t.Errorf("expected from=init, got %s", evt.Data["from"])
	}
	if evt.Data["to"] != "running" {
		t.Errorf("expected to=running, got %s", evt.Data["to"])
	}
	if evt.Data["reason"] != "starting execution" {
		t.Errorf("expected reason='starting execution', got %s", evt.Data["reason"])
	}
}

func TestLogger_PromptReload(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.LogPromptReload("/path/to/prompt.md")
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if evt.Event != "prompt_reload" {
		t.Errorf("expected prompt_reload event, got %s", evt.Event)
	}
	if evt.Data["path"] != "/path/to/prompt.md" {
		t.Errorf("expected path='/path/to/prompt.md', got %s", evt.Data["path"])
	}
}

func TestLogger_TimestampPresent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.LogSessionStart("sess-789", "Test", dir)
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if evt.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}
}

func TestLogger_AppendMode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	// Write first event
	logger1, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	logger1.LogSessionStart("sess-1", "First session", dir)
	logger1.Close()

	// Write second event (should append)
	logger2, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	logger2.LogSessionStart("sess-2", "Second session", dir)
	logger2.Close()

	// Read and verify both events present
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	var events []LogEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt LogEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		events = append(events, evt)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].SessionID != "sess-1" {
		t.Errorf("expected first session sess-1, got %s", events[0].SessionID)
	}
	if events[1].SessionID != "sess-2" {
		t.Errorf("expected second session sess-2, got %s", events[1].SessionID)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{
			name:     "short string unchanged",
			input:    "hello",
			max:      10,
			expected: "hello",
		},
		{
			name:     "exact length unchanged",
			input:    "hello",
			max:      5,
			expected: "hello",
		},
		{
			name:     "long string truncated",
			input:    "hello world",
			max:      5,
			expected: "hello...",
		},
		{
			name:     "empty string",
			input:    "",
			max:      5,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.max)
			if result != tt.expected {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, result, tt.expected)
			}
		})
	}
}

func TestLogger_LogBackendResult(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	result := &BackendResult{
		Backend:      "claude",
		Duration:     58 * time.Second,
		TokensIn:     2500,
		TokensOut:    1800,
		Cost:         0.06,
		CostEstimate: 0.06,
		FilesChanged: []string{"main.go", "util.go", "main_test.go"},
		TestsPassed:  12,
		TestsFailed:  0,
	}

	logger.LogBackendResult(15, result)
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if evt.Event != "backend_result" {
		t.Errorf("expected backend_result event, got %s", evt.Event)
	}
	if evt.Iteration != 15 {
		t.Errorf("expected iteration=15, got %d", evt.Iteration)
	}
	if evt.Data["backend"] != "claude" {
		t.Errorf("expected backend=claude, got %v", evt.Data["backend"])
	}
	if int(evt.Data["duration_ms"].(float64)) != 58000 {
		t.Errorf("expected duration_ms=58000, got %v", evt.Data["duration_ms"])
	}
	if int(evt.Data["tokens_in"].(float64)) != 2500 {
		t.Errorf("expected tokens_in=2500, got %v", evt.Data["tokens_in"])
	}
	if int(evt.Data["tokens_out"].(float64)) != 1800 {
		t.Errorf("expected tokens_out=1800, got %v", evt.Data["tokens_out"])
	}
	if evt.Data["cost"].(float64) != 0.06 {
		t.Errorf("expected cost=0.06, got %v", evt.Data["cost"])
	}
	if evt.Data["cost_estimate"].(float64) != 0.06 {
		t.Errorf("expected cost_estimate=0.06, got %v", evt.Data["cost_estimate"])
	}
	if int(evt.Data["files_changed"].(float64)) != 3 {
		t.Errorf("expected files_changed=3, got %v", evt.Data["files_changed"])
	}
	if int(evt.Data["tests_passed"].(float64)) != 12 {
		t.Errorf("expected tests_passed=12, got %v", evt.Data["tests_passed"])
	}
	if int(evt.Data["tests_failed"].(float64)) != 0 {
		t.Errorf("expected tests_failed=0, got %v", evt.Data["tests_failed"])
	}
}

func TestLogger_LogBackendResult_NilResult(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Should not panic on nil result
	logger.LogBackendResult(1, nil)
	logger.Close()

	// Verify file is empty or no event written
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if len(data) != 0 {
		t.Error("expected no event for nil result")
	}
}

func TestLogger_LogBackendComparison(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	results := []*BackendResult{
		{
			Backend:      "claude",
			Duration:     30 * time.Second,
			TokensIn:     1500,
			TokensOut:    1000,
			Cost:         0.03,
			CostEstimate: 0.03,
			TestsPassed:  10,
			TestsFailed:  0,
		},
		{
			Backend:      "codex",
			Duration:     25 * time.Second,
			TokensIn:     1200,
			TokensOut:    800,
			Cost:         0.02,
			CostEstimate: 0.02,
			TestsPassed:  8,
			TestsFailed:  2,
		},
	}

	logger.LogBackendComparison(5, results)
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if evt.Event != "backend_comparison" {
		t.Errorf("expected backend_comparison event, got %s", evt.Event)
	}
	if evt.Iteration != 5 {
		t.Errorf("expected iteration=5, got %d", evt.Iteration)
	}

	backends, ok := evt.Data["backends"].([]any)
	if !ok {
		t.Fatalf("expected backends array, got %T", evt.Data["backends"])
	}
	if len(backends) != 2 {
		t.Errorf("expected 2 backends, got %d", len(backends))
	}

	first := backends[0].(map[string]any)
	if first["backend"] != "claude" {
		t.Errorf("expected first backend=claude, got %v", first["backend"])
	}

	second := backends[1].(map[string]any)
	if second["backend"] != "codex" {
		t.Errorf("expected second backend=codex, got %v", second["backend"])
	}
}

func TestLogger_LogBackendComparison_Empty(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Should not panic on empty slice
	logger.LogBackendComparison(1, []*BackendResult{})
	logger.LogBackendComparison(2, nil)
	logger.Close()

	// Verify file is empty or no events written
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if len(data) != 0 {
		t.Error("expected no events for empty results")
	}
}

func TestLogger_LogBackendSwitch(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.LogBackendSwitch("claude", "codex", "rate limit reached")
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if evt.Event != "backend_switch" {
		t.Errorf("expected backend_switch event, got %s", evt.Event)
	}
	if evt.Data["from"] != "claude" {
		t.Errorf("expected from=claude, got %v", evt.Data["from"])
	}
	if evt.Data["to"] != "codex" {
		t.Errorf("expected to=codex, got %v", evt.Data["to"])
	}
	if evt.Data["reason"] != "rate limit reached" {
		t.Errorf("expected reason='rate limit reached', got %v", evt.Data["reason"])
	}
}

func TestLogger_LogScheduleAction(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	action := &ScheduleAction{
		Action:  "rotate_backend",
		Mode:    "round_robin",
		Backend: "codex",
		Reason:  "scheduled rotation",
	}

	logger.LogScheduleAction(action, "every_5_iterations")
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	var evt LogEvent
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if evt.Event != "schedule_action" {
		t.Errorf("expected schedule_action event, got %s", evt.Event)
	}
	if evt.Data["action"] != "rotate_backend" {
		t.Errorf("expected action=rotate_backend, got %v", evt.Data["action"])
	}
	if evt.Data["mode"] != "round_robin" {
		t.Errorf("expected mode=round_robin, got %v", evt.Data["mode"])
	}
	if evt.Data["backend"] != "codex" {
		t.Errorf("expected backend=codex, got %v", evt.Data["backend"])
	}
	if evt.Data["reason"] != "scheduled rotation" {
		t.Errorf("expected reason='scheduled rotation', got %v", evt.Data["reason"])
	}
	if evt.Data["trigger"] != "every_5_iterations" {
		t.Errorf("expected trigger='every_5_iterations', got %v", evt.Data["trigger"])
	}
}

func TestLogger_LogScheduleAction_NilAction(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.jsonl")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Should not panic on nil action
	logger.LogScheduleAction(nil, "test")
	logger.Close()

	// Verify file is empty
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if len(data) != 0 {
		t.Error("expected no event for nil action")
	}
}

func TestLogger_NilLoggerSafe(t *testing.T) {
	var logger *Logger

	// All these should not panic
	logger.LogBackendResult(1, &BackendResult{})
	logger.LogBackendComparison(1, []*BackendResult{})
	logger.LogBackendSwitch("a", "b", "test")
	logger.LogScheduleAction(&ScheduleAction{}, "test")
}
