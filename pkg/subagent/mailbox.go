package subagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"m31labs.dev/buckley/pkg/agentcoord"
)

// ChildMailboxEnv points a local Buckley child at its live command mailbox.
// The file is private, append-only, and removed by the parent runner.
const ChildMailboxEnv = "BUCKLEY_SUBAGENT_MAILBOX_V1"

const (
	maxMailboxMessageBytes = 64 * 1024
	maxMailboxFileBytes    = 8 * 1024 * 1024
)

// FileMailbox is the parent-side transport for commands to one local child.
type FileMailbox struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	closed bool
	bytes  int64
}

// NewFileMailbox creates a private mailbox outside the workspace.
func NewFileMailbox() (*FileMailbox, error) {
	file, err := os.CreateTemp("", "buckley-subagent-mailbox-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("create subagent mailbox: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, fmt.Errorf("secure subagent mailbox: %w", err)
	}
	return &FileMailbox{file: file, path: file.Name()}, nil
}

// Path returns the absolute path exported to the child process.
func (m *FileMailbox) Path() string {
	if m == nil {
		return ""
	}
	return m.path
}

// Append makes one complete command visible to the child reader.
func (m *FileMailbox) Append(message agentcoord.Message) error {
	if m == nil {
		return fmt.Errorf("subagent mailbox is unavailable")
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode subagent mailbox command: %w", err)
	}
	if len(payload) > maxMailboxMessageBytes {
		return fmt.Errorf("subagent mailbox command exceeds %d bytes", maxMailboxMessageBytes)
	}
	payload = append(payload, '\n')

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.file == nil {
		return fmt.Errorf("subagent mailbox is closed")
	}
	if m.bytes+int64(len(payload)) > maxMailboxFileBytes {
		return fmt.Errorf("subagent mailbox exceeds %d bytes", maxMailboxFileBytes)
	}
	written, err := m.file.Write(payload)
	if err != nil {
		return fmt.Errorf("append subagent mailbox command: %w", err)
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}
	m.bytes += int64(written)
	return nil
}

// Close closes and removes the private mailbox.
func (m *FileMailbox) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	file, path := m.file, m.path
	m.file = nil
	m.mu.Unlock()

	var closeErr error
	if file != nil {
		closeErr = file.Close()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) && closeErr == nil {
		closeErr = err
	}
	return closeErr
}

// FileMailboxReader incrementally reads complete commands without rescanning
// prior bytes. It is instantiated only inside a spawned Buckley child.
type FileMailboxReader struct {
	file    *os.File
	offset  int64
	pending []byte
}

// OpenChildMailboxFromEnv opens the mailbox supplied by a local parent.
// Present is false for ordinary Buckley processes, which pay no polling cost.
func OpenChildMailboxFromEnv() (reader *FileMailboxReader, present bool, err error) {
	path := strings.TrimSpace(os.Getenv(ChildMailboxEnv))
	if path == "" {
		return nil, false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Errorf("open subagent mailbox: %w", err)
	}
	return &FileMailboxReader{file: file}, true, nil
}

// ReadAvailable returns every complete command appended since the last call.
func (r *FileMailboxReader) ReadAvailable() ([]agentcoord.Message, error) {
	if r == nil || r.file == nil {
		return nil, nil
	}
	info, err := r.file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat subagent mailbox: %w", err)
	}
	if info.Size() < r.offset {
		return nil, fmt.Errorf("subagent mailbox was truncated")
	}
	if info.Size() > maxMailboxFileBytes {
		return nil, fmt.Errorf("subagent mailbox exceeds %d bytes", maxMailboxFileBytes)
	}
	if info.Size() > r.offset {
		chunk := make([]byte, info.Size()-r.offset)
		n, readErr := r.file.ReadAt(chunk, r.offset)
		if readErr != nil && readErr != io.EOF {
			return nil, fmt.Errorf("read subagent mailbox: %w", readErr)
		}
		r.offset += int64(n)
		r.pending = append(r.pending, chunk[:n]...)
	}

	messages := make([]agentcoord.Message, 0)
	for {
		newline := bytes.IndexByte(r.pending, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSpace(r.pending[:newline])
		r.pending = r.pending[newline+1:]
		if len(line) == 0 {
			continue
		}
		if len(line) > maxMailboxMessageBytes {
			return nil, fmt.Errorf("subagent mailbox command exceeds %d bytes", maxMailboxMessageBytes)
		}
		var message agentcoord.Message
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, fmt.Errorf("decode subagent mailbox command: %w", err)
		}
		message.Content = strings.TrimSpace(message.Content)
		if message.Content == "" {
			return nil, fmt.Errorf("subagent mailbox command content is required")
		}
		messages = append(messages, message)
	}
	if len(r.pending) > maxMailboxMessageBytes {
		return nil, fmt.Errorf("subagent mailbox command exceeds %d bytes", maxMailboxMessageBytes)
	}
	return messages, nil
}

// Close releases the child-side file descriptor. The parent owns removal.
func (r *FileMailboxReader) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
