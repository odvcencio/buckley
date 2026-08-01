// Package mcp implements a Model Context Protocol (MCP) client for
// connecting Buckley to external stdio tool servers. It wraps the official
// github.com/modelcontextprotocol/go-sdk client rather than hand-rolling the
// JSON-RPC 2.0 wire format: the SDK negotiates protocol revisions from
// 2024-11-05 through 2026-07-28 and falls back gracefully when a server only
// understands an older revision (most servers today speak 2025-06-18).
package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// buckleyClientVersion is reported to MCP servers during the initialize
// handshake. It is deliberately independent of Buckley's release version so
// wire compatibility never depends on a build tag.
const buckleyClientVersion = "1.0.0"

// DefaultTimeout is used for a server connection when Config.Timeout is
// unset.
const DefaultTimeout = 30 * time.Second

// Config describes how to launch and connect to a single MCP stdio server.
type Config struct {
	// Name identifies the server within a Manager; also used as the prefix
	// for bridged tool names.
	Name string
	// Command is the executable to run. It must be an absolute path or
	// resolvable via PATH (see ValidateCommand).
	Command string
	// Args are passed to Command.
	Args []string
	// Env supplies additional environment variables for the server
	// process, merged over the ambient environment.
	Env map[string]string
	// Timeout bounds the initialize handshake and health checks.
	Timeout time.Duration
}

// Client wraps a single MCP server connection over stdio.
type Client struct {
	cfg Config
	sdk *sdkmcp.Client

	mu      sync.RWMutex
	cmd     *exec.Cmd
	session *sdkmcp.ClientSession
	tools   []*sdkmcp.Tool
}

// NewClient starts the configured server process and performs the MCP
// initialize handshake (the SDK's Client.Connect call). A successful return
// means the server responded to initialize and is ready for tools/list and
// tools/call.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("mcp: command is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), envSlice(cfg.Env)...)
	}

	sdk := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "buckley",
		Version: buckleyClientVersion,
	}, nil)

	connectCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	session, err := sdk.Connect(connectCtx, &sdkmcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect to server %q: %w", cfg.Name, err)
	}

	return &Client{cfg: cfg, sdk: sdk, cmd: cmd, session: session}, nil
}

// envSlice renders a map of environment variables as KEY=VALUE entries.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

// ListTools fetches the current tool list from the server and caches it.
func (c *Client) ListTools(ctx context.Context) ([]*sdkmcp.Tool, error) {
	if c == nil {
		return nil, fmt.Errorf("mcp: nil client")
	}
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return nil, fmt.Errorf("mcp: not connected")
	}

	var all []*sdkmcp.Tool
	cursor := ""
	for {
		res, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp: tools/list failed: %w", err)
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	c.mu.Lock()
	c.tools = all
	c.mu.Unlock()
	return all, nil
}

// CallTool invokes a tool on the connected server.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*sdkmcp.CallToolResult, error) {
	if c == nil {
		return nil, fmt.Errorf("mcp: nil client")
	}
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return nil, fmt.Errorf("mcp: not connected")
	}

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: tools/call %q failed: %w", name, err)
	}
	return res, nil
}

// Tools returns the most recently cached tool list (populated by
// ListTools).
func (c *Client) Tools() []*sdkmcp.Tool {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tools
}

// ServerInfo returns the implementation info the server reported during
// initialize, or nil if the client is not connected.
func (c *Client) ServerInfo() *sdkmcp.Implementation {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil || session.InitializeResult() == nil {
		return nil
	}
	return session.InitializeResult().ServerInfo
}

// ProtocolVersion returns the protocol revision negotiated with the server
// during initialize, or "" if not connected.
func (c *Client) ProtocolVersion() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil || session.InitializeResult() == nil {
		return ""
	}
	return session.InitializeResult().ProtocolVersion
}

// ServerID returns the configured server name.
func (c *Client) ServerID() string {
	if c == nil {
		return ""
	}
	return c.cfg.Name
}

// Wait blocks until the underlying connection closes, returning any error
// that caused the close. It is used by Manager to detect a crashed server
// process and trigger a restart.
func (c *Client) Wait() error {
	if c == nil {
		return fmt.Errorf("mcp: nil client")
	}
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("mcp: not connected")
	}
	return session.Wait()
}

// Close terminates the MCP session and, if the server process has not
// exited, sends SIGTERM after a grace period (handled by the SDK's
// CommandTransport).
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	session := c.session
	c.session = nil
	c.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}
