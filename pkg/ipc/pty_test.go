package ipc

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
)

func TestIntToUint16(t *testing.T) {
	tests := []struct {
		name   string
		value  int
		want   uint16
		wantOK bool
	}{
		{"zero", 0, 0, false},
		{"negative", -1, 0, false},
		{"one", 1, 1, true},
		{"max uint16", 65535, 65535, true},
		{"over max uint16", 65536, 0, false},
		{"large negative", -65536, 0, false},
		{"typical terminal rows", 24, 24, true},
		{"typical terminal cols", 80, 80, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := intToUint16(tt.value)
			if ok != tt.wantOK {
				t.Errorf("intToUint16(%d) ok = %v, wantOK %v", tt.value, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("intToUint16(%d) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil error", nil, 0},
		{"generic error", errors.New("something failed"), -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exitCode(tt.err)
			if got != tt.want {
				t.Errorf("exitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestExitCodeWithExitError(t *testing.T) {
	// Create a command that will fail with a known exit code
	cmd := exec.Command("sh", "-c", "exit 42")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected command to fail")
	}

	got := exitCode(err)
	if got != 42 {
		t.Errorf("exitCode() = %d, want 42", got)
	}
}

func TestMarshalPTYError(t *testing.T) {
	testErr := errors.New("test error message")
	result := marshalPTYError(testErr)

	var msg ptyMessage
	if err := json.Unmarshal(result, &msg); err != nil {
		t.Fatalf("failed to unmarshal PTY error: %v", err)
	}

	if msg.Type != "error" {
		t.Errorf("expected type=error, got %s", msg.Type)
	}
	if msg.Data != "test error message" {
		t.Errorf("expected data=%q, got %q", "test error message", msg.Data)
	}
}

func TestHttpError(t *testing.T) {
	rr := httptest.NewRecorder()
	httpError(rr, "something went wrong", http.StatusBadRequest)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}

	// The response body should contain the error message
	body := rr.Body.String()
	if body == "" {
		t.Error("expected non-empty body")
	}
}

func TestBuildPTYCommandDefaultShell(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws/pty", nil)
	cmd := buildPTYCommand(req)

	if cmd == nil {
		t.Fatal("expected command to be created")
	}
	if cmd.Path == "" {
		t.Error("expected command path to be set")
	}
}

func TestBuildPTYCommandWithCustomCommand(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws/pty?cmd=echo+hello", nil)
	cmd := buildPTYCommand(req)

	if cmd == nil {
		t.Fatal("expected command to be created")
	}
	if cmd.Args[0] != "echo" {
		t.Errorf("expected first arg=echo, got %s", cmd.Args[0])
	}
	if len(cmd.Args) < 2 || cmd.Args[1] != "hello" {
		t.Errorf("expected second arg=hello, got %v", cmd.Args)
	}
}

func TestBuildPTYCommandWithCwd(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws/pty?cwd=/tmp", nil)
	cmd := buildPTYCommand(req)

	if cmd == nil {
		t.Fatal("expected command to be created")
	}
	if cmd.Dir != "/tmp" {
		t.Errorf("expected dir=/tmp, got %s", cmd.Dir)
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantRows int
		wantCols int
	}{
		{"no size", "", 0, 0},
		{"rows only", "rows=24", 24, 0},
		{"cols only", "cols=80", 0, 80},
		{"both", "rows=24&cols=80", 24, 80},
		{"invalid rows", "rows=abc&cols=80", 0, 80},
		{"invalid cols", "rows=24&cols=abc", 24, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?"+tt.query, nil)
			rows, cols := parseSize(req)
			if rows != tt.wantRows {
				t.Errorf("parseSize() rows = %d, want %d", rows, tt.wantRows)
			}
			if cols != tt.wantCols {
				t.Errorf("parseSize() cols = %d, want %d", cols, tt.wantCols)
			}
		})
	}
}
