// Package mcp implements the Model Context Protocol client for connecting to external tool servers.
// MCP allows Buckley to dynamically discover and use tools from external servers.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Message represents an MCP JSON-RPC message
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ErrorResponse  `json:"error,omitempty"`
}

// ErrorResponse represents a JSON-RPC error
type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ServerInfo contains information about an MCP server
type ServerInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	ProtocolVer  string `json:"protocolVersion"`
	Instructions string `json:"instructions,omitempty"`
}

// ToolDefinition describes a tool provided by an MCP server
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolsListResult is the result of tools/list
type ToolsListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// ToolCallParams are the parameters for tools/call
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolCallResult is the result of tools/call
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock represents content in a tool result
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`     // For base64 data
	MimeType string `json:"mimeType,omitempty"` // For binary data
}

// Resource represents a resource from an MCP server
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourcesListResult is the result of resources/list
type ResourcesListResult struct {
	Resources []Resource `json:"resources"`
}

// Client is an MCP client that communicates with an MCP server
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu       sync.Mutex
	pending  map[int64]chan *Message
	msgID    int64
	closed   bool
	serverID string

	// Server capabilities
	serverInfo *ServerInfo
	tools      []ToolDefinition
	resources  []Resource
}

// Config contains configuration for an MCP server connection
type Config struct {
	Name    string            `yaml:"name" json:"name"`
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args" json:"args"`
	Env     map[string]string `yaml:"env" json:"env"`
	Timeout time.Duration     `yaml:"timeout" json:"timeout"`
}

// NewClient creates a new MCP client that connects to a server
func NewClient(cfg Config) (*Client, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)

	// Set up environment
	if len(cfg.Env) > 0 {
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start server: %w", err)
	}

	client := &Client{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		pending:  make(map[int64]chan *Message),
		serverID: cfg.Name,
	}

	// Start reading responses
	go client.readResponses()

	return client, nil
}

func (c *Client) readResponses() {
	scanner := bufio.NewScanner(c.stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		if msg.ID != nil {
			c.mu.Lock()
			if ch, ok := c.pending[*msg.ID]; ok {
				ch <- &msg
				delete(c.pending, *msg.ID)
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) nextID() int64 {
	return atomic.AddInt64(&c.msgID, 1)
}

func (c *Client) call(ctx context.Context, method string, params any) (*Message, error) {
	id := c.nextID()

	var paramsBytes json.RawMessage
	if params != nil {
		var err error
		paramsBytes, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
	}

	msg := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  paramsBytes,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	respCh := make(chan *Message, 1)
	c.mu.Lock()
	c.pending[id] = respCh
	c.mu.Unlock()

	// Write message with newline
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to write message: %w", err)
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Initialize performs the MCP initialization handshake
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"clientInfo": map[string]any{
			"name":    "buckley",
			"version": "1.0.0",
		},
	}

	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	// Parse server info
	var result struct {
		ServerInfo   ServerInfo `json:"serverInfo"`
		ProtocolVer  string     `json:"protocolVersion"`
		Instructions string     `json:"instructions,omitempty"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("failed to parse initialize result: %w", err)
	}

	c.serverInfo = &result.ServerInfo
	c.serverInfo.ProtocolVer = result.ProtocolVer
	c.serverInfo.Instructions = result.Instructions

	// Send initialized notification
	notif := Message{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	data, _ := json.Marshal(notif)
	c.stdin.Write(append(data, '\n'))

	return nil
}

// ListTools fetches the list of available tools from the server
func (c *Client) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	resp, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list failed: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tools list: %w", err)
	}

	c.tools = result.Tools
	return result.Tools, nil
}

// CallTool invokes a tool on the MCP server
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolCallResult, error) {
	params := ToolCallParams{
		Name:      name,
		Arguments: arguments,
	}

	resp, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("tools/call failed: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tools/call error: %s", resp.Error.Message)
	}

	var result ToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tool result: %w", err)
	}

	return &result, nil
}

// ListResources fetches the list of available resources from the server
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	resp, err := c.call(ctx, "resources/list", nil)
	if err != nil {
		return nil, fmt.Errorf("resources/list failed: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("resources/list error: %s", resp.Error.Message)
	}

	var result ResourcesListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resources list: %w", err)
	}

	c.resources = result.Resources
	return result.Resources, nil
}

// ReadResource reads a resource from the MCP server
func (c *Client) ReadResource(ctx context.Context, uri string) ([]ContentBlock, error) {
	params := map[string]any{
		"uri": uri,
	}

	resp, err := c.call(ctx, "resources/read", params)
	if err != nil {
		return nil, fmt.Errorf("resources/read failed: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("resources/read error: %s", resp.Error.Message)
	}

	var result struct {
		Contents []ContentBlock `json:"contents"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resource: %w", err)
	}

	return result.Contents, nil
}

// ServerInfo returns information about the connected server
func (c *Client) ServerInfo() *ServerInfo {
	return c.serverInfo
}

// Tools returns the cached list of tools
func (c *Client) Tools() []ToolDefinition {
	return c.tools
}

// Resources returns the cached list of resources
func (c *Client) Resources() []Resource {
	return c.resources
}

// ServerID returns the server's identifier
func (c *Client) ServerID() string {
	return c.serverID
}

// Close terminates the MCP server connection
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	// Close pending requests
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = nil

	c.stdin.Close()
	c.stdout.Close()
	c.stderr.Close()

	// Give the process time to exit gracefully
	done := make(chan error, 1)
	go func() {
		done <- c.cmd.Wait()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.cmd.Process.Kill()
	}

	return nil
}
