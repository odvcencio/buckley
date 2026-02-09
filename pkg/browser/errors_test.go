package browser

import (
	"errors"
	"fmt"
	"testing"
)

func TestBrowserdError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *BrowserdError
		want string
	}{
		{
			name: "without wrapped error",
			err:  NewBrowserdError("timeout", "operation timed out"),
			want: "browserd error [timeout]: operation timed out",
		},
		{
			name: "with wrapped error",
			err:  WrapBrowserdError("connection_lost", "daemon died", errors.New("broken pipe")),
			want: "browserd error [connection_lost]: daemon died: broken pipe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBrowserdError_Unwrap(t *testing.T) {
	inner := errors.New("inner error")
	err := WrapBrowserdError("test", "msg", inner)
	if !errors.Is(err, inner) {
		t.Error("Unwrap should expose inner error via errors.Is")
	}

	err2 := NewBrowserdError("test", "msg")
	if err2.Unwrap() != nil {
		t.Error("Unwrap on error without inner should return nil")
	}
}

func TestIsConnectionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "ErrConnectionLost directly",
			err:  ErrConnectionLost,
			want: true,
		},
		{
			name: "wrapped ErrConnectionLost",
			err:  fmt.Errorf("something failed: %w", ErrConnectionLost),
			want: true,
		},
		{
			name: "BrowserdError with connection_lost code",
			err:  NewBrowserdError("connection_lost", "daemon connection lost"),
			want: true,
		},
		{
			name: "BrowserdError with unavailable code",
			err:  NewBrowserdError("unavailable", "service unavailable"),
			want: true,
		},
		{
			name: "BrowserdError with timeout code",
			err:  NewBrowserdError("timeout", "operation timed out"),
			want: false,
		},
		{
			name: "BrowserdError with other code",
			err:  NewBrowserdError("invalid_action", "bad action"),
			want: false,
		},
		{
			name: "ErrSessionClosed is not connection error",
			err:  ErrSessionClosed,
			want: false,
		},
		{
			name: "ErrOperationTimeout is not connection error",
			err:  ErrOperationTimeout,
			want: false,
		},
		{
			name: "ErrUnavailable is not connection error",
			err:  ErrUnavailable,
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("something went wrong"),
			want: false,
		},
		{
			name: "wrapped BrowserdError connection_lost",
			err:  fmt.Errorf("wrapper: %w", NewBrowserdError("connection_lost", "lost")),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsConnectionError(tt.err)
			if got != tt.want {
				t.Errorf("IsConnectionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "ErrConnectionLost is retryable",
			err:  ErrConnectionLost,
			want: true,
		},
		{
			name: "ErrOperationTimeout is retryable",
			err:  ErrOperationTimeout,
			want: true,
		},
		{
			name: "wrapped ErrConnectionLost is retryable",
			err:  fmt.Errorf("failed: %w", ErrConnectionLost),
			want: true,
		},
		{
			name: "wrapped ErrOperationTimeout is retryable",
			err:  fmt.Errorf("failed: %w", ErrOperationTimeout),
			want: true,
		},
		{
			name: "BrowserdError connection_lost is retryable",
			err:  NewBrowserdError("connection_lost", "lost connection"),
			want: true,
		},
		{
			name: "BrowserdError timeout is retryable",
			err:  NewBrowserdError("timeout", "operation timed out"),
			want: true,
		},
		{
			name: "BrowserdError unavailable is retryable",
			err:  NewBrowserdError("unavailable", "service unavailable"),
			want: true,
		},
		{
			name: "BrowserdError invalid_action is not retryable",
			err:  NewBrowserdError("invalid_action", "bad action"),
			want: false,
		},
		{
			name: "BrowserdError unknown code is not retryable",
			err:  NewBrowserdError("permission_denied", "no access"),
			want: false,
		},
		{
			name: "ErrSessionClosed is not retryable",
			err:  ErrSessionClosed,
			want: false,
		},
		{
			name: "ErrStaleState is not retryable",
			err:  ErrStaleState,
			want: false,
		},
		{
			name: "ErrNotImplemented is not retryable",
			err:  ErrNotImplemented,
			want: false,
		},
		{
			name: "ErrReconnectFailed is not retryable",
			err:  ErrReconnectFailed,
			want: false,
		},
		{
			name: "generic error is not retryable",
			err:  errors.New("something went wrong"),
			want: false,
		},
		{
			name: "wrapped BrowserdError timeout is retryable",
			err:  fmt.Errorf("wrap: %w", NewBrowserdError("timeout", "timed out")),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestSentinelErrors_AreDistinct(t *testing.T) {
	sentinels := []error{
		ErrUnavailable,
		ErrNotImplemented,
		ErrSessionClosed,
		ErrStaleState,
		ErrConnectionLost,
		ErrOperationTimeout,
		ErrReconnectFailed,
	}

	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinel errors should be distinct: %v == %v", a, b)
			}
		}
	}
}
