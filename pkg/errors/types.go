package errors

import (
	"fmt"
	"runtime"
	"strings"
)

// ErrorCode represents a structured error code
type ErrorCode string

const (
	// Configuration errors
	ErrCodeConfigLoad    ErrorCode = "CONFIG_LOAD"
	ErrCodeConfigParse   ErrorCode = "CONFIG_PARSE"
	ErrCodeConfigInvalid ErrorCode = "CONFIG_INVALID"

	// Model errors
	ErrCodeModelNotFound  ErrorCode = "MODEL_NOT_FOUND"
	ErrCodeModelInvalid   ErrorCode = "MODEL_INVALID"
	ErrCodeModelAPIError  ErrorCode = "MODEL_API_ERROR"
	ErrCodeModelTimeout   ErrorCode = "MODEL_TIMEOUT"
	ErrCodeModelRateLimit ErrorCode = "MODEL_RATE_LIMIT"

	// Storage errors
	ErrCodeStorageRead    ErrorCode = "STORAGE_READ"
	ErrCodeStorageWrite   ErrorCode = "STORAGE_WRITE"
	ErrCodeStorageCorrupt ErrorCode = "STORAGE_CORRUPT"

	// Tool errors
	ErrCodeToolNotFound  ErrorCode = "TOOL_NOT_FOUND"
	ErrCodeToolExecution ErrorCode = "TOOL_EXECUTION"
	ErrCodeToolTimeout   ErrorCode = "TOOL_TIMEOUT"

	// Orchestrator errors
	ErrCodePlanInvalid    ErrorCode = "PLAN_INVALID"
	ErrCodeTaskFailed     ErrorCode = "TASK_FAILED"
	ErrCodeSelfHealFailed ErrorCode = "SELF_HEAL_FAILED"

	// Budget errors
	ErrCodeBudgetExceeded ErrorCode = "BUDGET_EXCEEDED"
	ErrCodeCostTracking   ErrorCode = "COST_TRACKING"

	// Generic errors
	ErrCodeInternal       ErrorCode = "INTERNAL"
	ErrCodeInvalidInput   ErrorCode = "INVALID_INPUT"
	ErrCodeNotImplemented ErrorCode = "NOT_IMPLEMENTED"
)

// Error represents a structured Buckley error
type Error struct {
	Code        ErrorCode
	Message     string
	Underlying  error
	Context     map[string]any
	Stack       []Frame
	Retryable   bool
	UserMessage string
	Remediation []string
}

// Frame represents a stack frame
type Frame struct {
	Function string
	File     string
	Line     int
}

// New creates a new structured error
func New(code ErrorCode, message string) *Error {
	return &Error{
		Code:      code,
		Message:   message,
		Context:   make(map[string]any),
		Stack:     captureStack(2), // Skip New and caller
		Retryable: false,
	}
}

// Wrap wraps an existing error with Buckley error context
func Wrap(err error, code ErrorCode, message string) *Error {
	if err == nil {
		return nil
	}

	return &Error{
		Code:       code,
		Message:    message,
		Underlying: err,
		Context:    make(map[string]any),
		Stack:      captureStack(2),
		Retryable:  false,
	}
}

// WithContext adds context key-value pairs to the error
func (e *Error) WithContext(key string, value any) *Error {
	if e.Context == nil {
		e.Context = make(map[string]any)
	}
	e.Context[key] = value
	return e
}

// WithRetryable marks the error as retryable
func (e *Error) WithRetryable(retryable bool) *Error {
	e.Retryable = retryable
	return e
}

// WithUserMessage sets the human-friendly message returned to users.
func (e *Error) WithUserMessage(message string) *Error {
	e.UserMessage = message
	return e
}

// WithRemediation appends actionable remediation tips for the error.
func (e *Error) WithRemediation(tips ...string) *Error {
	if len(tips) == 0 {
		return e
	}
	e.Remediation = append([]string{}, tips...)
	return e
}

// Error implements the error interface
func (e *Error) Error() string {
	var sb strings.Builder

	// Error code and message
	sb.WriteString(fmt.Sprintf("[%s] %s", e.Code, e.Message))

	// Context if present
	if len(e.Context) > 0 {
		sb.WriteString(" {")
		first := true
		for k, v := range e.Context {
			if !first {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf("%s: %v", k, v))
			first = false
		}
		sb.WriteString("}")
	}

	// Underlying error
	if e.Underlying != nil {
		sb.WriteString(fmt.Sprintf(": %v", e.Underlying))
	}

	return sb.String()
}

// Unwrap returns the underlying error for errors.Is/As
func (e *Error) Unwrap() error {
	return e.Underlying
}

// IsRetryable returns whether this error is retryable
func (e *Error) IsRetryable() bool {
	return e.Retryable
}

// StackTrace returns a formatted stack trace
func (e *Error) StackTrace() string {
	var sb strings.Builder

	sb.WriteString("Stack trace:\n")
	for i, frame := range e.Stack {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, frame.String()))
		sb.WriteString(fmt.Sprintf("     %s:%d\n", frame.File, frame.Line))
	}

	return sb.String()
}

// String formats a stack frame
func (f Frame) String() string {
	return f.Function
}

// captureStack captures the current call stack
func captureStack(skip int) []Frame {
	const maxDepth = 32
	var pcs [maxDepth]uintptr

	n := runtime.Callers(skip+1, pcs[:])
	frames := make([]Frame, 0, n)

	for i := 0; i < n; i++ {
		pc := pcs[i]
		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}

		file, line := fn.FileLine(pc)

		frames = append(frames, Frame{
			Function: fn.Name(),
			File:     file,
			Line:     line,
		})
	}

	return frames
}

// IsCode checks if an error has a specific error code
func IsCode(err error, code ErrorCode) bool {
	if err == nil {
		return false
	}

	buckleyErr, ok := err.(*Error)
	if !ok {
		return false
	}

	return buckleyErr.Code == code
}

// GetCode extracts the error code from an error
func GetCode(err error) ErrorCode {
	if err == nil {
		return ""
	}

	buckleyErr, ok := err.(*Error)
	if !ok {
		return ErrCodeInternal
	}

	return buckleyErr.Code
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	buckleyErr, ok := err.(*Error)
	if !ok {
		return false
	}

	return buckleyErr.Retryable
}
