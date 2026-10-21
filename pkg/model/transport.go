package model

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/paths"
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

const (
	maxNetworkLogBytes  = 8 << 20
	maxNetworkBodyBytes = 10000
)

// LoggingTransport captures bounded provider diagnostics without consuming bodies.
type LoggingTransport struct {
	base    http.RoundTripper
	logPath string
	mu      sync.Mutex
	enabled bool
	closed  bool
}

type networkBodyCapture struct {
	original  io.ReadCloser
	mu        sync.Mutex
	prefix    []byte
	readBytes int64
	expected  int64
	eof       bool
	onDone    func()
	once      sync.Once
}

func (c *networkBodyCapture) Read(dst []byte) (int, error) {
	n, err := c.original.Read(dst)
	c.mu.Lock()
	if remaining := maxNetworkBodyBytes + 1 - len(c.prefix); remaining > 0 {
		c.prefix = append(c.prefix, dst[:min(n, remaining)]...)
	}
	c.readBytes += int64(n)
	c.eof = c.eof || err == io.EOF
	c.mu.Unlock()
	if err != nil {
		c.finish()
	}
	return n, err
}

func (c *networkBodyCapture) Close() error {
	err := c.original.Close()
	c.finish()
	return err
}

func (c *networkBodyCapture) finish() {
	c.once.Do(func() {
		if c.onDone != nil {
			c.onDone()
		}
	})
}

func (c *networkBodyCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	text := truncateBody(string(c.prefix))
	if len(c.prefix) <= maxNetworkBodyBytes && !c.eof && (c.expected <= 0 || c.readBytes < c.expected) {
		text += "\n...[capture incomplete]"
	}
	return text
}

// networkLogDir is the directory for network logs
var networkLogDir = paths.BuckleyLogsBaseDir()

// defaultTransport is lazily initialized
var (
	defaultTransportOnce sync.Once
	defaultTransport     *http.Transport
)

// getDefaultTransport returns the optimized default transport.
// This function is kept for backward compatibility with existing code.
func getDefaultTransport() *http.Transport {
	defaultTransportOnce.Do(func() {
		defaultTransport = DefaultTransport()
	})
	return defaultTransport
}

// NewLoggingTransport creates a new logging transport
func NewLoggingTransport(base http.RoundTripper) *LoggingTransport {
	return NewLoggingTransportWithEnabled(base, true)
}

// NewLoggingTransportWithEnabled creates a new logging transport and optionally
// disables request/response logging entirely.
func NewLoggingTransportWithEnabled(base http.RoundTripper, enabled bool) *LoggingTransport {
	if base == nil {
		base = getDefaultTransport()
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

	path := filepath.Join(networkLogDir, "network.jsonl")
	if err := appendNetworkLog(path, nil); err == nil {
		t.logPath = path
	}
}

// RoundTrip implements http.RoundTripper
func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	t.mu.Lock()
	capture := t.enabled && t.logPath != "" && !t.closed
	t.mu.Unlock()
	if !capture {
		return t.base.RoundTrip(req)
	}
	entry := NetworkLogEntry{
		Timestamp: time.Now(), Method: boundErrorText(req.Method, 32),
		URL: boundErrorText(req.URL.String(), 2048), RequestHeaders: sanitizeHeaders(req.Header),
	}
	var requestCapture *networkBodyCapture
	if req.Body != nil && req.Body != http.NoBody {
		requestCapture = &networkBodyCapture{original: req.Body, expected: req.ContentLength}
		req.Body = requestCapture
	}
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	entry.Duration = time.Since(start)
	if requestCapture != nil {
		entry.RequestBody = requestCapture.text()
	}
	if err != nil {
		entry.Error = boundErrorText(err.Error(), 4096)
		t.log(entry)
		return nil, err
	}
	entry.ResponseStatus, entry.ResponseHeaders = resp.StatusCode, sanitizeHeaders(resp.Header)
	isStreaming := strings.Contains(req.Header.Get("Accept"), "text/event-stream") || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	if isStreaming {
		entry.ResponseBody = "[streaming - body not captured]"
		t.log(entry)
	} else if resp.Body == nil || resp.Body == http.NoBody {
		t.log(entry)
	} else {
		responseCapture := &networkBodyCapture{original: resp.Body, expected: resp.ContentLength}
		responseCapture.onDone = func() {
			entry.ResponseBody = responseCapture.text()
			t.log(entry)
		}
		resp.Body = responseCapture
	}
	return resp, nil
}

func (t *LoggingTransport) log(entry NetworkLogEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.logPath == "" {
		return
	}
	_ = appendNetworkLog(t.logPath, data)
}

// Close stops new diagnostic writes; it never takes ownership of active bodies.
func (t *LoggingTransport) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	return nil
}

// A stable lock file coordinates providers and processes. Each append opens the
// canonical log, so no writer keeps an old inode alive across retention cleanup.
func appendNetworkLog(path string, record []byte) error {
	if len(record) > maxNetworkLogBytes {
		return nil
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	locked, err := tryLockNetworkLog(lock)
	if err != nil || !locked {
		return err
	}
	defer unlockNetworkLog(lock)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("network log is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_ = file.Chmod(0600)
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(record)) > maxNetworkLogBytes {
		if err := file.Truncate(0); err != nil {
			return err
		}
		info, err = file.Stat()
		if err != nil {
			return err
		}
	}
	if len(record) > 0 {
		var n int
		n, err = file.Write(record)
		if err != nil || n != len(record) {
			_ = file.Truncate(info.Size())
			if err == nil {
				err = io.ErrShortWrite
			}
		}
	}
	return err
}

// sanitizeHeaders converts headers to a map, masking sensitive values
func sanitizeHeaders(headers http.Header) map[string]string {
	keys := make([]string, 0, 33)
	for key := range headers {
		i := sort.SearchStrings(keys, key)
		if i >= 32 {
			continue
		}
		keys = append(keys, "")
		copy(keys[i+1:], keys[i:])
		keys[i] = key
		if len(keys) > 32 {
			keys = keys[:32]
		}
	}
	result := make(map[string]string)
	for i, key := range keys {
		if i == 32 {
			break
		}
		name := boundErrorText(key, 256)
		if strings.EqualFold(key, "authorization") || strings.EqualFold(key, "x-api-key") {
			result[name] = "[REDACTED]"
			continue
		}
		var value strings.Builder
		for j, part := range headers[key] {
			if value.Len() > 512 {
				break
			}
			if j > 0 {
				value.WriteString(", ")
			}
			value.WriteString(part[:min(len(part), max(0, 513-value.Len()))])
		}
		result[name] = boundErrorText(value.String(), 512)
	}
	return result
}

func truncateBody(body string) string {
	if len(body) <= maxNetworkBodyBytes {
		return body
	}
	end := maxNetworkBodyBytes
	for end > 0 && !utf8.RuneStart(body[end]) {
		end--
	}
	return body[:end] + "\n...[truncated]"
}
