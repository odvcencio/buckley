package execmode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestReadLineRange_WholeLinesAndLimits(t *testing.T) {
	largeLine := strings.Repeat("x", maxReadBytes) + "\n"
	for _, tc := range []struct {
		name, source, want string
		start, end, next   int
		wantError          string
	}{
		{name: "empty", start: 1, end: 1},
		{name: "inclusive", source: "one\ntwo\nthree\nfour\n", start: 2, end: 3, want: "two\nthree\n"},
		{name: "past_eof", source: "one\n", start: 3, end: 5},
		{name: "last_unterminated", source: "one\ntwo", start: 2, end: 5, want: "two"},
		{name: "raw_bytes", source: "one\r\n界\r\n\xff\x00\n", start: 2, end: 3, want: "界\r\n\xff\x00\n"},
		{name: "skip_long_line", source: largeLine + "target\n", start: 2, end: 2, want: "target\n"},
		{name: "exact_page", source: strings.Repeat("x", maxReadBytes-1) + "\n", start: 1, end: 1, want: strings.Repeat("x", maxReadBytes-1) + "\n"},
		{name: "whole_line_page", source: strings.Repeat("x", maxReadBytes-2) + "\ny\n", start: 1, end: 2, want: strings.Repeat("x", maxReadBytes-2) + "\n", next: 2},
		{name: "oversized_first_line", source: largeLine, start: 1, end: 1, wantError: "use ReadFileChunk"},
		{name: "oversized_later_line", source: "first\n" + largeLine, start: 1, end: 2, want: "first\n", next: 2},
		{name: "scan_limit", source: strings.Repeat("x", maxLineReadScanBytes+1), start: 2, end: 2, wantError: "scan budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, next, err := readLineRange(context.Background(), strings.NewReader(tc.source), tc.start, tc.end)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || len(data) != 0 {
					t.Fatalf("data=%d next=%d err=%v", len(data), next, err)
				}
				return
			}
			if err != nil || string(data) != tc.want || next != tc.next || len(data) > maxReadBytes {
				t.Fatalf("data=%d next=%d err=%v; want data=%d next=%d", len(data), next, err, len(tc.want), tc.next)
			}
			if next != 0 && next <= tc.start {
				t.Fatalf("non-advancing page: %d", next)
			}
		})
	}
}

func TestReadLineRange_CancellationAndReadErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := readLineRange(ctx, iotest.ErrReader(io.ErrClosedPipe), 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	input := io.MultiReader(strings.NewReader("prefix\n"), iotest.ErrReader(io.ErrClosedPipe))
	data, _, err := readLineRange(context.Background(), input, 1, 2)
	if !errors.Is(err, io.ErrClosedPipe) || len(data) != 0 {
		t.Fatalf("partial read exposed: data=%q err=%v", data, err)
	}
}

func TestBrokerReadFileLines_ValidationAndPolicy(t *testing.T) {
	root := newWorkspace(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, token string
		denied, auditFail bool
		status            int
	}{
		{name: "valid", body: `{"path":"greeting.txt","start_line":"1","end_line":"1"}`, status: http.StatusOK},
		{name: "missing_range", body: `{"path":"greeting.txt"}`, status: http.StatusBadRequest},
		{name: "zero", body: `{"path":"greeting.txt","start_line":"0","end_line":"1"}`, status: http.StatusBadRequest},
		{name: "negative", body: `{"path":"greeting.txt","start_line":"-1","end_line":"1"}`, status: http.StatusBadRequest},
		{name: "reversed", body: `{"path":"greeting.txt","start_line":"2","end_line":"1"}`, status: http.StatusBadRequest},
		{name: "fraction", body: `{"path":"greeting.txt","start_line":"1.5","end_line":"2"}`, status: http.StatusBadRequest},
		{name: "overflow", body: `{"path":"greeting.txt","start_line":"9223372036854775808","end_line":"9223372036854775808"}`, status: http.StatusBadRequest},
		{name: "number", body: `{"path":"greeting.txt","start_line":1,"end_line":2}`, status: http.StatusBadRequest},
		{name: "null", body: `{"path":"greeting.txt","start_line":null,"end_line":"2"}`, status: http.StatusBadRequest},
		{name: "parent_escape", body: `{"path":"../outside.txt","start_line":"1","end_line":"1"}`, status: http.StatusBadRequest},
		{name: "absolute", body: `{"path":"/etc/passwd","start_line":"1","end_line":"1"}`, status: http.StatusBadRequest},
		{name: "symlink_escape", body: `{"path":"escape.txt","start_line":"1","end_line":"1"}`, status: http.StatusBadRequest},
		{name: "directory", body: `{"path":".","start_line":"1","end_line":"1"}`, status: http.StatusBadRequest},
		{name: "grant", body: `{"path":"greeting.txt","start_line":"1","end_line":"1"}`, denied: true, status: http.StatusForbidden},
		{name: "bad_token", body: `{"path":"greeting.txt","start_line":"1","end_line":"1"}`, token: "wrong", status: http.StatusUnauthorized},
		{name: "audit_failure", body: `{"path":"greeting.txt","start_line":"1","end_line":"1"}`, auditFail: true, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{fail: tc.auditFail}
			broker, err := NewBroker(root, sink)
			if err != nil {
				t.Fatal(err)
			}
			if tc.denied {
				WithCapabilities(CapFilesList)(broker)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/files/read-lines", strings.NewReader(tc.body))
			token := broker.Token()
			if tc.token != "" {
				token = tc.token
			}
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			broker.handleContext(CapFilesRead, broker.filesReadLines)(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.auditFail {
				if strings.Contains(response.Body.String(), "hello from") {
					t.Fatal("returned unaudited source")
				}
				return
			}
			records := sink.byMethod(CapFilesRead)
			if len(records) != 1 || (records[0].Outcome == "ok") != (tc.status == http.StatusOK) {
				t.Fatalf("audit=%+v", records)
			}
			if tc.status == http.StatusOK {
				var page struct {
					Data     []byte
					NextLine int `json:"next_line"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || string(page.Data) != "hello from the workspace\n" || page.NextLine != 0 {
					t.Fatalf("page=%+v err=%v", page, err)
				}
			}
		})
	}
}

func TestBrokerReadFileLines_CallBudget(t *testing.T) {
	root := newWorkspace(t)
	sink := &recordingSink{}
	broker, err := NewBroker(root, sink, WithCapabilityCallLimit(2))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/files/read-lines", strings.NewReader(`{"path":"greeting.txt","start_line":"1","end_line":"1"}`))
		request.Header.Set("Authorization", "Bearer "+broker.Token())
		response := httptest.NewRecorder()
		broker.handleContext(CapFilesRead, broker.filesReadLines)(response, request)
		want := http.StatusOK
		if i == 2 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("call=%d status=%d", i, response.Code)
		}
	}
	if records := sink.byMethod(CapFilesRead); len(records) != 3 || records[2].Outcome != "denied" {
		t.Fatalf("audit=%+v", records)
	}
}

func TestRunnerReadFileLines_PaginationPreservesBytes(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	root := t.TempDir()
	content := []byte(strings.Repeat("x", maxReadBytes-3) + "\r\n界\r\n\xff\x00\nlast")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	runner, err := NewRunner(root, sink, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), `package main
import("fmt";"crypto/sha256";"execprogram/caps")
func main(){
 h:=sha256.New();count:=0;
 for start:=1;start!=0;{
  data,next,err:=caps.ReadFileLines("source.txt",start,9);if err!=nil{panic(err)}
  h.Write(data);count+=len(data)
  if next!=0&&next<=start{panic(fmt.Sprint("non-advancing page ",next))};start=next
 }
 fmt.Printf("bytes=%d sha256=%x\n",count,h.Sum(nil))
}`)
	want := fmt.Sprintf("bytes=%d sha256=%x\n", len(content), sha256.Sum256(content))
	if err != nil || result.ExitCode != 0 || result.StdoutTruncated || result.Stdout != want {
		t.Fatalf("result bytes=%d truncated=%v exit=%d err=%v stderr=%s", len(result.Stdout), result.StdoutTruncated, result.ExitCode, err, result.Stderr)
	}
	if records := sink.byMethod(CapFilesRead); len(records) != 2 {
		t.Fatalf("audit=%+v", records)
	}
}
