package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func runCatalog(count int, mode, logPath string, legacy bool) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-catalog", Version: "1"}, nil)
	tools := make([]*mcp.Tool, 0, count)
	for i := range count {
		name := fmt.Sprintf("tool_%04d", i)
		if i == 0 {
			name = "echo"
		}
		def := &mcp.Tool{Name: name, Description: "A bounded catalog fixture tool.", InputSchema: map[string]any{"type": "object"}}
		tools = append(tools, def)
		server.AddTool(def, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "catalog fixture ok"}}}, nil
		})
	}
	var mu sync.Mutex
	round := 0
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			mu.Lock()
			defer mu.Unlock()
			if logPath != "" {
				f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					panic(err)
				}
				fmt.Fprintln(f, method)
				f.Close()
			}
			if legacy && method == "server/discover" {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "legacy fixture"}
			}
			if mode == "no-ping" && method == "ping" {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "ping not implemented"}
			}
			if method != "tools/list" {
				return next(ctx, method, req)
			}
			if mode == "slow" {
				time.Sleep(100 * time.Millisecond)
			}
			params := req.GetParams().(*mcp.ListToolsParams)
			offset := 0
			if params.Cursor == "" {
				round++
			} else {
				parts := strings.Split(params.Cursor, ":")
				if len(parts) != 2 {
					return nil, fmt.Errorf("invalid fixture cursor")
				}
				var err error
				offset, err = strconv.Atoi(parts[1])
				if err != nil {
					return nil, err
				}
			}
			if mode == "empty" {
				return &mcp.ListToolsResult{NextCursor: fmt.Sprintf("%d:%d", round, offset+1)}, nil
			}
			if mode == "cycle" {
				return &mcp.ListToolsResult{NextCursor: "cycle:0"}, nil
			}
			if mode == "cursor" {
				return &mcp.ListToolsResult{NextCursor: strings.Repeat("x", 65<<10)}, nil
			}
			if mode == "duplicate" {
				return &mcp.ListToolsResult{Tools: []*mcp.Tool{tools[0], tools[0]}}, nil
			}
			end := min(offset+5, len(tools))
			if mode == "bytes" {
				end = min(offset+1, len(tools))
			}
			page := append([]*mcp.Tool(nil), tools[offset:end]...)
			if mode == "bytes" && round > 1 {
				for i, def := range page {
					copy := *def
					copy.Description = strings.Repeat("x", 1500)
					page[i] = &copy
				}
			}
			nextCursor := ""
			if end < len(tools) {
				nextCursor = fmt.Sprintf("%d:%d", round, end)
			}
			return &mcp.ListToolsResult{Tools: page, NextCursor: nextCursor}, nil
		}
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}
