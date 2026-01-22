package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ReasoningLogger writes reasoning/thinking content to daily log files.
type ReasoningLogger struct {
	dir     string
	file    *os.File
	path    string
	mu      sync.Mutex
	lastDay string
}

// NewReasoningLogger creates a reasoning logger that writes to dir.
// Log files are named reasoning-YYYY-MM-DD.log.
func NewReasoningLogger(dir string) (*ReasoningLogger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create reasoning log dir: %w", err)
	}

	l := &ReasoningLogger{dir: dir}
	if err := l.rotate(); err != nil {
		return nil, err
	}
	return l, nil
}

// Write appends reasoning content to the log with timestamp.
func (l *ReasoningLogger) Write(content string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != l.lastDay {
		if err := l.rotateLocked(); err != nil {
			return err
		}
	}

	if l.file == nil {
		return nil
	}

	timestamp := time.Now().Format("15:04:05")
	_, err := fmt.Fprintf(l.file, "[%s] %s\n", timestamp, content)
	return err
}

// WriteBlock writes a reasoning block with header.
func (l *ReasoningLogger) WriteBlock(model, sessionID, content string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != l.lastDay {
		if err := l.rotateLocked(); err != nil {
			return err
		}
	}

	if l.file == nil {
		return nil
	}

	timestamp := time.Now().Format("15:04:05")
	header := fmt.Sprintf("\n=== [%s] model=%s session=%s ===\n", timestamp, model, sessionID)
	if _, err := l.file.WriteString(header); err != nil {
		return err
	}
	if _, err := l.file.WriteString(content); err != nil {
		return err
	}
	_, err := l.file.WriteString("\n")
	return err
}

// Path returns the current log file path.
func (l *ReasoningLogger) Path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// Close closes the log file.
func (l *ReasoningLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

func (l *ReasoningLogger) rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotateLocked()
}

func (l *ReasoningLogger) rotateLocked() error {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}

	today := time.Now().Format("2006-01-02")
	l.lastDay = today
	l.path = filepath.Join(l.dir, "reasoning-"+today+".log")

	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open reasoning log: %w", err)
	}
	l.file = file
	return nil
}
