# TUI Fixes and Verbose Flag Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix TUI issues (click-to-expand, streaming width, telemetry error, loading indicator) and add `--verbose`/`--trace` flags to one-shot commands with reasoning logging.

**Architecture:** The one-shot commands (`commit`, `pr`, `review`) will change from `--no-stream` (opt-out) to `--verbose` (opt-in) for reasoning display. Reasoning will be logged to `~/.buckley/logs/reasoning-YYYY-MM-DD.log`. TUI fixes address event handling order and error suppression.

**Tech Stack:** Go, fluffy-ui TUI framework, SQLite coordination events

---

### Task 1: Fix Click-to-Expand for Reasoning Blocks

**Files:**
- Modify: `pkg/buckley/ui/tui/app_widget.go:1752-1768`

**Problem:** The `handleChatMouse` method intercepts left clicks for text selection before the `ChatView` can handle reasoning toggle clicks.

**Change:** In `handleChatMouse`, before starting selection, check if the click is on a reasoning line and let ChatView handle it:

```go
if m.Action == MousePress && m.Button == MouseLeft {
	line, col, ok := a.chatView.PositionForPoint(m.X, m.Y)
	if ok {
		// Check if click is on reasoning block - let ChatView handle toggle
		if a.chatView.IsReasoningLine(line) {
			if a.chatView.ToggleReasoning() {
				a.dirty = true
				return true
			}
		}
		// Otherwise start selection
		if !a.selectionActive {
			a.chatView.ClearSelection()
			a.chatView.StartSelection(line, col)
			a.selectionActive = true
		} else {
			a.chatView.UpdateSelection(line, col)
		}
		a.selectionLastLine = line
		a.selectionLastCol = col
		a.selectionLastValid = true
		a.dirty = true
		return true
	}
}
```

**Test:** `go test ./pkg/buckley/ui/tui/... -v -run Mouse`

---

### Task 2: Fix Telemetry Persistence Error

**Files:**
- Modify: `cmd/buckley/coordination_runtime.go:122-124`

**Problem:** The warning "persist telemetry event: failed" prints to stderr during normal operation, likely due to context cancellation or closed store.

**Change:** Suppress context cancellation errors:

```go
if err := store.Append(ctx, streamID, []coordevents.Event{coordEvent}); err != nil {
	// Don't warn on context cancellation (expected during shutdown)
	if ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "warning: persist telemetry event: %v\n", err)
	}
}
```

---

### Task 3: Add Reasoning Logger

**Files:**
- Create: `pkg/logging/reasoning.go`
- Create: `pkg/logging/reasoning_test.go`

**Implementation:**

```go
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
```

**Test:** `go test ./pkg/logging/... -v -run Reasoning`

---

### Task 4: Update commit.go Flags

**Files:**
- Modify: `cmd/buckley/commit.go`

**Changes:**

1. Remove `noStream` flag (line 36)
2. Change `verbose` description to "stream model reasoning as it happens"
3. Add `trace` flag: `trace := fs.Bool("trace", false, "show context audit and reasoning trace after completion")`
4. Update streaming logic: `streamingEnabled := *verbose && stdinIsTerminalFn()`
5. Add reasoning logger initialization when verbose
6. Change trace output to use `*trace` instead of `*verbose`
7. Add imports: `"github.com/odvcencio/buckley/pkg/logging"`, `"path/filepath"`

**Test:** `go build ./cmd/buckley/...`

---

### Task 5: Update pr.go Flags

**Files:**
- Modify: `cmd/buckley/pr.go`

Same changes as commit.go:
1. Remove `noStream` flag
2. Update `verbose` description
3. Add `trace` flag
4. Update streaming logic
5. Add reasoning logger
6. Change trace output to use `*trace`

**Test:** `go build ./cmd/buckley/...`

---

### Task 6: Update review.go Flags

**Files:**
- Modify: `cmd/buckley/review.go`

Changes:
1. Update `verbose` description to "stream model reasoning as it happens"
2. Add `trace` flag
3. Change context audit to use `*trace` instead of `*verbose`
4. Add reasoning logger skeleton (review doesn't have streaming yet)

**Test:** `go build ./cmd/buckley/...`

---

### Task 7: Investigate Streaming Width Issue

**Files:**
- Investigate: `pkg/buckley/ui/scrollback/buffer.go`
- Investigate: `pkg/buckley/ui/widgets/chatview.go`

**Problem:** Streaming text appears in narrow column before collapsing.

**Investigation:**
1. Buffer initialized with width 80 in `chatview.go:73`
2. `Layout()` calls `buffer.Resize()` which should update width
3. Check if `Resize()` re-wraps existing content
4. Check timing: is content added before Layout is called?

---

### Task 8: Investigate Loading Indicator Issue

**Files:**
- Investigate: `pkg/buckley/ui/tui/controller.go`

**Problem:** Tool calls fail, then UI stops with no loading indicator.

**Investigation:**
1. Find tool execution flow in controller
2. Check error handling removes loading indicator
3. Verify error message is displayed to user

---

### Summary of Changes

| File | Change |
|------|--------|
| `pkg/buckley/ui/tui/app_widget.go` | Check reasoning line before selection |
| `cmd/buckley/coordination_runtime.go` | Suppress shutdown warnings |
| `pkg/logging/reasoning.go` | New reasoning logger |
| `cmd/buckley/commit.go` | `--verbose`/`--trace` flags |
| `cmd/buckley/pr.go` | `--verbose`/`--trace` flags |
| `cmd/buckley/review.go` | `--verbose`/`--trace` flags |
