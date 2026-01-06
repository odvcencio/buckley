package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestReadMessage verifies reading JSON-RPC messages with Content-Length headers
func TestReadMessage(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *JSONRPCMessage
		wantErr bool
	}{
		{
			name: "valid request message",
			input: "Content-Length: 74\r\n\r\n" +
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":1234}}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      mustMarshalRaw(1),
				Method:  "initialize",
				Params:  json.RawMessage(`{"processId":1234}`),
			},
			wantErr: false,
		},
		{
			name: "valid notification message",
			input: "Content-Length: 40\r\n\r\n" +
				`{"jsonrpc":"2.0","method":"initialized"}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "initialized",
			},
			wantErr: false,
		},
		{
			name: "valid response message",
			input: "Content-Length: 45\r\n\r\n" +
				`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      mustMarshalRaw(1),
				Result:  json.RawMessage(`{"ok":true}`),
			},
			wantErr: false,
		},
		{
			name: "message with multiple headers",
			input: "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
				"Content-Length: 40\r\n\r\n" +
				`{"jsonrpc":"2.0","method":"initialized"}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "initialized",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tt.input))
			got, err := ReadMessage(reader)

			if (err != nil) != tt.wantErr {
				t.Errorf("ReadMessage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if got.JSONRPC != tt.want.JSONRPC {
					t.Errorf("JSONRPC = %v, want %v", got.JSONRPC, tt.want.JSONRPC)
				}
				if got.Method != tt.want.Method {
					t.Errorf("Method = %v, want %v", got.Method, tt.want.Method)
				}
				if tt.want.ID != nil {
					if got.ID == nil {
						t.Errorf("ID = nil, want %v", string(*tt.want.ID))
					} else if string(*got.ID) != string(*tt.want.ID) {
						t.Errorf("ID = %v, want %v", string(*got.ID), string(*tt.want.ID))
					}
				}
				if len(tt.want.Params) > 0 && string(got.Params) != string(tt.want.Params) {
					t.Errorf("Params = %v, want %v", string(got.Params), string(tt.want.Params))
				}
				if len(tt.want.Result) > 0 && string(got.Result) != string(tt.want.Result) {
					t.Errorf("Result = %v, want %v", string(got.Result), string(tt.want.Result))
				}
			}
		})
	}
}

// TestReadMessage_InvalidHeader tests handling of malformed headers
func TestReadMessage_InvalidHeader(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "missing content-length",
			input: "Content-Type: application/json\r\n\r\n{}",
		},
		{
			name:  "invalid content-length format",
			input: "Content-Length: abc\r\n\r\n{}",
		},
		{
			name:  "malformed header line",
			input: "InvalidHeaderLine\r\n\r\n{}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tt.input))
			_, err := ReadMessage(reader)

			if err == nil {
				t.Error("ReadMessage() expected error, got nil")
			}
		})
	}
}

// TestReadMessage_TruncatedBody tests handling of incomplete message body
func TestReadMessage_TruncatedBody(t *testing.T) {
	input := "Content-Length: 100\r\n\r\n" +
		`{"jsonrpc":"2.0","id":1}` // Only 25 bytes, but header says 100

	reader := bufio.NewReader(strings.NewReader(input))
	_, err := ReadMessage(reader)

	if err == nil {
		t.Error("ReadMessage() expected error for truncated body, got nil")
	}
}

// TestWriteMessage verifies writing JSON-RPC messages with headers
func TestWriteMessage(t *testing.T) {
	tests := []struct {
		name       string
		msg        *JSONRPCMessage
		wantHeader string
		wantBody   string
	}{
		{
			name: "request message",
			msg: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      mustMarshalRaw(1),
				Method:  "initialize",
				Params:  json.RawMessage(`{"processId":1234}`),
			},
			wantHeader: "Content-Length: 74\r\n\r\n",
			wantBody:   `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":1234}}`,
		},
		{
			name: "response message",
			msg: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      mustMarshalRaw(1),
				Result:  json.RawMessage(`{"ok":true}`),
			},
			wantHeader: "Content-Length: 45\r\n\r\n",
			wantBody:   `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		},
		{
			name: "notification message",
			msg: &JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "initialized",
			},
			wantHeader: "Content-Length: 40\r\n\r\n",
			wantBody:   `{"jsonrpc":"2.0","method":"initialized"}`,
		},
		{
			name: "auto-add jsonrpc version",
			msg: &JSONRPCMessage{
				ID:     mustMarshalRaw(1),
				Method: "test",
			},
			wantHeader: "Content-Length: 40\r\n\r\n",
			wantBody:   `{"jsonrpc":"2.0","id":1,"method":"test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WriteMessage(&buf, tt.msg)

			if err != nil {
				t.Errorf("WriteMessage() error = %v", err)
				return
			}

			got := buf.String()
			if !strings.HasPrefix(got, tt.wantHeader) {
				t.Errorf("WriteMessage() header = %q, want %q", got[:len(tt.wantHeader)], tt.wantHeader)
			}

			body := got[len(tt.wantHeader):]
			if body != tt.wantBody {
				t.Errorf("WriteMessage() body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

// TestHandleMessage_Request tests routing requests to handlers
func TestHandleMessage_Request(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	tests := []struct {
		name         string
		msg          *JSONRPCMessage
		wantErr      bool
		wantErrCode  int
		wantResultOK bool
	}{
		{
			name: "initialize request",
			msg: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      mustMarshalRaw(1),
				Method:  "initialize",
				Params:  json.RawMessage(`{"processId":1234,"rootUri":"file:///test"}`),
			},
			wantErr:      false,
			wantResultOK: true,
		},
		{
			name: "initialize request with invalid params",
			msg: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      mustMarshalRaw(2),
				Method:  "initialize",
				Params:  json.RawMessage(`invalid json`),
			},
			wantErr:     true,
			wantErrCode: InvalidParams,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			response := bridge.HandleMessage(ctx, tt.msg)

			if response == nil {
				t.Fatal("HandleMessage() returned nil response for request")
			}

			if response.ID == nil {
				t.Error("HandleMessage() response missing ID")
			}

			if tt.wantErr {
				if response.Error == nil {
					t.Error("HandleMessage() expected error, got nil")
				} else if response.Error.Code != tt.wantErrCode {
					t.Errorf("HandleMessage() error code = %d, want %d", response.Error.Code, tt.wantErrCode)
				}
			} else {
				if response.Error != nil {
					t.Errorf("HandleMessage() unexpected error: %v", response.Error)
				}
				if tt.wantResultOK && len(response.Result) == 0 {
					t.Error("HandleMessage() expected result, got empty")
				}
			}
		})
	}
}

// TestHandleMessage_Notification tests routing notifications
func TestHandleMessage_Notification(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	// First initialize
	ctx := context.Background()
	initMsg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      mustMarshalRaw(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"processId":1234,"rootUri":"file:///test"}`),
	}
	initResp := bridge.HandleMessage(ctx, initMsg)
	if initResp.Error != nil {
		t.Fatalf("Initialize failed: %v", initResp.Error)
	}

	tests := []struct {
		name string
		msg  *JSONRPCMessage
	}{
		{
			name: "initialized notification",
			msg: &JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "initialized",
			},
		},
		{
			name: "unknown notification (should be silently ignored)",
			msg: &JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "textDocument/didOpen",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := bridge.HandleMessage(ctx, tt.msg)

			if response != nil {
				t.Error("HandleMessage() expected nil response for notification")
			}
		})
	}
}

// TestHandleMessage_InvalidJSON tests JSON parse error handling
func TestHandleMessage_InvalidJSON(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      mustMarshalRaw(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{invalid json}`),
	}

	ctx := context.Background()
	response := bridge.HandleMessage(ctx, msg)

	if response == nil {
		t.Fatal("HandleMessage() returned nil response")
	}

	if response.Error == nil {
		t.Error("HandleMessage() expected error for invalid JSON")
	}

	if response.Error != nil && response.Error.Code != InvalidParams {
		t.Errorf("HandleMessage() error code = %d, want %d", response.Error.Code, InvalidParams)
	}
}

// TestHandleMessage_MethodNotFound tests unknown method handling
func TestHandleMessage_MethodNotFound(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      mustMarshalRaw(1),
		Method:  "unknownMethod",
		Params:  json.RawMessage(`{}`),
	}

	ctx := context.Background()
	response := bridge.HandleMessage(ctx, msg)

	if response == nil {
		t.Fatal("HandleMessage() returned nil response")
	}

	if response.Error == nil {
		t.Error("HandleMessage() expected error for unknown method")
	}

	if response.Error != nil && response.Error.Code != MethodNotFound {
		t.Errorf("HandleMessage() error code = %d, want %d", response.Error.Code, MethodNotFound)
	}
}

// TestHandleMessage_Initialize tests initialize request handling
func TestHandleMessage_Initialize(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      mustMarshalRaw(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"processId":1234,"rootUri":"file:///test","capabilities":{}}`),
	}

	ctx := context.Background()
	response := bridge.HandleMessage(ctx, msg)

	if response == nil {
		t.Fatal("HandleMessage() returned nil response")
	}

	if response.Error != nil {
		t.Errorf("HandleMessage() unexpected error: %v", response.Error)
	}

	if len(response.Result) == 0 {
		t.Error("HandleMessage() expected result, got empty")
	}

	// Parse result to verify structure
	var result InitializeResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Errorf("Failed to parse result: %v", err)
	}

	if result.ServerInfo == nil {
		t.Error("Initialize result missing ServerInfo")
	}

	if result.ServerInfo != nil && result.ServerInfo.Name != "buckley-acp-bridge" {
		t.Errorf("ServerInfo.Name = %v, want %v", result.ServerInfo.Name, "buckley-acp-bridge")
	}
}

// TestHandleMessage_Shutdown tests shutdown request handling
func TestHandleMessage_Shutdown(t *testing.T) {
	config := &BridgeConfig{
		CoordinatorAddr: "localhost:50051",
		AgentID:         "test-agent",
	}
	bridge, err := NewBridge(config)
	if err != nil {
		t.Fatalf("NewBridge() error = %v", err)
	}

	ctx := context.Background()

	// First initialize
	initMsg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      mustMarshalRaw(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"processId":1234,"rootUri":"file:///test"}`),
	}
	initResp := bridge.HandleMessage(ctx, initMsg)
	if initResp.Error != nil {
		t.Fatalf("Initialize failed: %v", initResp.Error)
	}

	// Then initialized notification
	initializedMsg := &JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "initialized",
	}
	bridge.HandleMessage(ctx, initializedMsg)

	// Now shutdown
	shutdownMsg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      mustMarshalRaw(2),
		Method:  "shutdown",
	}

	response := bridge.HandleMessage(ctx, shutdownMsg)

	if response == nil {
		t.Fatal("HandleMessage() returned nil response")
	}

	if response.Error != nil {
		t.Errorf("HandleMessage() unexpected error: %v", response.Error)
	}

	if string(response.Result) != "null" {
		t.Errorf("HandleMessage() result = %v, want null", string(response.Result))
	}
}

// Helper function to marshal JSON values into json.RawMessage
func mustMarshalRaw(v interface{}) *json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	raw := json.RawMessage(data)
	return &raw
}
