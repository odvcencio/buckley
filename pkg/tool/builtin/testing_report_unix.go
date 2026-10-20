//go:build !windows

package builtin

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// A keeper keeps an unwritten pipe open until the test command finishes.
type testReportCapture struct {
	path           string
	reader, keeper *os.File
	buffer         reportBuffer
	done           chan struct{}
	err            error
}

// Segments avoid geometric buffer growth without reserving the full budget for small reports.
type reportBuffer struct {
	limit, size int
	chunks      [][]byte
	data        []byte
	truncated   bool
}

func (b *reportBuffer) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, b.limit-b.size)
	if keep > 0 {
		b.chunks = append(b.chunks, append([]byte(nil), p[:keep]...))
		b.size += keep
	}
	b.truncated = b.truncated || keep < n
	return n, nil
}
func (b *reportBuffer) bytes() []byte {
	if b.data != nil {
		return b.data
	}
	if len(b.chunks) == 1 {
		b.data = b.chunks[0]
	} else {
		b.data = make([]byte, b.size)
		offset := 0
		for _, chunk := range b.chunks {
			offset += copy(b.data[offset:], chunk)
		}
	}
	b.chunks = nil
	return b.data
}

func newTestReportCapture(path string, limit int64) (*testReportCapture, error) {
	if err := syscall.Mkfifo(path, 0600); err != nil {
		return nil, fmt.Errorf("create test report pipe: %w", err)
	}
	reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	keeper, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		reader.Close()
		return nil, err
	}
	c := &testReportCapture{path: path, reader: reader, keeper: keeper, buffer: reportBuffer{limit: int(limit)}, done: make(chan struct{})}
	go func() { _, c.err = io.Copy(&c.buffer, reader); close(c.done) }()
	return c, nil
}
func (c *testReportCapture) bytes(ctx context.Context) ([]byte, bool, error) {
	_ = c.keeper.Close()
	if err := ctx.Err(); err != nil {
		c.close()
		return nil, false, err
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-c.done:
	case <-ctx.Done():
		c.close()
		return nil, false, ctx.Err()
	case <-timer.C:
		c.close()
		return nil, false, fmt.Errorf("test report stream did not close after the command exited")
	}
	if c.err != nil {
		return nil, false, fmt.Errorf("read test report stream: %w", c.err)
	}
	info, err := os.Lstat(c.path)
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return nil, false, fmt.Errorf("test report pipe was replaced")
	}
	return c.buffer.bytes(), c.buffer.truncated, nil
}
func (c *testReportCapture) close() {
	_ = c.keeper.Close()
	_ = c.reader.Close()
	<-c.done
}
