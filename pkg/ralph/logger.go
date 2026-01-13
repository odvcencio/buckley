// pkg/ralph/logger.go
package ralph

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// LogEvent represents a single event in the JSONL log.
type LogEvent struct {
	Timestamp time.Time      `json:"ts"`
	Event     string         `json:"event"`
	SessionID string         `json:"session_id,omitempty"`
	Iteration int            `json:"iteration,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Logger writes JSONL events to a log file.
type Logger struct {
	mu        sync.Mutex
	file      *os.File
	writer    *bufio.Writer
	sessionID string
}

// NewLogger creates a new JSONL logger.
func NewLogger(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", path, err)
	}
	return &Logger{
		file:   f,
		writer: bufio.NewWriter(f),
	}, nil
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer != nil {
		l.writer.Flush()
	}
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

func (l *Logger) write(evt LogEvent, setSessionID ...string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// Atomically set sessionID if provided
	if len(setSessionID) > 0 {
		l.sessionID = setSessionID[0]
	}

	evt.Timestamp = time.Now()
	if l.sessionID != "" && evt.SessionID == "" {
		evt.SessionID = l.sessionID
	}

	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	l.writer.Write(data)
	l.writer.WriteByte('\n')
	l.writer.Flush()
}

// LogSessionStart logs the start of a session.
func (l *Logger) LogSessionStart(sessionID, prompt, sandboxDir string) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event:     "session_start",
		SessionID: sessionID,
		Data: map[string]any{
			"prompt":      prompt,
			"sandbox_dir": sandboxDir,
		},
	}, sessionID)
}

// LogIterationStart logs the start of an iteration.
func (l *Logger) LogIterationStart(iteration int) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event:     "iteration_start",
		Iteration: iteration,
	})
}

// LogToolCall logs a tool invocation.
func (l *Logger) LogToolCall(tool string, args map[string]any) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event: "tool_call",
		Data: map[string]any{
			"tool": tool,
			"args": args,
		},
	})
}

// LogToolResult logs a tool completion.
func (l *Logger) LogToolResult(tool string, success bool, output string) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event: "tool_result",
		Data: map[string]any{
			"tool":    tool,
			"success": success,
			"output":  truncate(output, 1000),
		},
	})
}

// LogModelResponse logs model output.
func (l *Logger) LogModelResponse(content string, tokens int) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event: "model_response",
		Data: map[string]any{
			"content": truncate(content, 500),
			"tokens":  tokens,
		},
	})
}

// LogIterationEnd logs the end of an iteration.
func (l *Logger) LogIterationEnd(iteration, tokens int, cost float64) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event:     "iteration_end",
		Iteration: iteration,
		Data: map[string]any{
			"tokens": tokens,
			"cost":   cost,
		},
	})
}

// LogStateChange logs a state transition.
func (l *Logger) LogStateChange(from, to State, reason string) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event: "state_change",
		Data: map[string]any{
			"from":   string(from),
			"to":     string(to),
			"reason": reason,
		},
	})
}

// LogPromptReload logs a prompt file hot-reload.
func (l *Logger) LogPromptReload(path string) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event: "prompt_reload",
		Data: map[string]any{
			"path": path,
		},
	})
}

// LogSessionEnd logs the end of a session.
func (l *Logger) LogSessionEnd(reason string, iterations int, totalCost float64) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event: "session_end",
		Data: map[string]any{
			"reason":     reason,
			"iterations": iterations,
			"total_cost": totalCost,
		},
	})
}

// LogBackendResult logs a single backend execution result.
func (l *Logger) LogBackendResult(iteration int, result *BackendResult) {
	if l == nil || result == nil {
		return
	}
	l.write(LogEvent{
		Event:     "backend_result",
		Iteration: iteration,
		Data: map[string]any{
			"backend":       result.Backend,
			"duration_ms":   result.Duration.Milliseconds(),
			"tokens_in":     result.TokensIn,
			"tokens_out":    result.TokensOut,
			"cost":          result.Cost,
			"cost_estimate": result.CostEstimate,
			"files_changed": len(result.FilesChanged),
			"tests_passed":  result.TestsPassed,
			"tests_failed":  result.TestsFailed,
		},
	})
}

// LogBackendComparison logs parallel execution results for comparison.
func (l *Logger) LogBackendComparison(iteration int, results []*BackendResult) {
	if l == nil || len(results) == 0 {
		return
	}

	backends := make([]map[string]any, 0, len(results))
	for _, r := range results {
		if r == nil {
			continue
		}
		backends = append(backends, map[string]any{
			"backend":       r.Backend,
			"duration_ms":   r.Duration.Milliseconds(),
			"tokens_in":     r.TokensIn,
			"tokens_out":    r.TokensOut,
			"cost":          r.Cost,
			"cost_estimate": r.CostEstimate,
			"files_changed": len(r.FilesChanged),
			"tests_passed":  r.TestsPassed,
			"tests_failed":  r.TestsFailed,
		})
	}

	if len(backends) == 0 {
		return
	}

	l.write(LogEvent{
		Event:     "backend_comparison",
		Iteration: iteration,
		Data: map[string]any{
			"backends": backends,
		},
	})
}

// LogBackendSwitch logs when the active backend changes.
func (l *Logger) LogBackendSwitch(from, to, reason string) {
	if l == nil {
		return
	}
	l.write(LogEvent{
		Event: "backend_switch",
		Data: map[string]any{
			"from":   from,
			"to":     to,
			"reason": reason,
		},
	})
}

// LogScheduleAction logs when a schedule rule triggers.
func (l *Logger) LogScheduleAction(action *ScheduleAction, trigger string) {
	if l == nil || action == nil {
		return
	}
	l.write(LogEvent{
		Event: "schedule_action",
		Data: map[string]any{
			"action":  action.Action,
			"mode":    action.Mode,
			"backend": action.Backend,
			"reason":  action.Reason,
			"trigger": trigger,
		},
	})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
