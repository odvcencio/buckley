package mcp

import (
	"testing"
	"time"
)

func TestNewToolAdapter(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]any{
			"type": "object",
		},
	}

	adapter := NewToolAdapter(m, "server1", tool, 30*time.Second)

	if adapter == nil {
		t.Fatal("NewToolAdapter returned nil")
	}

	if adapter.manager != m {
		t.Error("manager not set correctly")
	}

	if adapter.serverName != "server1" {
		t.Errorf("serverName = %v, want server1", adapter.serverName)
	}

	if adapter.tool.Name != "test_tool" {
		t.Errorf("tool.Name = %v, want test_tool", adapter.tool.Name)
	}

	if adapter.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", adapter.timeout)
	}
}

func TestNewToolAdapter_DefaultTimeout(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{Name: "test"}

	adapter := NewToolAdapter(m, "server", tool, 0)

	if adapter.timeout != 60*time.Second {
		t.Errorf("expected default timeout of 60s, got %v", adapter.timeout)
	}
}

func TestToolAdapter_Name(t *testing.T) {
	tests := []struct {
		serverName string
		toolName   string
		expected   string
	}{
		{"server1", "read_file", "mcp__server1__read_file"},
		{"my-server", "list_files", "mcp__my-server__list_files"},
		{"fs", "write", "mcp__fs__write"},
		{"server_with_underscores", "tool_name", "mcp__server_with_underscores__tool_name"},
	}

	m := NewManager()

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			tool := ToolDefinition{Name: tt.toolName}
			adapter := NewToolAdapter(m, tt.serverName, tool, 0)

			result := adapter.Name()
			if result != tt.expected {
				t.Errorf("Name() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestToolAdapter_Description(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name:        "test",
		Description: "This is a test tool that does something useful",
	}

	adapter := NewToolAdapter(m, "server", tool, 0)

	desc := adapter.Description()
	if desc != "This is a test tool that does something useful" {
		t.Errorf("Description() = %v", desc)
	}
}

func TestToolAdapter_Description_Empty(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{Name: "test"}

	adapter := NewToolAdapter(m, "server", tool, 0)

	desc := adapter.Description()
	if desc != "" {
		t.Errorf("expected empty description, got %v", desc)
	}
}

func TestToolAdapter_Parameters(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name: "search",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max results",
					"default":     10,
				},
			},
			"required": []any{"query"},
		},
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	if params.Type != "object" {
		t.Errorf("Type = %v, want object", params.Type)
	}

	if len(params.Properties) != 2 {
		t.Errorf("expected 2 properties, got %d", len(params.Properties))
	}

	// Check query property
	queryProp, ok := params.Properties["query"]
	if !ok {
		t.Error("query property not found")
	} else {
		if queryProp.Type != "string" {
			t.Errorf("query.Type = %v, want string", queryProp.Type)
		}
		if queryProp.Description != "Search query" {
			t.Errorf("query.Description = %v", queryProp.Description)
		}
	}

	// Check limit property
	limitProp, ok := params.Properties["limit"]
	if !ok {
		t.Error("limit property not found")
	} else {
		if limitProp.Type != "integer" {
			t.Errorf("limit.Type = %v, want integer", limitProp.Type)
		}
		if limitProp.Default != 10 {
			t.Errorf("limit.Default = %v, want 10", limitProp.Default)
		}
	}

	// Check required
	if len(params.Required) != 1 || params.Required[0] != "query" {
		t.Errorf("Required = %v, want [query]", params.Required)
	}
}

func TestToolAdapter_Parameters_EmptySchema(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name:        "no_params",
		InputSchema: map[string]any{},
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	if params.Type != "object" {
		t.Errorf("Type = %v, want object", params.Type)
	}

	if len(params.Properties) != 0 {
		t.Errorf("expected 0 properties, got %d", len(params.Properties))
	}

	if len(params.Required) != 0 {
		t.Errorf("expected 0 required, got %d", len(params.Required))
	}
}

func TestToolAdapter_Parameters_NilSchema(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name:        "no_params",
		InputSchema: nil,
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	if params.Type != "object" {
		t.Errorf("Type = %v, want object", params.Type)
	}
}

func TestToolAdapter_Parameters_InvalidPropertiesType(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name: "bad_schema",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": "not a map", // Invalid type
		},
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	// Should handle gracefully
	if len(params.Properties) != 0 {
		t.Errorf("expected 0 properties for invalid schema, got %d", len(params.Properties))
	}
}

func TestToolAdapter_Parameters_InvalidPropertyType(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name: "bad_property",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"good": map[string]any{
					"type": "string",
				},
				"bad": "not a map", // Invalid property
			},
		},
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	// Should only have the valid property
	if len(params.Properties) != 1 {
		t.Errorf("expected 1 property, got %d", len(params.Properties))
	}

	if _, ok := params.Properties["good"]; !ok {
		t.Error("expected 'good' property to exist")
	}
}

func TestToolAdapter_Parameters_InvalidRequiredType(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name: "bad_required",
		InputSchema: map[string]any{
			"type":     "object",
			"required": "not an array", // Invalid type
		},
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	if len(params.Required) != 0 {
		t.Errorf("expected 0 required for invalid type, got %d", len(params.Required))
	}
}

func TestToolAdapter_Parameters_MixedRequiredTypes(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name: "mixed_required",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"valid", 123, "also_valid"}, // Mixed types
		},
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	// Should only include string values
	if len(params.Required) != 2 {
		t.Errorf("expected 2 required, got %d: %v", len(params.Required), params.Required)
	}
}

func TestToolAdapter_Execute_ServerNotConnected(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name: "test_tool",
	}

	adapter := NewToolAdapter(m, "nonexistent", tool, time.Second)

	result, err := adapter.Execute(nil)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected Success=false")
	}

	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestGetString(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]any
		key      string
		expected string
	}{
		{
			name:     "string value",
			m:        map[string]any{"key": "value"},
			key:      "key",
			expected: "value",
		},
		{
			name:     "missing key",
			m:        map[string]any{"other": "value"},
			key:      "key",
			expected: "",
		},
		{
			name:     "non-string value",
			m:        map[string]any{"key": 123},
			key:      "key",
			expected: "",
		},
		{
			name:     "nil map",
			m:        nil,
			key:      "key",
			expected: "",
		},
		{
			name:     "empty string",
			m:        map[string]any{"key": ""},
			key:      "key",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getString(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("getString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParseMCPConfig_Variants(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		expectCount int
	}{
		{
			name: "valid config",
			input: `{
				"mcpServers": {
					"filesystem": {
						"command": "npx",
						"args": ["-y", "@modelcontextprotocol/server-filesystem"],
						"env": {"DEBUG": "true"}
					}
				}
			}`,
			expectError: false,
			expectCount: 1,
		},
		{
			name: "multiple servers",
			input: `{
				"mcpServers": {
					"server1": {"command": "cmd1"},
					"server2": {"command": "cmd2"},
					"server3": {"command": "cmd3"}
				}
			}`,
			expectError: false,
			expectCount: 3,
		},
		{
			name: "empty servers",
			input: `{
				"mcpServers": {}
			}`,
			expectError: false,
			expectCount: 0,
		},
		{
			name:        "invalid JSON",
			input:       `{not valid json}`,
			expectError: true,
		},
		{
			name:        "missing mcpServers",
			input:       `{"other": "data"}`,
			expectError: false,
			expectCount: 0,
		},
		{
			name:        "empty input",
			input:       `{}`,
			expectError: false,
			expectCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configs, err := ParseMCPConfig([]byte(tt.input))

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(configs) != tt.expectCount {
				t.Errorf("expected %d configs, got %d", tt.expectCount, len(configs))
			}
		})
	}
}

func TestParseMCPConfig_Details(t *testing.T) {
	input := `{
		"mcpServers": {
			"test-server": {
				"command": "/usr/bin/mcp-server",
				"args": ["--port", "8080", "--debug"],
				"env": {
					"API_KEY": "secret123",
					"LOG_LEVEL": "debug"
				}
			}
		}
	}`

	configs, err := ParseMCPConfig([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	cfg := configs[0]

	if cfg.Name != "test-server" {
		t.Errorf("Name = %v, want test-server", cfg.Name)
	}

	if cfg.Command != "/usr/bin/mcp-server" {
		t.Errorf("Command = %v", cfg.Command)
	}

	if len(cfg.Args) != 3 {
		t.Errorf("expected 3 args, got %d", len(cfg.Args))
	}

	if cfg.Args[0] != "--port" || cfg.Args[1] != "8080" || cfg.Args[2] != "--debug" {
		t.Errorf("Args = %v", cfg.Args)
	}

	if cfg.Env["API_KEY"] != "secret123" {
		t.Error("API_KEY env not set correctly")
	}

	if cfg.Env["LOG_LEVEL"] != "debug" {
		t.Error("LOG_LEVEL env not set correctly")
	}

	// Default timeout should be set
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout)
	}
}

func TestListMCPToolInfo_Empty(t *testing.T) {
	m := NewManager()

	infos := ListMCPToolInfo(m)
	// ListMCPToolInfo returns nil when no tools are registered (acceptable behavior)
	if len(infos) != 0 {
		t.Errorf("expected 0 infos, got %d", len(infos))
	}
}

func TestListMCPToolInfo_WithTools(t *testing.T) {
	m := NewManager()

	// Inject a mock client with tools
	client := &Client{
		tools: []ToolDefinition{
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
		pending: make(map[int64]chan *Message),
	}

	m.mu.Lock()
	m.clients["filesystem"] = client
	m.mu.Unlock()

	infos := ListMCPToolInfo(m)

	if len(infos) != 2 {
		t.Fatalf("expected 2 infos, got %d", len(infos))
	}

	// Find read_file info
	var readFileInfo *MCPToolInfo
	for i := range infos {
		if infos[i].Name == "read_file" {
			readFileInfo = &infos[i]
			break
		}
	}

	if readFileInfo == nil {
		t.Fatal("read_file info not found")
	}

	if readFileInfo.FullName != "mcp__filesystem__read_file" {
		t.Errorf("FullName = %v", readFileInfo.FullName)
	}

	if readFileInfo.Server != "filesystem" {
		t.Errorf("Server = %v", readFileInfo.Server)
	}

	if readFileInfo.Description != "Read a file" {
		t.Errorf("Description = %v", readFileInfo.Description)
	}
}

func TestRegisterMCPTools(t *testing.T) {
	m := NewManager()

	// Inject a mock client with tools
	client := &Client{
		tools: []ToolDefinition{
			{Name: "tool1", Description: "Tool 1"},
			{Name: "tool2", Description: "Tool 2"},
		},
		pending: make(map[int64]chan *Message),
	}

	m.mu.Lock()
	m.clients["server"] = client
	m.mu.Unlock()

	registered := make(map[string]any)
	register := func(name string, tool any) {
		registered[name] = tool
	}

	RegisterMCPTools(m, register)

	if len(registered) != 2 {
		t.Errorf("expected 2 registered tools, got %d", len(registered))
	}

	if _, ok := registered["mcp__server__tool1"]; !ok {
		t.Error("tool1 not registered")
	}

	if _, ok := registered["mcp__server__tool2"]; !ok {
		t.Error("tool2 not registered")
	}
}

func TestRegisterMCPTools_MultipleServers(t *testing.T) {
	m := NewManager()

	// Server 1
	m.mu.Lock()
	m.clients["server1"] = &Client{
		tools: []ToolDefinition{
			{Name: "tool_a"},
		},
		pending: make(map[int64]chan *Message),
	}

	// Server 2
	m.clients["server2"] = &Client{
		tools: []ToolDefinition{
			{Name: "tool_a"}, // Same name as server1
			{Name: "tool_b"},
		},
		pending: make(map[int64]chan *Message),
	}
	m.mu.Unlock()

	registered := make(map[string]any)
	register := func(name string, tool any) {
		registered[name] = tool
	}

	RegisterMCPTools(m, register)

	// Should have 3 tools with unique names due to server prefix
	if len(registered) != 3 {
		t.Errorf("expected 3 registered tools, got %d", len(registered))
	}

	if _, ok := registered["mcp__server1__tool_a"]; !ok {
		t.Error("server1/tool_a not registered")
	}

	if _, ok := registered["mcp__server2__tool_a"]; !ok {
		t.Error("server2/tool_a not registered")
	}

	if _, ok := registered["mcp__server2__tool_b"]; !ok {
		t.Error("server2/tool_b not registered")
	}
}

func TestMCPToolInfo_Fields(t *testing.T) {
	info := MCPToolInfo{
		FullName:    "mcp__myserver__mytool",
		Server:      "myserver",
		Name:        "mytool",
		Description: "My tool description",
	}

	if info.FullName != "mcp__myserver__mytool" {
		t.Errorf("FullName = %v", info.FullName)
	}
	if info.Server != "myserver" {
		t.Errorf("Server = %v", info.Server)
	}
	if info.Name != "mytool" {
		t.Errorf("Name = %v", info.Name)
	}
	if info.Description != "My tool description" {
		t.Errorf("Description = %v", info.Description)
	}
}

// mockMCPManager is a test helper that provides a manager with mock clients
type mockMCPManager struct {
	*Manager
}

func newMockMCPManager() *mockMCPManager {
	return &mockMCPManager{
		Manager: NewManager(),
	}
}

func (m *mockMCPManager) addMockClient(name string, tools []ToolDefinition, resources []Resource) {
	client := &Client{
		serverID: name,
		serverInfo: &ServerInfo{
			Name:        name,
			Version:     "1.0.0",
			ProtocolVer: "2024-11-05",
		},
		tools:     tools,
		resources: resources,
		pending:   make(map[int64]chan *Message),
	}

	m.mu.Lock()
	m.configs[name] = Config{Name: name, Command: "mock"}
	m.clients[name] = client
	m.mu.Unlock()
}

func TestToolAdapter_ExecuteWithMockClient(t *testing.T) {
	// We can't easily mock a successful tool call without real subprocess,
	// but we can verify error handling paths
	m := NewManager()
	tool := ToolDefinition{
		Name: "test_tool",
	}

	adapter := NewToolAdapter(m, "missing_server", tool, 100*time.Millisecond)

	result, err := adapter.Execute(map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("Execute should not return error, got %v", err)
	}

	if result.Success {
		t.Error("expected Success=false for disconnected server")
	}

	if result.Error == "" {
		t.Error("expected error message for disconnected server")
	}
}

func TestToolAdapter_Timeout(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{Name: "slow_tool"}

	// Very short timeout
	adapter := NewToolAdapter(m, "server", tool, 1*time.Millisecond)

	if adapter.timeout != 1*time.Millisecond {
		t.Errorf("timeout = %v, want 1ms", adapter.timeout)
	}
}

func TestToolAdapter_ComplexParameters(t *testing.T) {
	m := NewManager()
	tool := ToolDefinition{
		Name: "complex_tool",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"string_param": map[string]any{
					"type":        "string",
					"description": "A string parameter",
				},
				"number_param": map[string]any{
					"type":        "number",
					"description": "A number parameter",
					"default":     42.5,
				},
				"boolean_param": map[string]any{
					"type":        "boolean",
					"description": "A boolean parameter",
				},
				"array_param": map[string]any{
					"type":        "array",
					"description": "An array parameter",
				},
				"object_param": map[string]any{
					"type":        "object",
					"description": "An object parameter",
				},
			},
			"required": []any{"string_param", "boolean_param"},
		},
	}

	adapter := NewToolAdapter(m, "server", tool, 0)
	params := adapter.Parameters()

	// Verify all properties are extracted
	if len(params.Properties) != 5 {
		t.Errorf("expected 5 properties, got %d", len(params.Properties))
	}

	// Verify types
	if params.Properties["number_param"].Type != "number" {
		t.Errorf("number_param type = %v", params.Properties["number_param"].Type)
	}

	if params.Properties["boolean_param"].Type != "boolean" {
		t.Errorf("boolean_param type = %v", params.Properties["boolean_param"].Type)
	}

	// Verify required
	if len(params.Required) != 2 {
		t.Errorf("expected 2 required, got %d", len(params.Required))
	}
}

func TestParseMCPConfig_NoArgs(t *testing.T) {
	input := `{
		"mcpServers": {
			"simple": {
				"command": "/usr/bin/simple-server"
			}
		}
	}`

	configs, err := ParseMCPConfig([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	if len(configs[0].Args) != 0 {
		t.Errorf("expected 0 args, got %d", len(configs[0].Args))
	}

	if configs[0].Env != nil && len(configs[0].Env) != 0 {
		t.Errorf("expected no env vars, got %d", len(configs[0].Env))
	}
}

func TestParseMCPConfig_EmptyEnv(t *testing.T) {
	input := `{
		"mcpServers": {
			"test": {
				"command": "cmd",
				"env": {}
			}
		}
	}`

	configs, err := ParseMCPConfig([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(configs[0].Env) != 0 {
		t.Errorf("expected empty env, got %d entries", len(configs[0].Env))
	}
}

func TestRegisterMCPTools_NoTools(t *testing.T) {
	m := NewManager()

	// Add client with no tools
	m.mu.Lock()
	m.clients["empty"] = &Client{
		tools:   []ToolDefinition{},
		pending: make(map[int64]chan *Message),
	}
	m.mu.Unlock()

	registered := make(map[string]any)
	register := func(name string, tool any) {
		registered[name] = tool
	}

	RegisterMCPTools(m, register)

	if len(registered) != 0 {
		t.Errorf("expected 0 registered tools, got %d", len(registered))
	}
}
