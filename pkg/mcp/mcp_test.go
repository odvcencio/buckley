package mcp

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMessage_Marshal(t *testing.T) {
	id := int64(1)
	msg := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/list",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %v, want 2.0", decoded.JSONRPC)
	}
	if *decoded.ID != 1 {
		t.Errorf("ID = %v, want 1", *decoded.ID)
	}
	if decoded.Method != "tools/list" {
		t.Errorf("Method = %v, want tools/list", decoded.Method)
	}
}

func TestToolDefinition(t *testing.T) {
	tool := ToolDefinition{
		Name:        "read_file",
		Description: "Read a file",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file",
				},
			},
			"required": []any{"path"},
		},
	}

	if tool.Name != "read_file" {
		t.Errorf("Name = %v, want read_file", tool.Name)
	}

	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema properties not found")
	}

	pathProp, ok := props["path"].(map[string]any)
	if !ok {
		t.Fatal("path property not found")
	}

	if pathProp["type"] != "string" {
		t.Errorf("path type = %v, want string", pathProp["type"])
	}
}

func TestManager_New(t *testing.T) {
	manager := NewManager()
	if manager == nil {
		t.Fatal("NewManager() returned nil")
	}

	if manager.clients == nil {
		t.Error("clients map not initialized")
	}

	if manager.configs == nil {
		t.Error("configs map not initialized")
	}
}

func TestManager_AddServer(t *testing.T) {
	manager := NewManager()

	cfg := Config{
		Name:    "test-server",
		Command: "echo",
		Args:    []string{"hello"},
		Timeout: 10 * time.Second,
	}

	manager.AddServer(cfg)

	servers := manager.ListServers()
	if len(servers) != 1 {
		t.Errorf("ListServers() returned %d servers, want 1", len(servers))
	}
	if servers[0] != "test-server" {
		t.Errorf("Server name = %v, want test-server", servers[0])
	}
}

func TestManager_ServerStatus(t *testing.T) {
	manager := NewManager()

	cfg := Config{
		Name:    "test-server",
		Command: "/path/to/server",
	}
	manager.AddServer(cfg)

	statuses := manager.ServerStatus()
	if len(statuses) != 1 {
		t.Errorf("ServerStatus() returned %d statuses, want 1", len(statuses))
	}

	status := statuses[0]
	if status.Name != "test-server" {
		t.Errorf("Name = %v, want test-server", status.Name)
	}
	if status.Connected {
		t.Error("Connected = true, want false (not connected)")
	}
}

func TestConfig(t *testing.T) {
	cfg := Config{
		Name:    "test",
		Command: "node",
		Args:    []string{"server.js"},
		Env: map[string]string{
			"API_KEY": "secret",
		},
		Timeout: 30 * time.Second,
	}

	if cfg.Name != "test" {
		t.Errorf("Name = %v, want test", cfg.Name)
	}
	if cfg.Command != "node" {
		t.Errorf("Command = %v, want node", cfg.Command)
	}
	if len(cfg.Args) != 1 || cfg.Args[0] != "server.js" {
		t.Errorf("Args = %v, want [server.js]", cfg.Args)
	}
	if cfg.Env["API_KEY"] != "secret" {
		t.Error("Env API_KEY not set correctly")
	}
}

func TestParseMCPConfig(t *testing.T) {
	configJSON := `{
		"mcpServers": {
			"filesystem": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
				"env": {
					"DEBUG": "true"
				}
			},
			"memory": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-memory"]
			}
		}
	}`

	configs, err := ParseMCPConfig([]byte(configJSON))
	if err != nil {
		t.Fatalf("ParseMCPConfig() error = %v", err)
	}

	if len(configs) != 2 {
		t.Errorf("got %d configs, want 2", len(configs))
	}

	// Find filesystem config
	var fsConfig *Config
	for i := range configs {
		if configs[i].Name == "filesystem" {
			fsConfig = &configs[i]
			break
		}
	}

	if fsConfig == nil {
		t.Fatal("filesystem config not found")
	}

	if fsConfig.Command != "npx" {
		t.Errorf("Command = %v, want npx", fsConfig.Command)
	}

	if len(fsConfig.Args) != 3 {
		t.Errorf("Args length = %d, want 3", len(fsConfig.Args))
	}

	if fsConfig.Env["DEBUG"] != "true" {
		t.Error("Env DEBUG not set correctly")
	}
}

func TestToolWithServer(t *testing.T) {
	tool := ToolDefinition{
		Name:        "list_files",
		Description: "List files in a directory",
	}

	twt := ToolWithServer{
		Server: "filesystem",
		Tool:   tool,
	}

	if twt.Server != "filesystem" {
		t.Errorf("Server = %v, want filesystem", twt.Server)
	}
	if twt.Tool.Name != "list_files" {
		t.Errorf("Tool.Name = %v, want list_files", twt.Tool.Name)
	}
}

func TestContentBlock(t *testing.T) {
	textBlock := ContentBlock{
		Type: "text",
		Text: "Hello, world!",
	}

	if textBlock.Type != "text" {
		t.Errorf("Type = %v, want text", textBlock.Type)
	}
	if textBlock.Text != "Hello, world!" {
		t.Errorf("Text = %v, want Hello, world!", textBlock.Text)
	}

	imageBlock := ContentBlock{
		Type:     "image",
		Data:     "base64data",
		MimeType: "image/png",
	}

	if imageBlock.Type != "image" {
		t.Errorf("Type = %v, want image", imageBlock.Type)
	}
	if imageBlock.MimeType != "image/png" {
		t.Errorf("MimeType = %v, want image/png", imageBlock.MimeType)
	}
}

func TestResource(t *testing.T) {
	resource := Resource{
		URI:         "file:///home/user/doc.txt",
		Name:        "Document",
		Description: "A text document",
		MimeType:    "text/plain",
	}

	if resource.URI != "file:///home/user/doc.txt" {
		t.Errorf("URI = %v", resource.URI)
	}
	if resource.Name != "Document" {
		t.Errorf("Name = %v", resource.Name)
	}
	if resource.MimeType != "text/plain" {
		t.Errorf("MimeType = %v", resource.MimeType)
	}
}

func TestServerInfo(t *testing.T) {
	info := ServerInfo{
		Name:         "test-server",
		Version:      "1.0.0",
		ProtocolVer:  "2024-11-05",
		Instructions: "Use this server for testing",
	}

	if info.Name != "test-server" {
		t.Errorf("Name = %v", info.Name)
	}
	if info.Version != "1.0.0" {
		t.Errorf("Version = %v", info.Version)
	}
	if info.ProtocolVer != "2024-11-05" {
		t.Errorf("ProtocolVer = %v", info.ProtocolVer)
	}
}

func TestMCPToolInfo(t *testing.T) {
	info := MCPToolInfo{
		FullName:    "mcp__filesystem__read_file",
		Server:      "filesystem",
		Name:        "read_file",
		Description: "Read a file",
	}

	if info.FullName != "mcp__filesystem__read_file" {
		t.Errorf("FullName = %v", info.FullName)
	}
	if info.Server != "filesystem" {
		t.Errorf("Server = %v", info.Server)
	}
	if info.Name != "read_file" {
		t.Errorf("Name = %v", info.Name)
	}
}
