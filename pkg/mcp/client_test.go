package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"
)

func TestNewClient_EmptyCommand(t *testing.T) {
	cfg := Config{
		Name:    "test",
		Command: "",
	}
	_, err := NewClient(cfg)
	if err == nil {
		t.Error("expected error for empty command")
	}
	if err.Error() != "command is required" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	// We can't easily test the default timeout is set since it happens
	// before we have access to the config, but we can verify it's used
	cfg := Config{
		Name:    "test",
		Command: "nonexistent-command-12345",
		Timeout: 0, // Should default to 30s
	}
	_, err := NewClient(cfg)
	// Command doesn't exist, so it should fail to start
	if err == nil {
		t.Error("expected error for nonexistent command")
	}
}

func TestConfig_Defaults(t *testing.T) {
	cfg := Config{
		Name:    "test",
		Command: "echo",
	}

	if cfg.Timeout != 0 {
		t.Errorf("expected zero timeout before initialization, got %v", cfg.Timeout)
	}

	if len(cfg.Args) != 0 {
		t.Errorf("expected empty args, got %v", cfg.Args)
	}

	if len(cfg.Env) != 0 {
		t.Errorf("expected empty env, got %v", cfg.Env)
	}
}

func TestMessage_JSONRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
	}{
		{
			name: "with ID and method",
			msg: func() Message {
				id := int64(42)
				return Message{
					JSONRPC: "2.0",
					ID:      &id,
					Method:  "test/method",
				}
			}(),
		},
		{
			name: "with result",
			msg: func() Message {
				id := int64(1)
				return Message{
					JSONRPC: "2.0",
					ID:      &id,
					Result:  json.RawMessage(`{"key": "value"}`),
				}
			}(),
		},
		{
			name: "with error",
			msg: func() Message {
				id := int64(1)
				return Message{
					JSONRPC: "2.0",
					ID:      &id,
					Error: &ErrorResponse{
						Code:    -32600,
						Message: "Invalid Request",
					},
				}
			}(),
		},
		{
			name: "notification (no ID)",
			msg: Message{
				JSONRPC: "2.0",
				Method:  "notifications/test",
			},
		},
		{
			name: "with params",
			msg: func() Message {
				id := int64(5)
				return Message{
					JSONRPC: "2.0",
					ID:      &id,
					Method:  "tools/call",
					Params:  json.RawMessage(`{"name": "test_tool"}`),
				}
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var decoded Message
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if decoded.JSONRPC != tt.msg.JSONRPC {
				t.Errorf("JSONRPC mismatch: got %v, want %v", decoded.JSONRPC, tt.msg.JSONRPC)
			}

			if (decoded.ID == nil) != (tt.msg.ID == nil) {
				t.Errorf("ID nil mismatch")
			} else if decoded.ID != nil && *decoded.ID != *tt.msg.ID {
				t.Errorf("ID mismatch: got %v, want %v", *decoded.ID, *tt.msg.ID)
			}

			if decoded.Method != tt.msg.Method {
				t.Errorf("Method mismatch: got %v, want %v", decoded.Method, tt.msg.Method)
			}
		})
	}
}

func TestErrorResponse_JSON(t *testing.T) {
	errResp := ErrorResponse{
		Code:    -32601,
		Message: "Method not found",
		Data:    map[string]any{"detail": "unknown method"},
	}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ErrorResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Code != -32601 {
		t.Errorf("Code = %v, want -32601", decoded.Code)
	}
	if decoded.Message != "Method not found" {
		t.Errorf("Message = %v, want 'Method not found'", decoded.Message)
	}
}

func TestToolsListResult_JSON(t *testing.T) {
	result := ToolsListResult{
		Tools: []ToolDefinition{
			{
				Name:        "read_file",
				Description: "Read a file",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "File path",
						},
					},
					"required": []any{"path"},
				},
			},
			{
				Name:        "write_file",
				Description: "Write a file",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
					"required": []any{"path", "content"},
				},
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ToolsListResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(decoded.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(decoded.Tools))
	}

	if decoded.Tools[0].Name != "read_file" {
		t.Errorf("first tool name = %v, want read_file", decoded.Tools[0].Name)
	}
}

func TestToolCallParams_JSON(t *testing.T) {
	params := ToolCallParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "test query",
			"limit": 10,
		},
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ToolCallParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Name != "search" {
		t.Errorf("Name = %v, want search", decoded.Name)
	}

	if decoded.Arguments["query"] != "test query" {
		t.Errorf("query = %v, want 'test query'", decoded.Arguments["query"])
	}
}

func TestToolCallResult_JSON(t *testing.T) {
	tests := []struct {
		name   string
		result ToolCallResult
	}{
		{
			name: "success with text",
			result: ToolCallResult{
				Content: []ContentBlock{
					{Type: "text", Text: "Success message"},
				},
				IsError: false,
			},
		},
		{
			name: "error result",
			result: ToolCallResult{
				Content: []ContentBlock{
					{Type: "text", Text: "Error occurred"},
				},
				IsError: true,
			},
		},
		{
			name: "with image",
			result: ToolCallResult{
				Content: []ContentBlock{
					{Type: "image", Data: "base64encoded", MimeType: "image/png"},
				},
			},
		},
		{
			name: "mixed content",
			result: ToolCallResult{
				Content: []ContentBlock{
					{Type: "text", Text: "Here is the image:"},
					{Type: "image", Data: "abc123", MimeType: "image/jpeg"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.result)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var decoded ToolCallResult
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if decoded.IsError != tt.result.IsError {
				t.Errorf("IsError = %v, want %v", decoded.IsError, tt.result.IsError)
			}

			if len(decoded.Content) != len(tt.result.Content) {
				t.Errorf("Content length = %d, want %d", len(decoded.Content), len(tt.result.Content))
			}
		})
	}
}

func TestResourcesListResult_JSON(t *testing.T) {
	result := ResourcesListResult{
		Resources: []Resource{
			{
				URI:         "file:///tmp/test.txt",
				Name:        "test.txt",
				Description: "A test file",
				MimeType:    "text/plain",
			},
			{
				URI:      "memory://data",
				Name:     "Memory Data",
				MimeType: "application/json",
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ResourcesListResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(decoded.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(decoded.Resources))
	}

	if decoded.Resources[0].URI != "file:///tmp/test.txt" {
		t.Errorf("first resource URI = %v", decoded.Resources[0].URI)
	}
}

// MockClient is a test helper that allows testing client methods without a real subprocess
type mockReadWriteCloser struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	closed   bool
	mu       sync.Mutex
}

func newMockReadWriteCloser() *mockReadWriteCloser {
	return &mockReadWriteCloser{
		readBuf:  new(bytes.Buffer),
		writeBuf: new(bytes.Buffer),
	}
}

func (m *mockReadWriteCloser) Read(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, io.EOF
	}
	return m.readBuf.Read(p)
}

func (m *mockReadWriteCloser) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	return m.writeBuf.Write(p)
}

func (m *mockReadWriteCloser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockReadWriteCloser) setResponse(msg Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, _ := json.Marshal(msg)
	m.readBuf.Write(data)
	m.readBuf.WriteByte('\n')
}

func TestClient_NextID(t *testing.T) {
	client := &Client{
		pending: make(map[int64]chan *Message),
	}

	ids := make([]int64, 100)
	var wg sync.WaitGroup

	// Generate IDs concurrently
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ids[idx] = client.nextID()
		}(i)
	}
	wg.Wait()

	// Verify all IDs are unique
	seen := make(map[int64]bool)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate ID generated: %d", id)
		}
		seen[id] = true
	}

	// Verify they're all positive
	for _, id := range ids {
		if id <= 0 {
			t.Errorf("expected positive ID, got %d", id)
		}
	}
}

func TestClient_ServerID(t *testing.T) {
	client := &Client{
		serverID: "test-server-123",
		pending:  make(map[int64]chan *Message),
	}

	if client.ServerID() != "test-server-123" {
		t.Errorf("ServerID() = %v, want test-server-123", client.ServerID())
	}
}

func TestClient_ServerInfo(t *testing.T) {
	info := &ServerInfo{
		Name:        "Test Server",
		Version:     "1.0.0",
		ProtocolVer: "2024-11-05",
	}

	client := &Client{
		serverInfo: info,
		pending:    make(map[int64]chan *Message),
	}

	if client.ServerInfo() != info {
		t.Error("ServerInfo() did not return correct info")
	}

	// Test nil case
	client2 := &Client{pending: make(map[int64]chan *Message)}
	if client2.ServerInfo() != nil {
		t.Error("expected nil ServerInfo for uninitialized client")
	}
}

func TestClient_Tools(t *testing.T) {
	tools := []ToolDefinition{
		{Name: "tool1", Description: "First tool"},
		{Name: "tool2", Description: "Second tool"},
	}

	client := &Client{
		tools:   tools,
		pending: make(map[int64]chan *Message),
	}

	result := client.Tools()
	if len(result) != 2 {
		t.Errorf("expected 2 tools, got %d", len(result))
	}

	if result[0].Name != "tool1" {
		t.Errorf("first tool name = %v, want tool1", result[0].Name)
	}
}

func TestClient_Resources(t *testing.T) {
	resources := []Resource{
		{URI: "file:///a", Name: "A"},
		{URI: "file:///b", Name: "B"},
	}

	client := &Client{
		resources: resources,
		pending:   make(map[int64]chan *Message),
	}

	result := client.Resources()
	if len(result) != 2 {
		t.Errorf("expected 2 resources, got %d", len(result))
	}
}

func TestClient_Close_AlreadyClosed(t *testing.T) {
	stdin := newMockReadWriteCloser()
	stdout := newMockReadWriteCloser()
	stderr := newMockReadWriteCloser()

	client := &Client{
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		pending: make(map[int64]chan *Message),
		closed:  true, // Already closed
	}

	// Should return nil when already closed
	err := client.Close()
	if err != nil {
		t.Errorf("expected nil error for already closed client, got %v", err)
	}
}

func TestClient_CallContextCancellation(t *testing.T) {
	stdin := newMockReadWriteCloser()
	stdout := newMockReadWriteCloser()

	client := &Client{
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int64]chan *Message),
	}

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Start reading goroutine (simulating readResponses)
	go func() {
		// Don't send any response, let the context cancellation take effect
		time.Sleep(100 * time.Millisecond)
	}()

	_, err := client.call(ctx, "test/method", nil)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Verify pending request was cleaned up
	client.mu.Lock()
	pendingCount := len(client.pending)
	client.mu.Unlock()

	if pendingCount != 0 {
		t.Errorf("expected 0 pending requests after cancellation, got %d", pendingCount)
	}
}

func TestClient_CallWithParams(t *testing.T) {
	stdin := newMockReadWriteCloser()
	stdout := newMockReadWriteCloser()

	client := &Client{
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int64]chan *Message),
	}

	// Set up response
	id := int64(1)
	respMsg := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Result:  json.RawMessage(`{"key": "value"}`),
	}

	// Start a goroutine to simulate response after a delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		client.mu.Lock()
		if ch, ok := client.pending[id]; ok {
			ch <- &respMsg
		}
		client.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	params := map[string]string{"param1": "value1"}
	resp, err := client.call(ctx, "test/method", params)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected response, got nil")
	}

	if *resp.ID != id {
		t.Errorf("response ID = %v, want %v", *resp.ID, id)
	}
}

func TestClient_CallWithNilParams(t *testing.T) {
	stdin := newMockReadWriteCloser()
	stdout := newMockReadWriteCloser()

	client := &Client{
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int64]chan *Message),
	}

	// Set up response
	id := int64(1)
	respMsg := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Result:  json.RawMessage(`{}`),
	}

	// Start a goroutine to simulate response
	go func() {
		time.Sleep(50 * time.Millisecond)
		client.mu.Lock()
		if ch, ok := client.pending[id]; ok {
			ch <- &respMsg
		}
		client.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := client.call(ctx, "test/method", nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp == nil {
		t.Fatal("expected response")
	}
}

func TestClient_CallWriteError(t *testing.T) {
	stdout := newMockReadWriteCloser()

	// Close stdin to simulate write error
	stdin := newMockReadWriteCloser()
	stdin.Close()

	client := &Client{
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int64]chan *Message),
	}

	ctx := context.Background()
	_, err := client.call(ctx, "test/method", nil)

	if err == nil {
		t.Error("expected error for closed stdin")
	}
}

// TestReadResponses tests the response reading goroutine
func TestReadResponses_ValidMessage(t *testing.T) {
	// Create a pipe for stdout simulation
	pr, pw := io.Pipe()

	client := &Client{
		stdout:  pr,
		pending: make(map[int64]chan *Message),
	}

	// Set up a pending request
	id := int64(42)
	respCh := make(chan *Message, 1)
	client.mu.Lock()
	client.pending[id] = respCh
	client.mu.Unlock()

	// Start reading responses
	go client.readResponses()

	// Write a valid response
	msg := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Result:  json.RawMessage(`{"success": true}`),
	}
	data, _ := json.Marshal(msg)
	pw.Write(append(data, '\n'))

	// Wait for response
	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("expected response, got nil")
		}
		if *resp.ID != id {
			t.Errorf("response ID = %v, want %v", *resp.ID, id)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}

	pw.Close()
}

func TestReadResponses_EmptyLine(t *testing.T) {
	pr, pw := io.Pipe()

	client := &Client{
		stdout:  pr,
		pending: make(map[int64]chan *Message),
	}

	go client.readResponses()

	// Write empty lines (should be skipped)
	pw.Write([]byte("\n\n\n"))

	// Write a valid message after
	id := int64(1)
	respCh := make(chan *Message, 1)
	client.mu.Lock()
	client.pending[id] = respCh
	client.mu.Unlock()

	msg := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Result:  json.RawMessage(`{}`),
	}
	data, _ := json.Marshal(msg)
	pw.Write(append(data, '\n'))

	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("expected response")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	pw.Close()
}

func TestReadResponses_InvalidJSON(t *testing.T) {
	pr, pw := io.Pipe()

	client := &Client{
		stdout:  pr,
		pending: make(map[int64]chan *Message),
	}

	go client.readResponses()

	// Write invalid JSON (should be skipped)
	pw.Write([]byte("not valid json\n"))

	// Write a valid message
	id := int64(1)
	respCh := make(chan *Message, 1)
	client.mu.Lock()
	client.pending[id] = respCh
	client.mu.Unlock()

	msg := Message{
		JSONRPC: "2.0",
		ID:      &id,
	}
	data, _ := json.Marshal(msg)
	pw.Write(append(data, '\n'))

	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("expected response")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	pw.Close()
}

func TestReadResponses_Notification(t *testing.T) {
	pr, pw := io.Pipe()

	client := &Client{
		stdout:  pr,
		pending: make(map[int64]chan *Message),
	}

	go client.readResponses()

	// Write a notification (no ID)
	msg := Message{
		JSONRPC: "2.0",
		Method:  "notification/test",
	}
	data, _ := json.Marshal(msg)
	pw.Write(append(data, '\n'))

	// This should not panic - notifications are ignored
	time.Sleep(50 * time.Millisecond)
	pw.Close()
}

func TestReadResponses_UnknownID(t *testing.T) {
	pr, pw := io.Pipe()

	client := &Client{
		stdout:  pr,
		pending: make(map[int64]chan *Message),
	}

	go client.readResponses()

	// Write response with ID that has no pending request
	id := int64(999)
	msg := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Result:  json.RawMessage(`{}`),
	}
	data, _ := json.Marshal(msg)
	pw.Write(append(data, '\n'))

	// Should not panic
	time.Sleep(50 * time.Millisecond)
	pw.Close()
}

// mockPipeClient creates a client with pipes for stdin/stdout for testing
func newMockPipeClient(serverID string) (*Client, *io.PipeWriter, *io.PipeReader) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()

	client := &Client{
		stdin:    stdinWriter,
		stdout:   stdoutReader,
		stderr:   newMockReadWriteCloser(),
		pending:  make(map[int64]chan *Message),
		serverID: serverID,
	}

	// Start reading responses
	go client.readResponses()

	return client, stdoutWriter, stdinReader
}

func TestClient_Initialize(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	// Set up a goroutine to respond to the initialize request
	go func() {
		// Read the request
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		// Send response
		result := map[string]any{
			"serverInfo": map[string]any{
				"name":    "TestServer",
				"version": "1.0.0",
			},
			"protocolVersion": "2024-11-05",
			"instructions":    "Test instructions",
		}
		resultBytes, _ := json.Marshal(result)
		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultBytes,
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))

		// Also consume the notifications/initialized message to prevent blocking
		stdinReader.Read(buf)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	info := client.ServerInfo()
	if info == nil {
		t.Fatal("expected server info")
	}

	if info.Name != "TestServer" {
		t.Errorf("server name = %v, want TestServer", info.Name)
	}

	if info.Version != "1.0.0" {
		t.Errorf("version = %v, want 1.0.0", info.Version)
	}

	if info.ProtocolVer != "2024-11-05" {
		t.Errorf("protocol = %v, want 2024-11-05", info.ProtocolVer)
	}
}

func TestClient_Initialize_Error(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	// Set up a goroutine to respond with an error
	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &ErrorResponse{
				Code:    -32600,
				Message: "Initialize failed",
			},
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := client.Initialize(ctx)
	if err == nil {
		t.Error("expected error from Initialize")
	}
}

func TestClient_ListTools(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		result := ToolsListResult{
			Tools: []ToolDefinition{
				{Name: "tool1", Description: "Tool 1"},
				{Name: "tool2", Description: "Tool 2"},
			},
		}
		resultBytes, _ := json.Marshal(result)
		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultBytes,
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}

	// Verify cached
	cachedTools := client.Tools()
	if len(cachedTools) != 2 {
		t.Errorf("expected 2 cached tools, got %d", len(cachedTools))
	}
}

func TestClient_ListTools_Error(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &ErrorResponse{
				Code:    -32601,
				Message: "tools/list not supported",
			},
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.ListTools(ctx)
	if err == nil {
		t.Error("expected error")
	}
}

func TestClient_CallTool(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		result := ToolCallResult{
			Content: []ContentBlock{
				{Type: "text", Text: "Tool executed successfully"},
			},
			IsError: false,
		}
		resultBytes, _ := json.Marshal(result)
		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultBytes,
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := client.CallTool(ctx, "test_tool", map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if result.IsError {
		t.Error("expected IsError=false")
	}

	if len(result.Content) != 1 {
		t.Errorf("expected 1 content block, got %d", len(result.Content))
	}

	if result.Content[0].Text != "Tool executed successfully" {
		t.Errorf("content text = %v", result.Content[0].Text)
	}
}

func TestClient_CallTool_Error(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &ErrorResponse{
				Code:    -32602,
				Message: "Tool not found",
			},
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.CallTool(ctx, "unknown", nil)
	if err == nil {
		t.Error("expected error")
	}
}

func TestClient_ListResources(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		result := ResourcesListResult{
			Resources: []Resource{
				{URI: "file:///a", Name: "A"},
				{URI: "file:///b", Name: "B"},
			},
		}
		resultBytes, _ := json.Marshal(result)
		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultBytes,
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resources, err := client.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}

	if len(resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(resources))
	}

	// Verify cached
	cached := client.Resources()
	if len(cached) != 2 {
		t.Errorf("expected 2 cached resources, got %d", len(cached))
	}
}

func TestClient_ListResources_Error(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &ErrorResponse{
				Code:    -32601,
				Message: "Not supported",
			},
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.ListResources(ctx)
	if err == nil {
		t.Error("expected error")
	}
}

func TestClient_ReadResource(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		result := map[string]any{
			"contents": []ContentBlock{
				{Type: "text", Text: "File content here"},
			},
		}
		resultBytes, _ := json.Marshal(result)
		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultBytes,
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	contents, err := client.ReadResource(ctx, "file:///test.txt")
	if err != nil {
		t.Fatalf("ReadResource failed: %v", err)
	}

	if len(contents) != 1 {
		t.Errorf("expected 1 content block, got %d", len(contents))
	}

	if contents[0].Text != "File content here" {
		t.Errorf("content = %v", contents[0].Text)
	}
}

func TestClient_ReadResource_Error(t *testing.T) {
	client, stdoutWriter, stdinReader := newMockPipeClient("test-server")
	defer stdinReader.Close()
	defer stdoutWriter.Close()

	go func() {
		buf := make([]byte, 4096)
		n, _ := stdinReader.Read(buf)
		var req Message
		json.Unmarshal(buf[:n], &req)

		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &ErrorResponse{
				Code:    -32602,
				Message: "Resource not found",
			},
		}
		respData, _ := json.Marshal(resp)
		stdoutWriter.Write(append(respData, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.ReadResource(ctx, "file:///nonexistent")
	if err == nil {
		t.Error("expected error")
	}
}
