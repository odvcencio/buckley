package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level represents log severity
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Category represents the subsystem generating the log
type Category string

const (
	CategoryConversation Category = "conversation"
	CategoryModel        Category = "model"
	CategoryTool         Category = "tool"
	CategoryWorkflow     Category = "workflow"
	CategoryCost         Category = "cost"
	CategorySession      Category = "session"
	CategoryValidation   Category = "validation"
	CategoryRetry        Category = "retry"
	CategoryBuilder      Category = "builder"
	CategoryReview       Category = "review"
	CategoryResearch     Category = "research"
	CategoryNetwork      Category = "network"
)

// Event represents a structured log event
type Event struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     Level             `json:"level"`
	Category  Category          `json:"category"`
	EventType string            `json:"type"`
	SessionID string            `json:"session_id,omitempty"`
	PlanID    string            `json:"plan_id,omitempty"`
	TaskID    string            `json:"task_id,omitempty"`
	Details   map[string]any    `json:"details,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Message   string            `json:"message,omitempty"`
}

// Logger writes structured events to multiple destinations
type Logger struct {
	sessionID   string
	planID      string
	baseDir     string
	sessionFile *os.File
	errorFile   *os.File
	costFile    *os.File
	mu          sync.Mutex
	minLevel    Level
}

// NewLogger creates a new structured logger
func NewLogger(baseDir, sessionID string) (*Logger, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create subdirectories
	sessionsDir := filepath.Join(baseDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}

	// Open log files
	sessionFile, err := os.OpenFile(
		filepath.Join(sessionsDir, sessionID+".jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to open session log: %w", err)
	}

	errorFile, err := os.OpenFile(
		filepath.Join(baseDir, "errors.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		sessionFile.Close()
		return nil, fmt.Errorf("failed to open error log: %w", err)
	}

	costFile, err := os.OpenFile(
		filepath.Join(baseDir, "costs.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		sessionFile.Close()
		errorFile.Close()
		return nil, fmt.Errorf("failed to open cost log: %w", err)
	}

	return &Logger{
		sessionID:   sessionID,
		baseDir:     baseDir,
		sessionFile: sessionFile,
		errorFile:   errorFile,
		costFile:    costFile,
		minLevel:    LevelInfo,
	}, nil
}

// SetMinLevel sets the minimum log level
func (l *Logger) SetMinLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.minLevel = level
}

// SetPlanID sets the current plan ID for subsequent events
func (l *Logger) SetPlanID(planID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.planID = planID
}

// Log writes an event to appropriate destinations
func (l *Logger) Log(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Set timestamp if not provided
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// Set session ID if not provided
	if event.SessionID == "" {
		event.SessionID = l.sessionID
	}

	// Set plan ID if not provided and we have one
	if event.PlanID == "" && l.planID != "" {
		event.PlanID = l.planID
	}

	// Check min level
	if !l.shouldLog(event.Level) {
		return nil
	}

	// Marshal event
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}
	data = append(data, '\n')

	// Write to session log
	if l.sessionFile != nil {
		if _, err := l.sessionFile.Write(data); err != nil {
			return fmt.Errorf("failed to write to session log: %w", err)
		}
	}

	// Write errors to error log
	if event.Level == LevelError && l.errorFile != nil {
		if _, err := l.errorFile.Write(data); err != nil {
			return fmt.Errorf("failed to write to error log: %w", err)
		}
	}

	// Write cost events to cost log
	if event.Category == CategoryCost && l.costFile != nil {
		if _, err := l.costFile.Write(data); err != nil {
			return fmt.Errorf("failed to write to cost log: %w", err)
		}
	}

	return nil
}

// shouldLog checks if event should be logged based on level
func (l *Logger) shouldLog(level Level) bool {
	levels := map[Level]int{
		LevelDebug: 0,
		LevelInfo:  1,
		LevelWarn:  2,
		LevelError: 3,
	}
	return levels[level] >= levels[l.minLevel]
}

// Helper methods for common log patterns

// Debug logs a debug event
func (l *Logger) Debug(category Category, eventType string, message string, details map[string]any) error {
	return l.Log(Event{
		Level:     LevelDebug,
		Category:  category,
		EventType: eventType,
		Message:   message,
		Details:   details,
	})
}

// Info logs an info event
func (l *Logger) Info(category Category, eventType string, message string, details map[string]any) error {
	return l.Log(Event{
		Level:     LevelInfo,
		Category:  category,
		EventType: eventType,
		Message:   message,
		Details:   details,
	})
}

// Warn logs a warning event
func (l *Logger) Warn(category Category, eventType string, message string, details map[string]any) error {
	return l.Log(Event{
		Level:     LevelWarn,
		Category:  category,
		EventType: eventType,
		Message:   message,
		Details:   details,
	})
}

// Error logs an error event
func (l *Logger) Error(category Category, eventType string, message string, details map[string]any) error {
	return l.Log(Event{
		Level:     LevelError,
		Category:  category,
		EventType: eventType,
		Message:   message,
		Details:   details,
	})
}

// Close closes all log files
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var errs []error
	if l.sessionFile != nil {
		if err := l.sessionFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.errorFile != nil {
		if err := l.errorFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.costFile != nil {
		if err := l.costFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing log files: %v", errs)
	}
	return nil
}

// ReadRecentEvents reads the last N events from the session log
func ReadRecentEvents(logPath string, count int) ([]Event, error) {
	file, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open log: %w", err)
	}
	defer file.Close()

	// Read all lines (inefficient for large files, but works for now)
	var lines []string
	decoder := json.NewDecoder(file)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			break
		}
		data, _ := json.Marshal(event)
		lines = append(lines, string(data))
	}

	// Return last N lines
	start := 0
	if len(lines) > count {
		start = len(lines) - count
	}

	events := make([]Event, 0, len(lines)-start)
	for i := start; i < len(lines); i++ {
		var event Event
		if err := json.Unmarshal([]byte(lines[i]), &event); err == nil {
			events = append(events, event)
		}
	}

	return events, nil
}
