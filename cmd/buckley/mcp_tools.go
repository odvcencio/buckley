package main

import (
	"context"
	"fmt"
	"os"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/mcp"
	"github.com/odvcencio/buckley/pkg/tool"
)

func registerMCPTools(cfg *config.Config, registry *tool.Registry) {
	if cfg == nil || registry == nil {
		return
	}

	manager, err := mcp.ManagerFromConfig(context.Background(), cfg.MCP)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: MCP setup failed: %v\n", err)
	}
	if manager == nil {
		return
	}

	mcp.RegisterMCPTools(manager, func(_ string, toolAny any) {
		toolAdapter, ok := toolAny.(tool.Tool)
		if !ok {
			return
		}
		registry.Register(toolAdapter)
	})
}
