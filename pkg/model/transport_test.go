package model

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggingTransport_RoundTrip(t *testing.T) {
	// Create a temp directory for logs
	tmpDir := t.TempDir()
	oldDir := networkLogDir
	networkLogDir = tmpDir
	defer func() { networkLogDir = oldDir }()

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"status": "ok"}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	// Create transport and make request
	transport := NewLoggingTransport(nil)
	t.Cleanup(func() { _ = transport.Close() })

	req, err := http.NewRequest("POST", server.URL+"/test", bytes.NewReader([]byte(`{"input": "test"}`)))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test-12345678901234567890")
	req.Header.Set("Content-Type", "application/json")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	if string(body) != `{"status": "ok"}` {
		t.Errorf("unexpected body: %s", body)
	}

	// Verify log file was created
	logPath := filepath.Join(tmpDir, "network.jsonl")
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	logContent := string(logData)

	// Verify log contains expected fields
	if !strings.Contains(logContent, `"method":"POST"`) {
		t.Error("log should contain method")
	}
	if !strings.Contains(logContent, `"response_status":200`) {
		t.Error("log should contain response status")
	}
	if !strings.Contains(logContent, `"request_body"`) {
		t.Error("log should contain request body")
	}
	if !strings.Contains(logContent, `"response_body"`) {
		t.Error("log should contain response body")
	}
}

func TestLoggingTransport_RoundTripDisabledDoesNotWriteLog(t *testing.T) {
	tmpDir := t.TempDir()
	oldDir := networkLogDir
	networkLogDir = tmpDir
	defer func() { networkLogDir = oldDir }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"status": "ok"}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	transport := NewLoggingTransportWithEnabled(nil, false)
	t.Cleanup(func() { _ = transport.Close() })

	req, err := http.NewRequest("POST", server.URL+"/test", bytes.NewReader([]byte(`{"input": "test"}`)))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test-12345678901234567890")
	req.Header.Set("Content-Type", "application/json")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != `{"status": "ok"}` {
		t.Errorf("unexpected body: %s", body)
	}

	logPath := filepath.Join(tmpDir, "network.jsonl")
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("expected log file to not exist, but found %s", logPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat log file: %v", err)
	}
}

func TestSanitizeHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-test-12345678901234567890")
	headers.Set("X-Api-Key", "not-a-real-key")
	headers.Set("Content-Type", "application/json")

	result := sanitizeHeaders(headers)

	// Authorization should be fully redacted (header is canonicalized to "Authorization")
	if result["Authorization"] != "[REDACTED]" {
		t.Errorf("Authorization should be redacted, got: %s", result["Authorization"])
	}

	// X-Api-Key should be fully redacted (header is canonicalized to "X-Api-Key")
	if result["X-Api-Key"] != "[REDACTED]" {
		t.Errorf("X-Api-Key should be redacted, got: %s", result["X-Api-Key"])
	}

	// Content-Type should not be masked
	if result["Content-Type"] != "application/json" {
		t.Errorf("Content-Type should not be masked, got: %s", result["Content-Type"])
	}
}

func TestTruncateBody(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "short body",
			input:    "hello",
			expected: "hello",
		},
		{
			name:     "exactly at limit",
			input:    strings.Repeat("a", 10000),
			expected: strings.Repeat("a", 10000),
		},
		{
			name:     "over limit",
			input:    strings.Repeat("a", 10001),
			expected: strings.Repeat("a", 10000) + "\n...[truncated]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateBody(tt.input)
			if result != tt.expected {
				t.Errorf("expected length %d, got length %d", len(tt.expected), len(result))
			}
		})
	}
}
