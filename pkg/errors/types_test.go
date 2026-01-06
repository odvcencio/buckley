package errors

import (
	"errors"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	err := New(ErrCodeModelNotFound, "model xyz not found")

	if err == nil {
		t.Fatal("New should return non-nil error")
	}

	if err.Code != ErrCodeModelNotFound {
		t.Errorf("Code = %v, want %v", err.Code, ErrCodeModelNotFound)
	}

	if err.Message != "model xyz not found" {
		t.Errorf("Message = %v, want 'model xyz not found'", err.Message)
	}

	if err.Underlying != nil {
		t.Error("Underlying should be nil for New error")
	}

	if len(err.Stack) == 0 {
		t.Error("Stack should be captured")
	}

	if err.Retryable {
		t.Error("Retryable should default to false")
	}
}

func TestWrap(t *testing.T) {
	underlying := errors.New("original error")
	err := Wrap(underlying, ErrCodeStorageRead, "failed to read storage")

	if err == nil {
		t.Fatal("Wrap should return non-nil error")
	}

	if err.Underlying != underlying {
		t.Error("Underlying should be preserved")
	}

	if err.Code != ErrCodeStorageRead {
		t.Errorf("Code = %v, want %v", err.Code, ErrCodeStorageRead)
	}

	if !strings.Contains(err.Error(), "original error") {
		t.Error("Error string should include underlying error")
	}
}

func TestWrap_Nil(t *testing.T) {
	err := Wrap(nil, ErrCodeInternal, "test")

	if err != nil {
		t.Error("Wrap of nil should return nil")
	}
}

func TestWithContext(t *testing.T) {
	err := New(ErrCodeToolExecution, "tool failed")
	err.WithContext("tool", "go_test")
	err.WithContext("exit_code", 1)

	if err.Context["tool"] != "go_test" {
		t.Error("Context should contain 'tool' key")
	}

	if err.Context["exit_code"] != 1 {
		t.Error("Context should contain 'exit_code' key")
	}

	// Check that context appears in error string
	errStr := err.Error()
	if !strings.Contains(errStr, "tool") || !strings.Contains(errStr, "go_test") {
		t.Error("Error string should include context")
	}
}

func TestWithRetryable(t *testing.T) {
	err := New(ErrCodeModelTimeout, "request timed out")
	err.WithRetryable(true)

	if !err.Retryable {
		t.Error("WithRetryable should set Retryable to true")
	}

	if !err.IsRetryable() {
		t.Error("IsRetryable should return true")
	}
}

func TestError_String(t *testing.T) {
	err := New(ErrCodeConfigInvalid, "invalid config value")
	errStr := err.Error()

	// Should contain code
	if !strings.Contains(errStr, string(ErrCodeConfigInvalid)) {
		t.Error("Error string should contain error code")
	}

	// Should contain message
	if !strings.Contains(errStr, "invalid config value") {
		t.Error("Error string should contain message")
	}
}

func TestError_WithUnderlying(t *testing.T) {
	underlying := errors.New("file not found")
	err := Wrap(underlying, ErrCodeStorageRead, "failed to read")

	errStr := err.Error()

	if !strings.Contains(errStr, "file not found") {
		t.Error("Error string should include underlying error")
	}

	if !strings.Contains(errStr, "STORAGE_READ") {
		t.Error("Error string should include error code")
	}
}

func TestUnwrap(t *testing.T) {
	underlying := errors.New("underlying")
	err := Wrap(underlying, ErrCodeInternal, "wrapped")

	unwrapped := err.Unwrap()

	if unwrapped != underlying {
		t.Error("Unwrap should return underlying error")
	}
}

func TestIsCode(t *testing.T) {
	err := New(ErrCodeModelAPIError, "API error")

	if !IsCode(err, ErrCodeModelAPIError) {
		t.Error("IsCode should return true for matching code")
	}

	if IsCode(err, ErrCodeModelTimeout) {
		t.Error("IsCode should return false for non-matching code")
	}

	if IsCode(nil, ErrCodeModelAPIError) {
		t.Error("IsCode should return false for nil error")
	}

	stdErr := errors.New("standard error")
	if IsCode(stdErr, ErrCodeInternal) {
		t.Error("IsCode should return false for non-Buckley errors")
	}
}

func TestGetCode(t *testing.T) {
	err := New(ErrCodeToolTimeout, "timeout")

	code := GetCode(err)
	if code != ErrCodeToolTimeout {
		t.Errorf("GetCode = %v, want %v", code, ErrCodeToolTimeout)
	}

	// Nil error
	if GetCode(nil) != "" {
		t.Error("GetCode should return empty string for nil")
	}

	// Standard error
	stdErr := errors.New("standard")
	if GetCode(stdErr) != ErrCodeInternal {
		t.Error("GetCode should return ErrCodeInternal for non-Buckley errors")
	}
}

func TestIsRetryable_Function(t *testing.T) {
	retryable := New(ErrCodeModelRateLimit, "rate limited").WithRetryable(true)
	notRetryable := New(ErrCodeConfigInvalid, "bad config")

	if !IsRetryable(retryable) {
		t.Error("IsRetryable should return true for retryable error")
	}

	if IsRetryable(notRetryable) {
		t.Error("IsRetryable should return false for non-retryable error")
	}

	if IsRetryable(nil) {
		t.Error("IsRetryable should return false for nil")
	}

	stdErr := errors.New("standard")
	if IsRetryable(stdErr) {
		t.Error("IsRetryable should return false for non-Buckley errors")
	}
}

func TestStackTrace(t *testing.T) {
	err := New(ErrCodeInternal, "test error")

	trace := err.StackTrace()

	if trace == "" {
		t.Error("StackTrace should return non-empty string")
	}

	if !strings.Contains(trace, "Stack trace:") {
		t.Error("StackTrace should contain header")
	}

	// Should contain at least one frame
	if len(err.Stack) == 0 {
		t.Error("Stack should have frames")
	}
}

func TestFrame_String(t *testing.T) {
	frame := Frame{
		Function: "github.com/odvcencio/buckley/pkg/errors.TestFunc",
		File:     "/path/to/file.go",
		Line:     42,
	}

	str := frame.String()

	if str != frame.Function {
		t.Errorf("Frame.String() = %v, want %v", str, frame.Function)
	}
}

func TestCaptureStack(t *testing.T) {
	frames := captureStack(0)

	if len(frames) == 0 {
		t.Error("captureStack should return at least one frame")
	}

	// Should contain testing-related frames
	found := false
	for _, frame := range frames {
		if strings.Contains(frame.Function, "Test") || strings.Contains(frame.Function, "errors") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Stack should contain test or errors package frames")
	}
}

func TestMultipleContext(t *testing.T) {
	err := New(ErrCodeTaskFailed, "task execution failed")
	err.WithContext("task_id", "123")
	err.WithContext("attempt", 2)
	err.WithContext("reason", "timeout")

	if len(err.Context) != 3 {
		t.Errorf("Context should have 3 entries, got %d", len(err.Context))
	}

	errStr := err.Error()
	for _, key := range []string{"task_id", "attempt", "reason"} {
		if !strings.Contains(errStr, key) {
			t.Errorf("Error string should contain context key %q", key)
		}
	}
}

func TestChaining(t *testing.T) {
	// Test method chaining
	err := New(ErrCodeModelAPIError, "API failed").
		WithContext("model", "gpt-4").
		WithContext("status_code", 429).
		WithRetryable(true)

	if err.Code != ErrCodeModelAPIError {
		t.Error("Chaining should preserve code")
	}

	if len(err.Context) != 2 {
		t.Error("Chaining should add all context")
	}

	if !err.Retryable {
		t.Error("Chaining should set retryable")
	}
}

func TestErrorCodes_Defined(t *testing.T) {
	// Ensure all error codes are non-empty
	codes := []ErrorCode{
		ErrCodeConfigLoad,
		ErrCodeConfigParse,
		ErrCodeConfigInvalid,
		ErrCodeModelNotFound,
		ErrCodeModelInvalid,
		ErrCodeModelAPIError,
		ErrCodeModelTimeout,
		ErrCodeModelRateLimit,
		ErrCodeStorageRead,
		ErrCodeStorageWrite,
		ErrCodeStorageCorrupt,
		ErrCodeToolNotFound,
		ErrCodeToolExecution,
		ErrCodeToolTimeout,
		ErrCodePlanInvalid,
		ErrCodeTaskFailed,
		ErrCodeSelfHealFailed,
		ErrCodeBudgetExceeded,
		ErrCodeCostTracking,
		ErrCodeInternal,
		ErrCodeInvalidInput,
		ErrCodeNotImplemented,
	}

	for _, code := range codes {
		if code == "" {
			t.Error("Error code should not be empty")
		}
	}
}
