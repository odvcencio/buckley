package model

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/paths"
)

// NetworkLogEntry represents a single network request/response log entry
type NetworkLogEntry struct {
	Timestamp       time.Time         `json:"timestamp"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	RequestBody     string            `json:"request_body,omitempty"`
	ResponseStatus  int               `json:"response_status,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body,omitempty"`
	Duration        time.Duration     `json:"duration_ms"`
	Error           string            `json:"error,omitempty"`
}

// LoggingTransport is an http.RoundTripper that logs all requests and responses
type LoggingTransport struct {
	base    http.RoundTripper
	logFile *os.File
	mu      sync.Mutex
	enabled bool
}

// networkLogDir is the directory for network logs
var networkLogDir = paths.BuckleyLogsBaseDir()

// NewLoggingTransport creates a new logging transport
func NewLoggingTransport(base http.RoundTripper) *LoggingTransport {
	return NewLoggingTransportWithEnabled(base, true)
}

// NewLoggingTransportWithEnabled creates a new logging transport and optionally
// disables request/response logging entirely.
func NewLoggingTransportWithEnabled(base http.RoundTripper, enabled bool) *LoggingTransport {
	if base == nil {
		base = http.DefaultTransport
	}

	lt := &LoggingTransport{base: base, enabled: enabled}
	if lt.enabled {
		lt.initLogFile()
	}
	return lt
}

func (t *LoggingTransport) initLogFile() {
	// Create log directory if it doesn't exist
	if err := os.MkdirAll(networkLogDir, 0o700); err != nil {
		return
	}
	_ = os.Chmod(networkLogDir, 0o700)

	logPath := filepath.Join(networkLogDir, "network.jsonl")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_ = f.Chmod(0o600)
	t.logFile = f
}

// RoundTrip implements http.RoundTripper
func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	if !t.enabled || t.logFile == nil {
		return t.base.RoundTrip(req)
	}

	entry := NetworkLogEntry{
		Timestamp:      time.Now(),
		Method:         req.Method,
		URL:            req.URL.String(),
		RequestHeaders: sanitizeHeaders(req.Header),
	}

	// Detect streaming requests - don't buffer these
	isStreaming := req.Header.Get("Accept") == "text/event-stream"

	// Capture request body if present
	if req.Body != nil && req.Body != http.NoBody {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			entry.RequestBody = truncateBody(string(bodyBytes))
			// Restore the body for the actual request
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
	}

	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	entry.Duration = time.Since(start)

	if err != nil {
		entry.Error = err.Error()
		t.log(entry)
		return nil, err
	}

	entry.ResponseStatus = resp.StatusCode
	entry.ResponseHeaders = sanitizeHeaders(resp.Header)

	// Skip response body capture for streaming requests - this would block
	// until the entire stream completes, defeating the purpose of streaming
	if !isStreaming && resp.Body != nil {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr == nil {
			entry.ResponseBody = truncateBody(string(bodyBytes))
			// Restore the body for the caller
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
	} else if isStreaming {
		entry.ResponseBody = "[streaming - body not captured]"
	}

	t.log(entry)
	return resp, nil
}

func (t *LoggingTransport) log(entry NetworkLogEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.logFile == nil {
		return
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	t.logFile.Write(data)
	t.logFile.Write([]byte("\n"))
}

// Close closes the log file
func (t *LoggingTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.logFile != nil {
		return t.logFile.Close()
	}
	return nil
}

// sanitizeHeaders converts headers to a map, masking sensitive values
func sanitizeHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)
	for key, values := range headers {
		lowerKey := strings.ToLower(key)
		if lowerKey == "authorization" || lowerKey == "x-api-key" {
			result[key] = "[REDACTED]"
		} else {
			result[key] = strings.Join(values, ", ")
		}
	}
	return result
}

// truncateBody limits body size for logging
func truncateBody(body string) string {
	const maxLen = 10000
	if len(body) > maxLen {
		return body[:maxLen] + "\n...[truncated]"
	}
	return body
}
