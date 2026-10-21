package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLoggingTransport_DoesNotReadResponseBeforeCaller(t *testing.T) {
	body := &networkLogFlood{remaining: 1 << 20}
	transport, path := profileNetworkTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
	}))
	req, _ := http.NewRequest("GET", "http://fixture.invalid", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil || body.read != 0 {
		t.Fatalf("logging read a response before the caller: %v read=%d", err, body.read)
	}
	if _, err := io.CopyN(io.Discard, resp.Body, 100); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_ = resp.Body.Close()
	data, err := os.ReadFile(path)
	if err != nil || bytes.Count(data, []byte{'\n'}) != 1 || !bytes.Contains(data, []byte("capture incomplete")) || !body.closed {
		t.Fatal("partial close lost its marker, logged twice, or lost close ownership")
	}
}

func TestLoggingTransport_StreamHeaderAndConcurrentClose(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			transport, path := profileNetworkTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
				header := make(http.Header)
				if stream {
					header.Set("Content-Type", "text/event-stream; charset=utf-8")
				}
				return &http.Response{StatusCode: 200, Header: header, Body: reader}, nil
			}))
			req, _ := http.NewRequest("GET", "http://fixture.invalid", nil)
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := io.Copy(io.Discard, resp.Body); done <- err }()
			_ = resp.Body.Close()
			select {
			case err := <-done:
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("close changed the original read error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("logging prevented concurrent close from unblocking a read")
			}
			data, err := os.ReadFile(path)
			if err != nil || bytes.Count(data, []byte{'\n'}) != 1 || bytes.Contains(data, []byte("streaming - body not captured")) != stream {
				t.Fatal("stream detection or single log finalization failed")
			}
		})
	}
}

func TestLoggingTransport_LockContentionDropsOnlyDiagnostics(t *testing.T) {
	transport, path := profileNetworkTransport(t, nil)
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if locked, err := tryLockNetworkLog(lock); err != nil || !locked {
		t.Fatalf("lock fixture: %v", err)
	}
	done := make(chan struct{})
	go func() { transport.log(NetworkLogEntry{URL: "dropped"}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		unlockNetworkLog(lock)
		t.Fatal("diagnostic lock blocked a writer")
	}
	unlockNetworkLog(lock)
	transport.log(NetworkLogEntry{URL: "retained"})
	_ = transport.Close()
	transport.log(NetworkLogEntry{URL: "after-close"})
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("dropped")) || bytes.Contains(data, []byte("after-close")) || !bytes.Contains(data, []byte("retained")) {
		t.Fatal("lock/close handling lost the retained diagnostic")
	}
}

func TestLoggingTransport_WriterProcess(t *testing.T) {
	dir := os.Getenv("BUCKLEY_TEST_NETWORK_LOG_PROCESS")
	if dir == "" {
		t.Skip("subprocess fixture")
	}
	networkLogDir = dir
	transport := NewLoggingTransport(nil)
	defer transport.Close()
	for i := range 300 {
		transport.log(NetworkLogEntry{URL: fmt.Sprintf("process/%d/%d", os.Getpid(), i), RequestBody: strings.Repeat("x", 10000), ResponseBody: strings.Repeat("y", 10000)})
	}
}

func TestLoggingTransport_MultipleProcessesStayWithinBudget(t *testing.T) {
	transport, path := profileNetworkTransport(t, nil)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var children []*exec.Cmd
	for range 4 {
		child := exec.CommandContext(t.Context(), binary, "-test.run=^TestLoggingTransport_WriterProcess$", "-test.count=1")
		child.Env = append(os.Environ(), "BUCKLEY_TEST_NETWORK_LOG_PROCESS="+filepath.Dir(path))
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if child.ProcessState == nil {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
		})
		children = append(children, child)
	}
	for _, child := range children {
		if err := child.Wait(); err != nil {
			t.Fatalf("writer process: %v", err)
		}
	}
	transport.log(NetworkLogEntry{URL: "newest-parent-marker"})
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 8<<20 {
		t.Fatal("concurrent writers exceeded budget or lost final record")
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	var last NetworkLogEntry
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil || last.URL != "newest-parent-marker" {
		t.Fatal("concurrent writers lost the newest complete record")
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatal("concurrent writers interleaved/partially wrote JSON lines")
		}
	}
	t.Logf("writer_processes=4 retained_entries=%d disk_bytes=%d json_lines_valid=true final_marker=true", len(lines), len(data))
}

func TestLoggingTransport_ExistingOversizedFileAndSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			transport, path := profileNetworkTransport(t, nil)
			_ = transport.Close()
			file, err := os.OpenFile(path, os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(16 << 20); err != nil {
				t.Fatal(err)
			}
			_ = file.Close()
			if symlink {
				foreign := filepath.Join(t.TempDir(), "foreign.log")
				if err := os.Rename(path, foreign); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			next := NewLoggingTransport(nil)
			t.Cleanup(func() { _ = next.Close() })
			info, err := os.Stat(path)
			if err != nil || (info.Size() > 8<<20) != symlink {
				t.Fatal("retention failed or modified a symlink target")
			}
		})
	}
}

func TestLoggingTransport_HeaderAndUnicodeCaptureLimits(t *testing.T) {
	header := make(http.Header)
	for i := range 1000 {
		header.Set(fmt.Sprintf("X-%04d", i), strings.Repeat("λ", 1000))
	}
	header.Set("Authorization", "synthetic-private-key")
	result := sanitizeHeaders(header)
	if len(result) > 32 || result["Authorization"] != "[REDACTED]" {
		t.Fatal("header count/redaction budget failed")
	}
	for key, value := range result {
		if len(key) > 256 || len(value) > 512 || !utf8.ValidString(value) {
			t.Fatal("header capture exceeded byte budget or split UTF-8")
		}
	}
	if !utf8.ValidString(truncateBody(strings.Repeat("λ", 10001))) {
		t.Fatal("body capture split UTF-8")
	}
}
