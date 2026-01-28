// Package shellmode provides inline shell command execution with ! and $ prefixes.
package shellmode

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Mode represents the current input mode.
type Mode int

const (
	ModeNormal Mode = iota // Normal chat input
	ModeShell              // Shell command (! prefix)
	ModeEnv                // Environment variable ($ prefix)
)

// Handler manages shell mode detection and execution.
type Handler struct {
	mu sync.Mutex

	// History
	shellHistory []string
	historyIdx   int
	maxHistory   int

	// Execution
	workDir     string
	timeout     time.Duration
	running     bool
	cancelFunc  context.CancelFunc

	// Output callback
	onOutput func(output string, isError bool)
}

// Result represents command execution result.
type Result struct {
	Command  string
	Output   string
	ExitCode int
	Duration time.Duration
	Error    error
}

// NewHandler creates a new shell mode handler.
func NewHandler(workDir string) *Handler {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &Handler{
		workDir:      workDir,
		timeout:      30 * time.Second,
		maxHistory:   100,
		shellHistory: make([]string, 0, 100),
	}
}

// SetWorkDir sets the working directory.
func (h *Handler) SetWorkDir(dir string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.workDir = dir
}

// SetTimeout sets command timeout.
func (h *Handler) SetTimeout(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.timeout = d
}

// SetOutputCallback sets the callback for command output.
func (h *Handler) SetOutputCallback(fn func(output string, isError bool)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onOutput = fn
}

// DetectMode checks if input starts with a mode prefix.
func DetectMode(input string) (Mode, string) {
	input = strings.TrimSpace(input)

	if strings.HasPrefix(input, "!") {
		return ModeShell, strings.TrimPrefix(input, "!")
	}

	if strings.HasPrefix(input, "$") {
		return ModeEnv, strings.TrimPrefix(input, "$")
	}

	return ModeNormal, input
}

// Execute runs a shell command.
func (h *Handler) Execute(command string) Result {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return Result{
			Command: command,
			Error:   ErrAlreadyRunning,
		}
	}
	h.running = true
	workDir := h.workDir
	timeout := h.timeout
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		h.running = false
		h.cancelFunc = nil
		h.mu.Unlock()
	}()

	start := time.Now()

	// Add to history
	h.addToHistory(command)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	h.mu.Lock()
	h.cancelFunc = cancel
	h.mu.Unlock()
	defer cancel()

	// Execute command
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := Result{
		Command:  command,
		Duration: time.Since(start),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Error = err
			result.ExitCode = -1
		}
	}

	// Combine output
	if stderr.Len() > 0 {
		result.Output = strings.TrimSpace(stderr.String())
		if stdout.Len() > 0 {
			result.Output += "\n" + strings.TrimSpace(stdout.String())
		}
	} else {
		result.Output = strings.TrimSpace(stdout.String())
	}

	// Notify callback
	h.mu.Lock()
	callback := h.onOutput
	h.mu.Unlock()
	if callback != nil {
		callback(result.Output, result.ExitCode != 0)
	}

	return result
}

// Cancel cancels the currently running command.
func (h *Handler) Cancel() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.cancelFunc != nil {
		h.cancelFunc()
	}
}

// IsRunning returns true if a command is running.
func (h *Handler) IsRunning() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running
}

// GetEnv returns an environment variable value.
func (h *Handler) GetEnv(name string) string {
	name = strings.TrimSpace(name)
	return os.Getenv(name)
}

// ExpandEnv expands environment variables in a string.
func (h *Handler) ExpandEnv(s string) string {
	return os.ExpandEnv(s)
}

// History returns the command history.
func (h *Handler) History() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	result := make([]string, len(h.shellHistory))
	copy(result, h.shellHistory)
	return result
}

// HistoryUp moves up in history, returns command or empty if at top.
func (h *Handler) HistoryUp() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.shellHistory) == 0 {
		return ""
	}

	if h.historyIdx > 0 {
		h.historyIdx--
	}

	return h.shellHistory[h.historyIdx]
}

// HistoryDown moves down in history, returns command or empty if at bottom.
func (h *Handler) HistoryDown() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.shellHistory) == 0 {
		return ""
	}

	if h.historyIdx < len(h.shellHistory)-1 {
		h.historyIdx++
		return h.shellHistory[h.historyIdx]
	}

	// Past the end - return empty for new input
	h.historyIdx = len(h.shellHistory)
	return ""
}

// ResetHistoryPosition resets history navigation to end.
func (h *Handler) ResetHistoryPosition() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.historyIdx = len(h.shellHistory)
}

// ClearHistory clears command history.
func (h *Handler) ClearHistory() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shellHistory = h.shellHistory[:0]
	h.historyIdx = 0
}

func (h *Handler) addToHistory(command string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	command = strings.TrimSpace(command)
	if command == "" {
		return
	}

	// Don't add duplicates consecutively
	if len(h.shellHistory) > 0 && h.shellHistory[len(h.shellHistory)-1] == command {
		return
	}

	h.shellHistory = append(h.shellHistory, command)

	// Trim to max size
	if len(h.shellHistory) > h.maxHistory {
		h.shellHistory = h.shellHistory[len(h.shellHistory)-h.maxHistory:]
	}

	h.historyIdx = len(h.shellHistory)
}

// QuickCommands provides shortcuts for common commands.
var QuickCommands = map[string]string{
	"gs":  "git status",
	"gd":  "git diff",
	"gl":  "git log --oneline -10",
	"gb":  "git branch",
	"ls":  "ls -la",
	"pwd": "pwd",
}

// ExpandQuickCommand expands a quick command alias.
func ExpandQuickCommand(input string) string {
	if expanded, ok := QuickCommands[input]; ok {
		return expanded
	}
	return input
}

// Errors
type shellError string

func (e shellError) Error() string { return string(e) }

const (
	ErrAlreadyRunning = shellError("command already running")
	ErrTimeout        = shellError("command timed out")
)
