package config

// mergeMCPConfig merges mcp.enabled, mcp.max_tools, and mcp.servers.
// Servers are merged per-name (like mergePosturesConfig's Layers map) so a
// project config can add or override a single server without needing to
// restate every server defined at the user scope.
func mergeMCPConfig(base, override *Config, raw map[string]any) {
	if boolFieldSet(raw, "mcp", "enabled") {
		base.MCP.Enabled = override.MCP.Enabled
	}
	if boolFieldSet(raw, "mcp", "max_tools") {
		base.MCP.MaxTools = override.MCP.MaxTools
	}
	if boolFieldSet(raw, "mcp", "servers") {
		merged := make(map[string]MCPServerConfig, len(base.MCP.Servers)+len(override.MCP.Servers))
		order := make([]string, 0, len(base.MCP.Servers)+len(override.MCP.Servers))
		for _, srv := range base.MCP.Servers {
			if _, exists := merged[srv.Name]; !exists {
				order = append(order, srv.Name)
			}
			merged[srv.Name] = srv
		}
		for _, srv := range override.MCP.Servers {
			if _, exists := merged[srv.Name]; !exists {
				order = append(order, srv.Name)
			}
			merged[srv.Name] = srv
		}
		servers := make([]MCPServerConfig, 0, len(order))
		for _, name := range order {
			servers = append(servers, merged[name])
		}
		base.MCP.Servers = servers
	}
}
