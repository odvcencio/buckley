package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
)

// ManagerFromConfig builds and connects an MCP manager from config.
func ManagerFromConfig(ctx context.Context, cfg config.MCPConfig) (*Manager, error) {
	if !cfg.Enabled || len(cfg.Servers) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	manager := NewManager()
	var errs []string

	for _, srv := range cfg.Servers {
		if srv.Disabled {
			continue
		}
		name := strings.TrimSpace(srv.Name)
		command := strings.TrimSpace(srv.Command)
		if name == "" {
			errs = append(errs, "server name is required")
			continue
		}
		if command == "" {
			errs = append(errs, fmt.Sprintf("%s: command is required", name))
			continue
		}
		manager.AddServer(Config{
			Name:    name,
			Command: command,
			Args:    srv.Args,
			Env:     srv.Env,
			Timeout: srv.Timeout,
		})
	}

	if len(manager.configs) == 0 {
		if len(errs) > 0 {
			return nil, fmt.Errorf("mcp config: %s", strings.Join(errs, "; "))
		}
		return nil, nil
	}

	err := manager.Connect(ctx)
	if len(errs) > 0 {
		if err != nil {
			errs = append(errs, err.Error())
		}
		return manager, fmt.Errorf("mcp setup: %s", strings.Join(errs, "; "))
	}

	return manager, err
}
