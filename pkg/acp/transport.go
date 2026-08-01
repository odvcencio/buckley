package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Transport handles JSON-RPC message reading and writing over stdio.
type Transport struct {
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex

	// Outbound request/response correlation. Buckley is a JSON-RPC peer in
	// both directions: it answers requests the client sends, and it can
	// also originate requests of its own (e.g. session/request_permission)
	// that the client answers. nextReqID and pending track the latter.
	nextReqID int64
	pendingMu sync.Mutex
	pending   map[string]chan *Response
}

// NewTransport creates a new transport over the given reader and writer.
func NewTransport(reader io.Reader, writer io.Writer) *Transport {
	return &Transport{
		reader:  bufio.NewReader(reader),
		writer:  writer,
		pending: make(map[string]chan *Response),
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

// SendRequest sends a JSON-RPC request to the client and blocks until a
// correlated response arrives, ctx is cancelled, or timeout elapses (a
// timeout <= 0 means no timeout, only ctx governs the wait). It is
// concurrency-safe: multiple goroutines may call SendRequest at once, and
// inbound notifications/requests from the client continue to be handled
// independently by the caller's read loop while a request is outstanding.
func (t *Transport) SendRequest(ctx context.Context, method string, params interface{}, timeout time.Duration) (*Response, error) {
	id := atomic.AddInt64(&t.nextReqID, 1)
	key := strconv.FormatInt(id, 10)

	respCh := make(chan *Response, 1)
	t.pendingMu.Lock()
	t.pending[key] = respCh
	t.pendingMu.Unlock()
	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, key)
		t.pendingMu.Unlock()
	}()

	var paramsJSON json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		paramsJSON = data
	}

	if err := t.WriteMessage(&Request{JSONRPC: "2.0", ID: id, Method: method, Params: paramsJSON}); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeoutCh:
		return nil, fmt.Errorf("request %s (method %s) timed out after %s", key, method, timeout)
	}
}

// dispatchResponse routes an inbound response to the goroutine awaiting it
// in SendRequest. It reports false when the response's id does not match
// any outstanding request (e.g. it arrived after SendRequest already timed
// out), so the caller can decide how to handle an unmatched response.
func (t *Transport) dispatchResponse(resp *Response) bool {
	key := responseKey(resp.ID)
	if key == "" {
		return false
	}
	t.pendingMu.Lock()
	ch, ok := t.pending[key]
	t.pendingMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- resp:
	default:
		// The channel is buffered (size 1) and only ever receives once, so
		// this should not happen; skip rather than block if it somehow does.
	}
	return true
}

// responseKey normalizes a JSON-RPC response id (as decoded into `any`,
// typically float64 for a JSON number) into the same string form used to
// key the pending-request map when the request was sent.
func responseKey(id any) string {
	switch v := id.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

// IsResponseMessage reports whether a raw JSON-RPC message is a response
// (carries no "method" member) rather than a request or notification (both
// of which always carry "method"). The caller's read loop uses this to
// route inbound responses to SendRequest callers instead of treating them
// as an unrecognized method call.
func IsResponseMessage(raw json.RawMessage) bool {
	var probe struct {
		Method *string `json:"method"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Method == nil
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
