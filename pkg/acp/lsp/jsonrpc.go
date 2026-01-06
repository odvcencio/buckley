package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// JSONRPCMessage represents a JSON-RPC 2.0 message
type JSONRPCMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *JSONRPCError    `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// JSON-RPC error codes
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// ReadMessage reads a JSON-RPC message from the reader
func ReadMessage(reader *bufio.Reader) (*JSONRPCMessage, error) {
	// Read headers
	headers := make(map[string]string)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			break // End of headers
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid header: %s", line)
		}

		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	// Get content length
	contentLengthStr, ok := headers["Content-Length"]
	if !ok {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	contentLength, err := strconv.Atoi(contentLengthStr)
	if err != nil {
		return nil, fmt.Errorf("invalid Content-Length: %w", err)
	}

	// Read body
	body := make([]byte, contentLength)
	_, err = io.ReadFull(reader, body)
	if err != nil {
		return nil, fmt.Errorf("failed to read message body: %w", err)
	}

	// Parse JSON
	var msg JSONRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &msg, nil
}

// WriteMessage writes a JSON-RPC message to the writer
func WriteMessage(writer io.Writer, msg *JSONRPCMessage) error {
	// Ensure JSONRPC version is set
	if msg.JSONRPC == "" {
		msg.JSONRPC = "2.0"
	}

	// Marshal to JSON
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// Write headers and body
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := writer.Write([]byte(header)); err != nil {
		return err
	}

	if _, err := writer.Write(body); err != nil {
		return err
	}

	return nil
}

// HandleMessage processes a JSON-RPC message and returns a response
func (b *Bridge) HandleMessage(ctx context.Context, msg *JSONRPCMessage) *JSONRPCMessage {
	// Check for notification (no ID)
	if msg.ID == nil {
		// Notifications don't get responses
		b.handleNotification(ctx, msg)
		return nil
	}

	// Handle request
	return b.handleRequest(ctx, msg)
}

// handleRequest processes a JSON-RPC request
func (b *Bridge) handleRequest(ctx context.Context, msg *JSONRPCMessage) *JSONRPCMessage {
	response := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
	}

	switch msg.Method {
	case "initialize":
		var params InitializeParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			response.Error = &JSONRPCError{
				Code:    InvalidParams,
				Message: "invalid initialize params",
			}
			return response
		}

		result, err := b.Initialize(ctx, params)
		if err != nil {
			response.Error = &JSONRPCError{
				Code:    InternalError,
				Message: err.Error(),
			}
			return response
		}

		resultJSON, _ := json.Marshal(result)
		response.Result = resultJSON

	case "shutdown":
		if err := b.Shutdown(ctx); err != nil {
			response.Error = &JSONRPCError{
				Code:    InternalError,
				Message: err.Error(),
			}
			return response
		}

		response.Result = json.RawMessage("null")

	case "buckley/textQuery":
		var params TextQueryParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			response.Error = &JSONRPCError{
				Code:    InvalidParams,
				Message: "invalid textQuery params",
			}
			return response
		}

		responseText, err := b.HandleTextQuery(ctx, params.Query)
		if err != nil {
			response.Error = &JSONRPCError{
				Code:    InternalError,
				Message: err.Error(),
			}
			return response
		}

		result := TextQueryResult{
			Response: responseText,
			AgentID:  b.config.AgentID,
		}
		resultJSON, _ := json.Marshal(result)
		response.Result = resultJSON

	case "buckley/streamQuery":
		var params StreamQueryParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			response.Error = &JSONRPCError{
				Code:    InvalidParams,
				Message: "invalid streamQuery params",
			}
			return response
		}

		// This will be handled asynchronously, so we need access to writer
		// For now, return error indicating this needs special handling
		response.Error = &JSONRPCError{
			Code:    InternalError,
			Message: "streamQuery requires async handling via ServeStdio",
		}

	default:
		response.Error = &JSONRPCError{
			Code:    MethodNotFound,
			Message: fmt.Sprintf("method not found: %s", msg.Method),
		}
	}

	return response
}

// handleNotification processes a JSON-RPC notification
func (b *Bridge) handleNotification(ctx context.Context, msg *JSONRPCMessage) {
	switch msg.Method {
	case "initialized":
		b.Initialized(ctx)
	case "exit":
		b.Exit(ctx)
	case "$/cancelRequest":
		var params CancelParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return
		}
		// Extract stream ID from the cancelled request
		// This is a simplified implementation
		// In production, you'd track request ID -> stream ID mapping
		// Silently ignore unknown notifications per JSON-RPC spec
	}
}
