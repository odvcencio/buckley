package browser

import (
	"errors"
	"fmt"
)

var (
	ErrUnavailable    = errors.New("browser runtime unavailable")
	ErrNotImplemented = errors.New("browser runtime not implemented")
	ErrSessionClosed  = errors.New("browser session closed")
	ErrStaleState     = errors.New("stale state version")
	ErrConnectionLost = errors.New("browserd connection lost")
	ErrOperationTimeout = errors.New("operation timeout")
	ErrReconnectFailed = errors.New("reconnection failed")
)

// BrowserdError wraps errors from the browserd daemon with additional context.
type BrowserdError struct {
	Code    string
	Message string
	Err     error
}

func (e *BrowserdError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("browserd error [%s]: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("browserd error [%s]: %s", e.Code, e.Message)
}

func (e *BrowserdError) Unwrap() error {
	return e.Err
}

// NewBrowserdError creates a new BrowserdError.
func NewBrowserdError(code, message string) *BrowserdError {
	return &BrowserdError{Code: code, Message: message}
}

// WrapBrowserdError wraps an existing error with browserd context.
func WrapBrowserdError(code, message string, err error) *BrowserdError {
	return &BrowserdError{Code: code, Message: message, Err: err}
}

// IsConnectionError returns true if the error indicates a lost connection.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnectionLost) {
		return true
	}
	var browserdErr *BrowserdError
	if errors.As(err, &browserdErr) {
		return browserdErr.Code == "connection_lost" || browserdErr.Code == "unavailable"
	}
	return false
}

// IsRetryableError returns true if the error might succeed on retry.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnectionLost) || errors.Is(err, ErrOperationTimeout) {
		return true
	}
	var browserdErr *BrowserdError
	if errors.As(err, &browserdErr) {
		switch browserdErr.Code {
		case "connection_lost", "timeout", "unavailable":
			return true
		}
	}
	return false
}
