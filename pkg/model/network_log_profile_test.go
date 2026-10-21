package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type networkLogFlood struct {
	remaining int64
	read      int64
	closed    bool
}

func (r *networkLogFlood) Read(dst []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(dst)), r.remaining)
	for i := range dst[:n] {
		dst[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return int(n), nil
}

func (r *networkLogFlood) Close() error { r.closed = true; return nil }

func profileNetworkTransport(t *testing.T, base http.RoundTripper) (*LoggingTransport, string) {
	t.Helper()
	oldDir := networkLogDir
	dir := t.TempDir()
	networkLogDir = dir
	t.Cleanup(func() { networkLogDir = oldDir })
	transport := NewLoggingTransport(base)
	t.Cleanup(func() { _ = transport.Close() })
	return transport, filepath.Join(dir, "network.jsonl")
}

func TestLoggingTransport_PayloadTransparencyProfile(t *testing.T) {
	const payloadBytes = (50 << 20) + 257
	requestBody := &networkLogFlood{remaining: payloadBytes}
	responseBody := &networkLogFlood{remaining: payloadBytes}
	var requestBytes int64
	transport, _ := profileNetworkTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		requestBytes, err = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: responseBody}, err
	}))
	request, _ := http.NewRequest("POST", "http://fixture.invalid/chat/completions", requestBody)
	request.ContentLength = payloadBytes
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBytes, err := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	runtime.ReadMemStats(&after)
	if err != nil || closeErr != nil {
		t.Fatalf("response read/close: %v %v", err, closeErr)
	}
	t.Logf("offered_each_bytes=%d request_delivered_bytes=%d response_delivered_bytes=%d allocated_bytes=%d original_request_closed=%t original_response_closed=%t", payloadBytes, requestBytes, responseBytes, after.TotalAlloc-before.TotalAlloc, requestBody.closed, responseBody.closed)
	if requestBytes != payloadBytes || responseBytes != payloadBytes || !requestBody.closed || !responseBody.closed {
		t.Error("network logging changed payloads or lost close ownership")
	}
}

func TestLoggingTransport_DiskRetentionProfile(t *testing.T) {
	transport, path := profileNetworkTransport(t, nil)
	entry := NetworkLogEntry{RequestBody: strings.Repeat("x", 10000), ResponseBody: strings.Repeat("y", 10000)}
	for i := range 1000 {
		entry.URL = fmt.Sprintf("http://fixture.invalid/%04d", i)
		transport.log(entry)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatal("retention produced a partial log entry")
		}
	}
	var last NetworkLogEntry
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil || last.URL != "http://fixture.invalid/0999" {
		t.Fatal("retention lost the newest entry")
	}
	t.Logf("written_entries=1000 retained_entries=%d log_bytes=%d json_lines_valid=true newest_entry=0999", len(lines), len(data))
	if len(data) > 8<<20 {
		t.Error("network log exceeded disk budget")
	}
}
