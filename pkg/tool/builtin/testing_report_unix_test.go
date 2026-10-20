//go:build !windows

package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReportBuffer_FragmentedWrites(t *testing.T) {
	const limit = 1 << 20
	buffer := reportBuffer{limit: limit}
	for i := 0; i < limit+1; i++ {
		_, _ = buffer.Write([]byte{byte(i)})
	}
	capacity := 0
	for _, chunk := range buffer.chunks {
		capacity += cap(chunk)
	}
	if capacity > limit || len(buffer.chunks) > 64 {
		t.Fatalf("fragmented capture grew metadata: capacity=%d segments=%d", capacity, len(buffer.chunks))
	}
	data := buffer.bytes()
	if len(data) != limit || !buffer.truncated {
		t.Fatalf("bytes=%d truncated=%v", len(data), buffer.truncated)
	}
	for i, value := range data {
		if value != byte(i) {
			t.Fatalf("byte %d=%d", i, value)
		}
	}
}

func TestTestReportCapture_StreamsWithoutDiskPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results")
	capture, err := newTestReportCapture(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capture.close)
	ready := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			done <- err
			return
		}
		defer file.Close()
		chunk := []byte(strings.Repeat("x", 32<<10))
		for i := 0; i < 512; i++ {
			if _, err := file.Write(chunk); err != nil {
				done <- err
				return
			}
			if i == 255 {
				close(ready)
				<-resume
			}
		}
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-ready:
	case <-ctx.Done():
		close(resume)
		t.Fatal("report writer stalled")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Size() != 0 || info.Sys().(*syscall.Stat_t).Blocks != 0 {
		close(resume)
		t.Fatalf("report uses disk payload: info=%+v err=%v", info, err)
	}
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("oversized writer was not drained")
	}
	raw, exceeded, err := capture.bytes(ctx)
	if err != nil || !exceeded || len(raw) != 1024 || string(raw) != strings.Repeat("x", 1024) {
		t.Fatalf("bytes=%d exceeded=%v err=%v", len(raw), exceeded, err)
	}
}
func TestTestReportCapture_EmptyAndCanceled(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			capture, err := newTestReportCapture(filepath.Join(t.TempDir(), "results"), 1024)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(capture.close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			raw, exceeded, err := capture.bytes(ctx)
			if canceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil || exceeded || len(raw) != 0 {
				t.Fatalf("bytes=%d exceeded=%v err=%v", len(raw), exceeded, err)
			}
			select {
			case <-capture.done:
			default:
				t.Fatal("collector still running")
			}
		})
	}
}
func TestTestReportCapture_InheritedWriterDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results")
	capture, err := newTestReportCapture(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capture.close)
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err = capture.bytes(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-capture.done:
	default:
		t.Fatal("collector leaked after deadline")
	}
}
func TestTestReportCapture_ReplacedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results")
	capture, err := newTestReportCapture(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capture.close)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "<testsuites/>")
	_, _, err = capture.bytes(context.Background())
	if err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("err=%v", err)
	}
}
