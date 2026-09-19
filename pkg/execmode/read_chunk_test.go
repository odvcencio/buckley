package execmode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBrokerReadFileChunk(t *testing.T) {
	root := t.TempDir()
	content := []byte(strings.Repeat("x", maxReadBytes-1) + "界\nneedle-beyond-cap\n\xff\x00")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	broker, err := NewBroker(root, sink)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "caps.sock")
	if err := broker.Start(socket); err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	for _, offset := range []int{0, maxReadBytes, len(content), len(content) + 5} {
		t.Run(strconv.Itoa(offset), func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"path": "source.txt", "offset": strconv.Itoa(offset)})
			resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", string(body))
			if err != nil || resp.status != http.StatusOK {
				t.Fatalf("status=%d err=%v", resp.status, err)
			}
			var out struct {
				Data      []byte
				Truncated bool
			}
			if err := json.Unmarshal([]byte(resp.body), &out); err != nil {
				t.Fatal(err)
			}
			start := min(offset, len(content))
			end := min(start+maxReadBytes, len(content))
			if !bytes.Equal(out.Data, content[start:end]) || out.Truncated != (end < len(content)) {
				t.Fatalf("offset=%d got bytes=%d truncated=%v want bytes=%d truncated=%v", offset, len(out.Data), out.Truncated, end-start, end < len(content))
			}
		})
	}
	for _, size := range []int{0, maxReadBytes} {
		name := fmt.Sprintf("size-%d", size)
		if err := os.WriteFile(filepath.Join(root, name), content[:size], 0600); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"path": name, "offset": "0"})
		resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", string(body))
		if err != nil || resp.status != http.StatusOK {
			t.Fatalf("size=%d status=%d err=%v", size, resp.status, err)
		}
		var out struct {
			Data      []byte
			Truncated bool
		}
		if err := json.Unmarshal([]byte(resp.body), &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Data, content[:size]) || out.Truncated {
			t.Fatalf("size=%d returned bytes=%d truncated=%v", size, len(out.Data), out.Truncated)
		}
	}
	for _, offset := range []any{-1, nil, "-1", "1.5", "9223372036854775808", "invalid"} {
		body, _ := json.Marshal(map[string]any{"path": "source.txt", "offset": offset})
		resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", string(body))
		if err != nil || resp.status != http.StatusBadRequest || !strings.Contains(resp.body, "offset") {
			t.Fatalf("offset=%v status=%d err=%v", offset, resp.status, err)
		}
	}
	resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", `{"path":"source.txt"}`)
	if err != nil || resp.status != http.StatusOK {
		t.Fatalf("legacy status=%d err=%v", resp.status, err)
	}
	var legacy map[string]any
	if err := json.Unmarshal([]byte(resp.body), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy["content"] == nil || legacy["truncated"] != true || legacy["data"] != nil {
		t.Fatalf("legacy response shape changed")
	}
	for _, path := range []string{"../escape", "/etc/passwd"} {
		body, _ := json.Marshal(map[string]any{"path": path, "offset": "0"})
		resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", string(body))
		if err != nil || resp.status != http.StatusBadRequest {
			t.Fatalf("escape %q status=%d err=%v", path, resp.status, err)
		}
	}
	records := sink.byMethod(CapFilesRead)
	if len(records) != 15 {
		t.Fatalf("audited %d calls, want 15", len(records))
	}
}

func TestBrokerReadFileChunkBudget(t *testing.T) {
	root := newWorkspace(t)
	sink := &recordingSink{}
	broker, err := NewBroker(root, sink, WithCapabilityCallLimit(2))
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "caps.sock")
	if err := broker.Start(socket); err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	for i := 0; i < 3; i++ {
		resp, err := capsHTTPCall(t, socket, broker.Token(), "/v1/files/read", fmt.Sprintf(`{"path":"greeting.txt","offset":"%d"}`, i))
		want := http.StatusOK
		if i == 2 {
			want = http.StatusTooManyRequests
		}
		if err != nil || resp.status != want {
			t.Fatalf("call %d status=%d err=%v", i, resp.status, err)
		}
	}
	records := sink.byMethod(CapFilesRead)
	if len(records) != 3 || records[2].Outcome != "denied" {
		t.Fatalf("audit=%+v", records)
	}
}

func TestRunnerReadFileChunksPreserveBytes(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	root := t.TempDir()
	content := []byte(strings.Repeat("x", maxReadBytes-1) + "界\nneedle-beyond-cap\n\xff\x00")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	runner, err := NewRunner(root, sink, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const program = `package main
import (
 "crypto/sha256"
 "fmt"
 "execprogram/caps"
)
func main() {
 h:=sha256.New()
 var offset int64
 for {
  data, more, err:=caps.ReadFileChunk("source.txt",offset)
  if err!=nil { panic(err) }
  h.Write(data)
  offset+=int64(len(data))
  if !more { break }
  if len(data)==0 { panic("non-advancing read") }
 }
 fmt.Printf("bytes=%d sha256=%x\n",offset,h.Sum(nil))
}`
	result, err := runner.Run(context.Background(), program)
	want := fmt.Sprintf("bytes=%d sha256=%x\n", len(content), sha256.Sum256(content))
	if err != nil || result.ExitCode != 0 || result.Stdout != want {
		t.Fatalf("result=%+v err=%v want=%s", result, err, want)
	}
	records := sink.byMethod(CapFilesRead)
	if len(records) != 2 {
		t.Fatalf("audit=%+v", records)
	}
	for i, record := range records {
		var params map[string]any
		if err := json.Unmarshal([]byte(record.Params), &params); err != nil {
			t.Fatal(err)
		}
		if record.Outcome != "ok" || params["offset"] != strconv.Itoa(i*maxReadBytes) {
			t.Fatalf("record=%+v", record)
		}
	}
}

func TestCapsAPICardReadFileChunkGuidance(t *testing.T) {
	for _, want := range []string{"ReadFileChunk", "len(data)", "truncated", "32 broker operations", "do not pin a file snapshot"} {
		if !strings.Contains(CapsAPICard, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
