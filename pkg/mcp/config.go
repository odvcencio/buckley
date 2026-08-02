package mcp

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/config"
)

// ManagerFromConfig validates cfg, builds a Manager from its enabled
// servers, and connects to all of them. It returns (nil, nil) when MCP is
// disabled or no servers are configured.
func ManagerFromConfig(ctx context.Context, cfg config.MCPConfig) (*Manager, error) {
	if !cfg.Enabled || len(cfg.Servers) == 0 {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("mcp config: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	manager := NewManager()
	var configured int
	for _, srv := range cfg.Servers {
		if !srv.Enabled {
			continue
		}
		manager.AddServer(Config{
			Name:    strings.TrimSpace(srv.Name),
			Command: strings.TrimSpace(srv.Command),
			Args:    srv.Args,
			Env:     config.ExpandMCPEnv(srv.Env),
			Timeout: srv.Timeout,
		})
		configured++
	}
	if configured == 0 {
		return nil, nil
	}

	if err := manager.Connect(ctx); err != nil {
		return manager, err
	}
	return manager, nil
}
