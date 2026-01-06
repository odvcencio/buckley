package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewLogger tests logger construction with temp directories
func TestNewLogger(t *testing.T) {
	tests := []struct {
		name      string
		baseDir   string
		sessionID string
		wantErr   bool
	}{
		{
			name:      "valid directory and session ID",
			baseDir:   t.TempDir(),
			sessionID: "test-session-123",
			wantErr:   false,
		},
		{
			name:      "creates directories if not exist",
			baseDir:   filepath.Join(t.TempDir(), "nested", "path"),
			sessionID: "session-456",
			wantErr:   false,
		},
		{
			name:      "empty session ID",
			baseDir:   t.TempDir(),
			sessionID: "",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.baseDir, tt.sessionID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewLogger() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			defer logger.Close()

			// Verify logger fields
			if logger.sessionID != tt.sessionID {
				t.Errorf("sessionID = %v, want %v", logger.sessionID, tt.sessionID)
			}
			if logger.baseDir != tt.baseDir {
				t.Errorf("baseDir = %v, want %v", logger.baseDir, tt.baseDir)
			}
			if logger.minLevel != LevelInfo {
				t.Errorf("minLevel = %v, want %v", logger.minLevel, LevelInfo)
			}

			// Verify files were created
			sessionsDir := filepath.Join(tt.baseDir, "sessions")
			if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
				t.Errorf("sessions directory not created")
			}

			sessionFile := filepath.Join(sessionsDir, tt.sessionID+".jsonl")
			if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
				t.Errorf("session log file not created")
			}

			errorFile := filepath.Join(tt.baseDir, "errors.jsonl")
			if _, err := os.Stat(errorFile); os.IsNotExist(err) {
				t.Errorf("errors.jsonl not created")
			}

			costFile := filepath.Join(tt.baseDir, "costs.jsonl")
			if _, err := os.Stat(costFile); os.IsNotExist(err) {
				t.Errorf("costs.jsonl not created")
			}
		})
	}
}

// TestNewLoggerInvalidDirectory tests error handling for invalid directories
func TestNewLoggerInvalidDirectory(t *testing.T) {
	// Create a file where we want a directory
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "file-not-dir")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Try to create logger with a file path instead of directory
	_, err := NewLogger(filePath, "test-session")
	if err == nil {
		t.Fatal("expected error when baseDir is a file, got nil")
	}
}

// TestLogEvent tests the Log method
func TestLogEvent(t *testing.T) {
	baseDir := t.TempDir()
	sessionID := "test-session"
	logger, err := NewLogger(baseDir, sessionID)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Test basic event logging
	event := Event{
		Level:     LevelInfo,
		Category:  CategoryModel,
		EventType: "test_event",
		Message:   "test message",
		Details: map[string]any{
			"key1": "value1",
			"key2": 42,
		},
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Log() failed: %v", err)
	}

	// Read back the event from session log
	sessionFile := filepath.Join(baseDir, "sessions", sessionID+".jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	logged := events[0]
	if logged.Level != event.Level {
		t.Errorf("Level = %v, want %v", logged.Level, event.Level)
	}
	if logged.Category != event.Category {
		t.Errorf("Category = %v, want %v", logged.Category, event.Category)
	}
	if logged.EventType != event.EventType {
		t.Errorf("EventType = %v, want %v", logged.EventType, event.EventType)
	}
	if logged.Message != event.Message {
		t.Errorf("Message = %v, want %v", logged.Message, event.Message)
	}
	if logged.SessionID != sessionID {
		t.Errorf("SessionID = %v, want %v", logged.SessionID, sessionID)
	}
}

// TestLogEventWithTimestamp tests that timestamp is set automatically
func TestLogEventWithTimestamp(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	before := time.Now()
	event := Event{
		Level:     LevelInfo,
		Category:  CategoryModel,
		EventType: "timestamp_test",
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Log() failed: %v", err)
	}
	after := time.Now()

	// Read back the event
	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	logged := events[0]
	if logged.Timestamp.IsZero() {
		t.Error("Timestamp should be set automatically")
	}
	if logged.Timestamp.Before(before) || logged.Timestamp.After(after) {
		t.Errorf("Timestamp %v not in expected range [%v, %v]", logged.Timestamp, before, after)
	}
}

// TestLogErrorEvent tests error events are written to both session and error logs
func TestLogErrorEvent(t *testing.T) {
	baseDir := t.TempDir()
	sessionID := "test-session"
	logger, err := NewLogger(baseDir, sessionID)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	event := Event{
		Level:     LevelError,
		Category:  CategoryModel,
		EventType: "error_event",
		Message:   "something went wrong",
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Log() failed: %v", err)
	}

	// Verify event in session log
	sessionFile := filepath.Join(baseDir, "sessions", sessionID+".jsonl")
	sessionEvents, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents (session) failed: %v", err)
	}
	if len(sessionEvents) != 1 {
		t.Errorf("expected 1 event in session log, got %d", len(sessionEvents))
	}

	// Verify event in error log
	errorFile := filepath.Join(baseDir, "errors.jsonl")
	errorEvents, err := ReadRecentEvents(errorFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents (error) failed: %v", err)
	}
	if len(errorEvents) != 1 {
		t.Errorf("expected 1 event in error log, got %d", len(errorEvents))
	}

	if errorEvents[0].Message != event.Message {
		t.Errorf("error log message = %v, want %v", errorEvents[0].Message, event.Message)
	}
}

// TestLogCostEvent tests cost events are written to both session and cost logs
func TestLogCostEvent(t *testing.T) {
	baseDir := t.TempDir()
	sessionID := "test-session"
	logger, err := NewLogger(baseDir, sessionID)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	event := Event{
		Level:     LevelInfo,
		Category:  CategoryCost,
		EventType: "cost_event",
		Message:   "API call cost",
		Details: map[string]any{
			"cost": 0.0042,
		},
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Log() failed: %v", err)
	}

	// Verify event in session log
	sessionFile := filepath.Join(baseDir, "sessions", sessionID+".jsonl")
	sessionEvents, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents (session) failed: %v", err)
	}
	if len(sessionEvents) != 1 {
		t.Errorf("expected 1 event in session log, got %d", len(sessionEvents))
	}

	// Verify event in cost log
	costFile := filepath.Join(baseDir, "costs.jsonl")
	costEvents, err := ReadRecentEvents(costFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents (cost) failed: %v", err)
	}
	if len(costEvents) != 1 {
		t.Errorf("expected 1 event in cost log, got %d", len(costEvents))
	}

	if costEvents[0].Category != CategoryCost {
		t.Errorf("cost log category = %v, want %v", costEvents[0].Category, CategoryCost)
	}
}

// TestSetMinLevel tests level filtering
func TestSetMinLevel(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Default level is Info, so Debug should be filtered
	logger.Log(Event{
		Level:     LevelDebug,
		Category:  CategoryModel,
		EventType: "debug_event",
	})

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, _ := ReadRecentEvents(sessionFile, 10)
	if len(events) != 0 {
		t.Errorf("expected 0 events (debug filtered), got %d", len(events))
	}

	// Change to Debug level
	logger.SetMinLevel(LevelDebug)

	logger.Log(Event{
		Level:     LevelDebug,
		Category:  CategoryModel,
		EventType: "debug_event_2",
	})

	events, _ = ReadRecentEvents(sessionFile, 10)
	if len(events) != 1 {
		t.Errorf("expected 1 event after SetMinLevel(Debug), got %d", len(events))
	}

	// Change to Error level - Info should be filtered
	logger.SetMinLevel(LevelError)

	logger.Log(Event{
		Level:     LevelInfo,
		Category:  CategoryModel,
		EventType: "info_event",
	})

	events, _ = ReadRecentEvents(sessionFile, 10)
	if len(events) != 1 {
		t.Errorf("expected 1 event (info filtered), got %d", len(events))
	}

	logger.Log(Event{
		Level:     LevelError,
		Category:  CategoryModel,
		EventType: "error_event",
	})

	events, _ = ReadRecentEvents(sessionFile, 10)
	if len(events) != 2 {
		t.Errorf("expected 2 events (error logged), got %d", len(events))
	}
}

// TestShouldLog tests the shouldLog method indirectly
func TestShouldLog(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	tests := []struct {
		name      string
		minLevel  Level
		logLevel  Level
		shouldLog bool
	}{
		{"debug level allows debug", LevelDebug, LevelDebug, true},
		{"debug level allows info", LevelDebug, LevelInfo, true},
		{"debug level allows warn", LevelDebug, LevelWarn, true},
		{"debug level allows error", LevelDebug, LevelError, true},
		{"info level blocks debug", LevelInfo, LevelDebug, false},
		{"info level allows info", LevelInfo, LevelInfo, true},
		{"info level allows warn", LevelInfo, LevelWarn, true},
		{"info level allows error", LevelInfo, LevelError, true},
		{"warn level blocks debug", LevelWarn, LevelDebug, false},
		{"warn level blocks info", LevelWarn, LevelInfo, false},
		{"warn level allows warn", LevelWarn, LevelWarn, true},
		{"warn level allows error", LevelWarn, LevelError, true},
		{"error level blocks debug", LevelError, LevelDebug, false},
		{"error level blocks info", LevelError, LevelInfo, false},
		{"error level blocks warn", LevelError, LevelWarn, false},
		{"error level allows error", LevelError, LevelError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger.SetMinLevel(tt.minLevel)
			result := logger.shouldLog(tt.logLevel)
			if result != tt.shouldLog {
				t.Errorf("shouldLog(%v) with minLevel %v = %v, want %v",
					tt.logLevel, tt.minLevel, result, tt.shouldLog)
			}
		})
	}
}

// TestDebugHelper tests the Debug helper method
func TestDebugHelper(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Set level to debug so it's not filtered
	logger.SetMinLevel(LevelDebug)

	details := map[string]any{"key": "value"}
	if err := logger.Debug(CategoryModel, "test_type", "test message", details); err != nil {
		t.Fatalf("Debug() failed: %v", err)
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.Level != LevelDebug {
		t.Errorf("Level = %v, want %v", event.Level, LevelDebug)
	}
	if event.Category != CategoryModel {
		t.Errorf("Category = %v, want %v", event.Category, CategoryModel)
	}
	if event.EventType != "test_type" {
		t.Errorf("EventType = %v, want %v", event.EventType, "test_type")
	}
	if event.Message != "test message" {
		t.Errorf("Message = %v, want %v", event.Message, "test message")
	}
}

// TestInfoHelper tests the Info helper method
func TestInfoHelper(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	if err := logger.Info(CategoryWorkflow, "info_type", "info message", nil); err != nil {
		t.Fatalf("Info() failed: %v", err)
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.Level != LevelInfo {
		t.Errorf("Level = %v, want %v", event.Level, LevelInfo)
	}
	if event.Category != CategoryWorkflow {
		t.Errorf("Category = %v, want %v", event.Category, CategoryWorkflow)
	}
}

// TestWarnHelper tests the Warn helper method
func TestWarnHelper(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	if err := logger.Warn(CategoryTool, "warn_type", "warning message", nil); err != nil {
		t.Fatalf("Warn() failed: %v", err)
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.Level != LevelWarn {
		t.Errorf("Level = %v, want %v", event.Level, LevelWarn)
	}
	if event.Category != CategoryTool {
		t.Errorf("Category = %v, want %v", event.Category, CategoryTool)
	}
}

// TestErrorHelper tests the Error helper method
func TestErrorHelper(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	if err := logger.Error(CategorySession, "error_type", "error message", nil); err != nil {
		t.Fatalf("Error() failed: %v", err)
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.Level != LevelError {
		t.Errorf("Level = %v, want %v", event.Level, LevelError)
	}
	if event.Category != CategorySession {
		t.Errorf("Category = %v, want %v", event.Category, CategorySession)
	}
}

// TestSetPlanID tests setting plan ID
func TestSetPlanID(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	planID := "plan-123"
	logger.SetPlanID(planID)

	// Log an event without plan ID - should be filled in automatically
	if err := logger.Info(CategoryWorkflow, "test", "test", nil); err != nil {
		t.Fatalf("Info() failed: %v", err)
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].PlanID != planID {
		t.Errorf("PlanID = %v, want %v", events[0].PlanID, planID)
	}
}

// TestEventWithExplicitIDs tests that explicit IDs are not overwritten
func TestEventWithExplicitIDs(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "default-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	logger.SetPlanID("default-plan")

	// Log event with explicit IDs
	explicitSessionID := "explicit-session"
	explicitPlanID := "explicit-plan"
	explicitTaskID := "explicit-task"

	event := Event{
		Level:     LevelInfo,
		Category:  CategoryModel,
		EventType: "test",
		SessionID: explicitSessionID,
		PlanID:    explicitPlanID,
		TaskID:    explicitTaskID,
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Log() failed: %v", err)
	}

	sessionFile := filepath.Join(baseDir, "sessions", "default-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	logged := events[0]
	if logged.SessionID != explicitSessionID {
		t.Errorf("SessionID = %v, want %v", logged.SessionID, explicitSessionID)
	}
	if logged.PlanID != explicitPlanID {
		t.Errorf("PlanID = %v, want %v", logged.PlanID, explicitPlanID)
	}
	if logged.TaskID != explicitTaskID {
		t.Errorf("TaskID = %v, want %v", logged.TaskID, explicitTaskID)
	}
}

// TestClose tests cleanup of log files
func TestClose(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Log something
	logger.Info(CategoryModel, "test", "test", nil)

	// Close logger
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Verify files still exist and are readable
	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents after Close() failed: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event after Close(), got %d", len(events))
	}
}

// TestReadRecentEvents tests reading events with different counts
func TestReadRecentEvents(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Log multiple events
	for i := 0; i < 10; i++ {
		logger.Info(CategoryModel, "test", "message", map[string]any{
			"index": i,
		})
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")

	tests := []struct {
		name      string
		count     int
		wantCount int
	}{
		{"read last 5", 5, 5},
		{"read last 10", 10, 10},
		{"read more than exist", 20, 10},
		{"read 0", 0, 0},
		{"read 1", 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := ReadRecentEvents(sessionFile, tt.count)
			if err != nil {
				t.Fatalf("ReadRecentEvents failed: %v", err)
			}
			if len(events) != tt.wantCount {
				t.Errorf("got %d events, want %d", len(events), tt.wantCount)
			}
		})
	}
}

// TestReadRecentEventsNonexistent tests reading from nonexistent file
func TestReadRecentEventsNonexistent(t *testing.T) {
	_, err := ReadRecentEvents("/nonexistent/path/file.jsonl", 10)
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

// TestReadRecentEventsEmptyFile tests reading from empty file
func TestReadRecentEventsEmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	emptyFile := filepath.Join(tempDir, "empty.jsonl")
	if err := os.WriteFile(emptyFile, []byte(""), 0644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	events, err := ReadRecentEvents(emptyFile, 10)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from empty file, got %d", len(events))
	}
}

// TestReadRecentEventsOrder tests that events are returned in correct order
func TestReadRecentEventsOrder(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Log events with sequential messages
	for i := 0; i < 5; i++ {
		logger.Info(CategoryModel, "test", "", map[string]any{
			"seq": float64(i),
		})
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 5)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	// Verify events are in order
	for i, event := range events {
		seq, ok := event.Details["seq"]
		if !ok {
			t.Fatalf("event %d missing seq in Details", i)
		}
		seqFloat, ok := seq.(float64)
		if !ok {
			t.Fatalf("event %d seq is not float64: %T", i, seq)
		}
		if int(seqFloat) != i {
			t.Errorf("event %d has seq=%v, want %d", i, seqFloat, i)
		}
	}
}

// TestConcurrentWrites tests thread safety of logging
func TestConcurrentWrites(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Launch multiple goroutines writing concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				logger.Info(CategoryModel, "concurrent", "", map[string]any{
					"goroutine": id,
					"iteration": j,
				})
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all events were written
	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 200)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	// Should have 100 events (10 goroutines * 10 iterations)
	if len(events) != 100 {
		t.Errorf("expected 100 events, got %d", len(events))
	}
}

// TestMetadataField tests event metadata field
func TestMetadataField(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	metadata := map[string]string{
		"trace_id": "abc123",
		"span_id":  "def456",
	}

	event := Event{
		Level:     LevelInfo,
		Category:  CategoryModel,
		EventType: "test",
		Metadata:  metadata,
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Log() failed: %v", err)
	}

	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	events, err := ReadRecentEvents(sessionFile, 1)
	if err != nil {
		t.Fatalf("ReadRecentEvents failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	logged := events[0]
	if logged.Metadata == nil {
		t.Fatal("Metadata is nil")
	}
	if logged.Metadata["trace_id"] != "abc123" {
		t.Errorf("trace_id = %v, want abc123", logged.Metadata["trace_id"])
	}
	if logged.Metadata["span_id"] != "def456" {
		t.Errorf("span_id = %v, want def456", logged.Metadata["span_id"])
	}
}

// TestJSONLFormat tests that output is valid JSONL
func TestJSONLFormat(t *testing.T) {
	baseDir := t.TempDir()
	logger, err := NewLogger(baseDir, "test-session")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Log a few events
	for i := 0; i < 3; i++ {
		logger.Info(CategoryModel, "test", "", nil)
	}

	// Read raw file and verify each line is valid JSON
	sessionFile := filepath.Join(baseDir, "sessions", "test-session.jsonl")
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	file, err := os.Open(sessionFile)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer file.Close()

	lines := 0
	decoder := json.NewDecoder(file)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			break
		}
		lines++
	}

	if lines != 3 {
		t.Errorf("expected 3 valid JSON lines, got %d", lines)
	}

	// Verify file ends with newline
	if len(data) > 0 && data[len(data)-1] != '\n' {
		t.Error("JSONL file should end with newline")
	}
}
