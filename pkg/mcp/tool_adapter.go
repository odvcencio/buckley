package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ToolAdapter wraps MCP tools as Buckley tools
type ToolAdapter struct {
	manager    *Manager
	serverName string
	tool       ToolDefinition
	timeout    time.Duration
}

// NewToolAdapter creates a new tool adapter
func NewToolAdapter(manager *Manager, serverName string, tool ToolDefinition, timeout time.Duration) *ToolAdapter {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &ToolAdapter{
		manager:    manager,
		serverName: serverName,
		tool:       tool,
		timeout:    timeout,
	}
}

// Name returns the tool name (prefixed with server name)
func (t *ToolAdapter) Name() string {
	return fmt.Sprintf("mcp__%s__%s", t.serverName, t.tool.Name)
}

// Description returns the tool description
func (t *ToolAdapter) Description() string {
	return t.tool.Description
}

// Parameters returns the tool parameters schema
func (t *ToolAdapter) Parameters() builtin.ParameterSchema {
	schema := builtin.ParameterSchema{
		Type:       "object",
		Properties: make(map[string]builtin.PropertySchema),
		Required:   []string{},
	}

	// Convert from MCP schema to Buckley schema
	if props, ok := t.tool.InputSchema["properties"].(map[string]any); ok {
		for name, propRaw := range props {
			prop, ok := propRaw.(map[string]any)
			if !ok {
				continue
			}

			propSchema := builtin.PropertySchema{
				Type:        getString(prop, "type"),
				Description: getString(prop, "description"),
			}

			if def, ok := prop["default"]; ok {
				propSchema.Default = def
			}

			schema.Properties[name] = propSchema
		}
	}

	// Get required fields
	if req, ok := t.tool.InputSchema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				schema.Required = append(schema.Required, s)
			}
		}
	}

	return schema
}

// Execute calls the MCP tool
func (t *ToolAdapter) Execute(params map[string]any) (*builtin.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()

	result, err := t.manager.CallTool(ctx, t.serverName, t.tool.Name, params)
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   fmt.Sprintf("MCP tool call failed: %v", err),
		}, nil
	}

	if result.IsError {
		var errMsg string
		for _, block := range result.Content {
			if block.Type == "text" {
				errMsg += block.Text
			}
		}
		return &builtin.Result{
			Success: false,
			Error:   errMsg,
		}, nil
	}

	// Convert content blocks to result
	var textContent strings.Builder
	data := make(map[string]any)

	for i, block := range result.Content {
		switch block.Type {
		case "text":
			textContent.WriteString(block.Text)
			if i < len(result.Content)-1 {
				textContent.WriteString("\n")
			}
		case "image":
			data[fmt.Sprintf("image_%d", i)] = map[string]any{
				"data":     block.Data,
				"mimeType": block.MimeType,
			}
		case "resource":
			data[fmt.Sprintf("resource_%d", i)] = map[string]any{
				"data":     block.Data,
				"mimeType": block.MimeType,
			}
		}
	}

	data["content"] = textContent.String()
	data["server"] = t.serverName
	data["tool"] = t.tool.Name

	return &builtin.Result{
		Success: true,
		Data:    data,
	}, nil
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// RegisterMCPTools registers all MCP tools with a tool registry
func RegisterMCPTools(manager *Manager, register func(name string, tool any)) {
	if manager == nil {
		return
	}
	for _, twt := range manager.AllTools() {
		timeout := 60 * time.Second
		if cfg, ok := manager.configs[twt.Server]; ok && cfg.Timeout > 0 {
			timeout = cfg.Timeout
		}
		adapter := NewToolAdapter(manager, twt.Server, twt.Tool, timeout)
		register(adapter.Name(), adapter)
	}
}

// MCPToolInfo provides info about MCP tools for display
type MCPToolInfo struct {
	FullName    string
	Server      string
	Name        string
	Description string
}

// ListMCPToolInfo returns information about all registered MCP tools
func ListMCPToolInfo(manager *Manager) []MCPToolInfo {
	var infos []MCPToolInfo
	for _, twt := range manager.AllTools() {
		infos = append(infos, MCPToolInfo{
			FullName:    fmt.Sprintf("mcp__%s__%s", twt.Server, twt.Tool.Name),
			Server:      twt.Server,
			Name:        twt.Tool.Name,
			Description: twt.Tool.Description,
		})
	}
	return infos
}

// ParseMCPConfig parses MCP server configurations from JSON
func ParseMCPConfig(data []byte) ([]Config, error) {
	var raw struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse MCP config: %w", err)
	}

	var configs []Config
	for name, srv := range raw.MCPServers {
		configs = append(configs, Config{
			Name:    name,
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
			Timeout: 30 * time.Second,
		})
	}

	return configs, nil
}
