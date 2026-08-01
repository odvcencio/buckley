// Command fakeserver is a small MCP server used only by pkg/mcp's tests. It
// is built with `go build` (not `go run`) so the test harness has direct
// control over the child process for crash-simulation and clean-shutdown
// assertions. It is not part of the Buckley binary: `go build ./...` and
// `go vet ./...` skip everything under a testdata directory by convention.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	mode := flag.String("mode", "normal", "normal, crash, or malformed")
	toolCount := flag.Int("tools", 2, "number of synthetic tools to register in normal mode")
	flag.Parse()

	switch *mode {
	case "malformed":
		runMalformed()
	case "crash":
		runCrash()
	default:
		runNormal(*toolCount)
	}
}

// runMalformed hand-rolls just enough JSON-RPC to complete a valid
// initialize handshake (so the client connects successfully), then replies
// to the first subsequent request with a deliberately invalid JSON-RPC
// line and exits. This intentionally does not use the SDK's server: the
// SDK cannot be asked to emit non-compliant wire output, and that is
// exactly what this mode needs to produce to exercise the client's error
// handling for a misbehaving server.
func runMalformed() {
	reader := bufio.NewReader(os.Stdin)
	for {
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var req map[string]json.RawMessage
			if err := json.Unmarshal([]byte(trimmed), &req); err == nil {
				var method string
				if m, ok := req["method"]; ok {
					_ = json.Unmarshal(m, &method)
				}
				id, hasID := req["id"]
				switch method {
				case "server/discover":
					// This server predates SEP-2575's stateless discover
					// RPC (like most MCP servers as of 2025-06-18): reply
					// with a standard JSON-RPC "method not found" error so
					// the client falls back to the legacy initialize
					// handshake, exactly as it would against a real
					// pre-2026-07-28 server.
					if hasID {
						writeLine(map[string]any{
							"jsonrpc": "2.0",
							"id":      id,
							"error":   map[string]any{"code": -32601, "message": "method not found"},
						})
					}
				case "initialize":
					writeLine(map[string]any{
						"jsonrpc": "2.0",
						"id":      id,
						"result": map[string]any{
							"protocolVersion": "2025-06-18",
							"capabilities":    map[string]any{"tools": map[string]any{}},
							"serverInfo":      map[string]any{"name": "fake-malformed", "version": "0.0.1"},
						},
					})
				case "notifications/initialized":
					// No response required for a notification.
				case "tools/list":
					fmt.Fprintln(os.Stdout, "{this is not valid json-rpc")
					return
				default:
					if hasID {
						writeLine(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
					}
				}
			}
		}
		if readErr != nil {
			return
		}
	}
}

func writeLine(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}

// runCrash serves one normal tool call ("crash"), then exits nonzero to
// simulate the server process dying mid-session.
func runCrash() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-crash", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "crash",
		Description: "Exits the process instead of responding.",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		os.Stdout.Close()
		os.Exit(1)
		return nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

// runNormal serves a handful of well-formed tools for handshake/list/call
// coverage, including tools whose results exercise text, image, and
// resource content blocks.
func runNormal(toolCount int) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-normal", Version: "0.0.1"}, nil)

	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Echoes the given text back as a text content block.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "Text to echo"},
			},
			"required": []string{"text"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Text string `json:"text"`
		}
		_ = unmarshalArgs(req, &args)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "fail",
		Description: "Always returns a tool-level error.",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "synthetic tool failure"}},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "mixed_content",
		Description: "Returns text, image, and embedded-resource content blocks.",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "first line"},
			&mcp.ImageContent{Data: []byte{0x1, 0x2, 0x3}, MIMEType: "image/png"},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///tmp/example.txt", MIMEType: "text/plain"}},
		}}, nil
	})

	// Pad out with synthetic tools to exercise the max_tools budget and
	// description-truncation logic in the bridging layer.
	for i := 0; i < toolCount; i++ {
		name := fmt.Sprintf("synthetic_%d", i)
		server.AddTool(&mcp.Tool{
			Name:        name,
			Description: fmt.Sprintf("Synthetic test tool number %d with a long description padded well past the two hundred byte schema budget so truncation can be exercised end to end without relying on a hand-written fixture that could drift from the real budget constant. %s", i, longFiller),
			InputSchema: map[string]any{"type": "object"},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	}

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

const longFiller = "Padding padding padding padding padding padding padding padding padding padding."

func unmarshalArgs(req *mcp.CallToolRequest, out any) error {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return nil
	}
	return json.Unmarshal(req.Params.Arguments, out)
}
