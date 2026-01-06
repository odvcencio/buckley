package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Transport handles JSON-RPC message reading and writing over stdio.
type Transport struct {
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex
}

// NewTransport creates a new transport over the given reader and writer.
func NewTransport(reader io.Reader, writer io.Writer) *Transport {
	return &Transport{
		reader: bufio.NewReader(reader),
		writer: writer,
	}
}

// ReadMessage reads a single JSON-RPC message from the transport.
// Messages are newline-delimited JSON.
func (t *Transport) ReadMessage() (json.RawMessage, error) {
	line, err := t.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	// Trim trailing newline
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}

	if len(line) == 0 {
		// Skip empty lines
		return t.ReadMessage()
	}

	return json.RawMessage(line), nil
}

// WriteMessage writes a JSON-RPC message to the transport.
// Thread-safe.
func (t *Transport) WriteMessage(msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if _, err := t.writer.Write(data); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if _, err := t.writer.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}

	return nil
}

// SendResponse sends a JSON-RPC response.
func (t *Transport) SendResponse(id interface{}, result interface{}) error {
	return t.WriteMessage(&Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

// SendError sends a JSON-RPC error response.
func (t *Transport) SendError(id interface{}, code int, message string, data interface{}) error {
	return t.WriteMessage(&Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
			Data:    data,
		},
	})
}

// SendNotification sends a JSON-RPC notification.
func (t *Transport) SendNotification(method string, params interface{}) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	return t.WriteMessage(&Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	})
}

// ParseRequest attempts to parse a raw message as a request.
func ParseRequest(raw json.RawMessage) (*Request, error) {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.JSONRPC != "2.0" {
		return nil, fmt.Errorf("invalid jsonrpc version: %s", req.JSONRPC)
	}
	return &req, nil
}

// ParseParams unmarshals request params into the target.
func ParseParams[T any](req *Request) (*T, error) {
	if req.Params == nil {
		return nil, fmt.Errorf("missing params")
	}
	var params T
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	return &params, nil
}

// IsNotification checks if a request is a notification (no ID).
func IsNotification(req *Request) bool {
	return req.ID == nil
}
