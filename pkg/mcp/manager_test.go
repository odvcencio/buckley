package mcp

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	m := NewManager()

	if m == nil {
		t.Fatal("NewManager() returned nil")
	}

	if m.clients == nil {
		t.Error("clients map not initialized")
	}

	if m.configs == nil {
		t.Error("configs map not initialized")
	}

	if len(m.clients) != 0 {
		t.Error("clients map should be empty initially")
	}

	if len(m.configs) != 0 {
		t.Error("configs map should be empty initially")
	}
}

func TestManager_AddServerConfig(t *testing.T) {
	m := NewManager()

	cfg := Config{
		Name:    "server1",
		Command: "node",
		Args:    []string{"server.js"},
		Env:     map[string]string{"DEBUG": "true"},
		Timeout: 30 * time.Second,
	}

	m.AddServer(cfg)

	servers := m.ListServers()
	if len(servers) != 1 {
		t.Errorf("expected 1 server, got %d", len(servers))
	}

	if servers[0] != "server1" {
		t.Errorf("expected server1, got %v", servers[0])
	}
}

func TestManager_AddServer_Multiple(t *testing.T) {
	m := NewManager()

	configs := []Config{
		{Name: "server1", Command: "cmd1"},
		{Name: "server2", Command: "cmd2"},
		{Name: "server3", Command: "cmd3"},
	}

	for _, cfg := range configs {
		m.AddServer(cfg)
	}

	servers := m.ListServers()
	if len(servers) != 3 {
		t.Errorf("expected 3 servers, got %d", len(servers))
	}
}

func TestManager_AddServer_Override(t *testing.T) {
	m := NewManager()

	cfg1 := Config{Name: "server", Command: "cmd1"}
	cfg2 := Config{Name: "server", Command: "cmd2"}

	m.AddServer(cfg1)
	m.AddServer(cfg2)

	// Should have overwritten, so still 1 server
	servers := m.ListServers()
	if len(servers) != 1 {
		t.Errorf("expected 1 server after override, got %d", len(servers))
	}

	// Verify the config was updated
	statuses := m.ServerStatus()
	if statuses[0].Command != "cmd2" {
		t.Errorf("expected cmd2 after override, got %v", statuses[0].Command)
	}
}

func TestManager_ListServers_Empty(t *testing.T) {
	m := NewManager()

	servers := m.ListServers()
	if servers == nil {
		t.Error("ListServers should return empty slice, not nil")
	}
	if len(servers) != 0 {
		t.Errorf("expected empty slice, got %d servers", len(servers))
	}
}

func TestManager_ListConnectedServers_Empty(t *testing.T) {
	m := NewManager()

	servers := m.ListConnectedServers()
	if servers == nil {
		t.Error("ListConnectedServers should return empty slice, not nil")
	}
	if len(servers) != 0 {
		t.Errorf("expected empty slice, got %d servers", len(servers))
	}
}

func TestManager_ListConnectedServers_NoConnections(t *testing.T) {
	m := NewManager()
	m.AddServer(Config{Name: "server1", Command: "cmd1"})
	m.AddServer(Config{Name: "server2", Command: "cmd2"})

	// Added servers but not connected
	connected := m.ListConnectedServers()
	if len(connected) != 0 {
		t.Errorf("expected 0 connected servers, got %d", len(connected))
	}
}

func TestManager_GetClient_NotFound(t *testing.T) {
	m := NewManager()

	client, found := m.GetClient("nonexistent")
	if found {
		t.Error("expected found=false for nonexistent client")
	}
	if client != nil {
		t.Error("expected nil client for nonexistent server")
	}
}

func TestManager_DisconnectServer_NotConnected(t *testing.T) {
	m := NewManager()

	// Should return nil for non-existent server
	err := m.DisconnectServer("nonexistent")
	if err != nil {
		t.Errorf("expected nil error for disconnecting non-existent server, got %v", err)
	}
}

func TestManager_ConnectServer_NotConfigured(t *testing.T) {
	m := NewManager()

	ctx := context.Background()
	err := m.ConnectServer(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for connecting to non-configured server")
	}

	if err.Error() != "server not configured: nonexistent" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManager_CallTool_NotConnected(t *testing.T) {
	m := NewManager()
	m.AddServer(Config{Name: "server1", Command: "cmd"})

	ctx := context.Background()
	_, err := m.CallTool(ctx, "server1", "some_tool", nil)
	if err == nil {
		t.Error("expected error for calling tool on non-connected server")
	}

	if err.Error() != "server not connected: server1" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManager_FindTool_NotFound(t *testing.T) {
	m := NewManager()

	serverName, tool, found := m.FindTool("nonexistent_tool")
	if found {
		t.Error("expected found=false for nonexistent tool")
	}
	if serverName != "" {
		t.Errorf("expected empty server name, got %v", serverName)
	}
	if tool != nil {
		t.Error("expected nil tool for nonexistent tool")
	}
}

func TestManager_AllTools_Empty(t *testing.T) {
	m := NewManager()

	tools := m.AllTools()
	// AllTools returns nil when no clients are connected (acceptable behavior)
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestManager_ServerStatus_NotConnected(t *testing.T) {
	m := NewManager()
	m.AddServer(Config{
		Name:    "test-server",
		Command: "/usr/bin/test",
	})

	statuses := m.ServerStatus()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	status := statuses[0]
	if status.Name != "test-server" {
		t.Errorf("Name = %v, want test-server", status.Name)
	}
	if status.Command != "/usr/bin/test" {
		t.Errorf("Command = %v, want /usr/bin/test", status.Command)
	}
	if status.Connected {
		t.Error("expected Connected=false")
	}
	if status.ToolCount != 0 {
		t.Errorf("expected ToolCount=0, got %d", status.ToolCount)
	}
	if status.ResourceCount != 0 {
		t.Errorf("expected ResourceCount=0, got %d", status.ResourceCount)
	}
}

func TestManager_Close_Empty(t *testing.T) {
	m := NewManager()

	err := m.Close()
	if err != nil {
		t.Errorf("expected nil error for closing empty manager, got %v", err)
	}
}

func TestManager_Refresh_NoClients(t *testing.T) {
	m := NewManager()

	ctx := context.Background()
	err := m.Refresh(ctx)
	if err != nil {
		t.Errorf("expected nil error for refreshing with no clients, got %v", err)
	}
}

func TestManager_HealthCheck_NoClients(t *testing.T) {
	m := NewManager()

	ctx := context.Background()
	results := m.HealthCheck(ctx, time.Second)
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

// TestManager_ConcurrentAccess tests that the manager is safe for concurrent use
func TestManager_ConcurrentAccess(t *testing.T) {
	m := NewManager()

	var wg sync.WaitGroup
	const numGoroutines = 50

	// Concurrent AddServer calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.AddServer(Config{
				Name:    "server",
				Command: "cmd",
			})
		}(i)
	}

	// Concurrent ListServers calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.ListServers()
		}()
	}

	// Concurrent ListConnectedServers calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.ListConnectedServers()
		}()
	}

	// Concurrent GetClient calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.GetClient("server")
		}()
	}

	// Concurrent AllTools calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.AllTools()
		}()
	}

	// Concurrent FindTool calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = m.FindTool("some_tool")
		}()
	}

	// Concurrent ServerStatus calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.ServerStatus()
		}()
	}

	wg.Wait()
}

func TestToolWithServer_Struct(t *testing.T) {
	tool := ToolDefinition{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]any{
			"type": "object",
		},
	}

	twt := ToolWithServer{
		Server: "my-server",
		Tool:   tool,
	}

	if twt.Server != "my-server" {
		t.Errorf("Server = %v, want my-server", twt.Server)
	}

	if twt.Tool.Name != "test_tool" {
		t.Errorf("Tool.Name = %v, want test_tool", twt.Tool.Name)
	}
}

func TestServerStatus_Fields(t *testing.T) {
	status := ServerStatus{
		Name:          "test-server",
		Command:       "/usr/bin/mcp-server",
		Connected:     true,
		Version:       "1.0.0",
		Protocol:      "2024-11-05",
		ToolCount:     5,
		ResourceCount: 3,
	}

	if status.Name != "test-server" {
		t.Errorf("Name = %v", status.Name)
	}
	if status.Command != "/usr/bin/mcp-server" {
		t.Errorf("Command = %v", status.Command)
	}
	if !status.Connected {
		t.Error("Connected should be true")
	}
	if status.Version != "1.0.0" {
		t.Errorf("Version = %v", status.Version)
	}
	if status.Protocol != "2024-11-05" {
		t.Errorf("Protocol = %v", status.Protocol)
	}
	if status.ToolCount != 5 {
		t.Errorf("ToolCount = %d", status.ToolCount)
	}
	if status.ResourceCount != 3 {
		t.Errorf("ResourceCount = %d", status.ResourceCount)
	}
}

// TestManager_WithMockClient tests manager operations with a mock client
func TestManager_WithMockClient(t *testing.T) {
	m := NewManager()

	// Manually inject a mock client for testing
	mockClient := &Client{
		serverID: "mock-server",
		serverInfo: &ServerInfo{
			Name:        "Mock Server",
			Version:     "1.0.0",
			ProtocolVer: "2024-11-05",
		},
		tools: []ToolDefinition{
			{Name: "tool1", Description: "Tool 1"},
			{Name: "tool2", Description: "Tool 2"},
		},
		resources: []Resource{
			{URI: "file:///a", Name: "Resource A"},
		},
		pending: make(map[int64]chan *Message),
	}

	m.mu.Lock()
	m.configs["mock-server"] = Config{Name: "mock-server", Command: "mock"}
	m.clients["mock-server"] = mockClient
	m.mu.Unlock()

	// Test GetClient
	client, found := m.GetClient("mock-server")
	if !found {
		t.Error("expected to find mock client")
	}
	if client != mockClient {
		t.Error("returned client doesn't match mock client")
	}

	// Test ListConnectedServers
	connected := m.ListConnectedServers()
	if len(connected) != 1 || connected[0] != "mock-server" {
		t.Errorf("unexpected connected servers: %v", connected)
	}

	// Test AllTools
	tools := m.AllTools()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}

	// Test FindTool
	serverName, tool, found := m.FindTool("tool1")
	if !found {
		t.Error("expected to find tool1")
	}
	if serverName != "mock-server" {
		t.Errorf("server name = %v, want mock-server", serverName)
	}
	if tool.Name != "tool1" {
		t.Errorf("tool name = %v, want tool1", tool.Name)
	}

	// Test FindTool not found
	_, _, found = m.FindTool("nonexistent")
	if found {
		t.Error("should not find nonexistent tool")
	}

	// Test ServerStatus with connected client
	statuses := m.ServerStatus()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	status := statuses[0]
	if !status.Connected {
		t.Error("expected Connected=true")
	}
	if status.Version != "1.0.0" {
		t.Errorf("Version = %v, want 1.0.0", status.Version)
	}
	if status.ToolCount != 2 {
		t.Errorf("ToolCount = %d, want 2", status.ToolCount)
	}
	if status.ResourceCount != 1 {
		t.Errorf("ResourceCount = %d, want 1", status.ResourceCount)
	}
}

func TestManager_Connect_FailedClient(t *testing.T) {
	m := NewManager()
	m.AddServer(Config{
		Name:    "bad-server",
		Command: "nonexistent-command-xyz123",
	})

	ctx := context.Background()
	err := m.Connect(ctx)

	// Should error because command doesn't exist
	if err == nil {
		t.Error("expected error for failed connection")
	}

	// Server should not be in connected list
	connected := m.ListConnectedServers()
	if len(connected) != 0 {
		t.Errorf("expected 0 connected servers, got %d", len(connected))
	}
}

func TestManager_ConnectServer_FailedClient(t *testing.T) {
	m := NewManager()
	m.AddServer(Config{
		Name:    "bad-server",
		Command: "nonexistent-command-xyz123",
	})

	ctx := context.Background()
	err := m.ConnectServer(ctx, "bad-server")

	if err == nil {
		t.Error("expected error for failed connection")
	}
}

func TestManager_DisconnectServer_WithClient(t *testing.T) {
	m := NewManager()

	// Manually inject a mock client that's already closed (to avoid cmd.Wait panic)
	mockClient := &Client{
		serverID: "test-server",
		pending:  make(map[int64]chan *Message),
		closed:   true, // Already closed to skip close logic
		stdin:    &mockCloser{},
		stdout:   &mockCloser{},
		stderr:   &mockCloser{},
	}

	m.mu.Lock()
	m.configs["test-server"] = Config{Name: "test-server", Command: "cmd"}
	m.clients["test-server"] = mockClient
	m.mu.Unlock()

	err := m.DisconnectServer("test-server")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify client was removed
	_, found := m.GetClient("test-server")
	if found {
		t.Error("expected client to be removed after disconnect")
	}
}

// mockCloser implements io.Closer for testing
type mockCloser struct {
	closed bool
}

func (m *mockCloser) Close() error {
	m.closed = true
	return nil
}

func (m *mockCloser) Read(p []byte) (n int, err error) {
	return 0, nil
}

func (m *mockCloser) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func TestManager_Close_WithClients(t *testing.T) {
	m := NewManager()

	// Add multiple mock clients that are already closed
	for i := 0; i < 3; i++ {
		name := "server" + string(rune('A'+i))
		mockClient := &Client{
			serverID: name,
			pending:  make(map[int64]chan *Message),
			closed:   true, // Already closed to skip close logic with nil cmd
			stdin:    &mockCloser{},
			stdout:   &mockCloser{},
			stderr:   &mockCloser{},
		}
		m.mu.Lock()
		m.configs[name] = Config{Name: name, Command: "cmd"}
		m.clients[name] = mockClient
		m.mu.Unlock()
	}

	err := m.Close()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify all clients were removed
	connected := m.ListConnectedServers()
	if len(connected) != 0 {
		t.Errorf("expected 0 connected servers after close, got %d", len(connected))
	}
}

func TestManager_Refresh_WithClient(t *testing.T) {
	m := NewManager()

	// Refresh with no clients should be a no-op
	ctx := context.Background()
	err := m.Refresh(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Add a config but no client (simulate partially disconnected state)
	m.mu.Lock()
	m.configs["test"] = Config{Name: "test", Command: "cmd"}
	// Don't add client - this tests the early continue path
	m.mu.Unlock()

	err = m.Refresh(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestManager_HealthCheck_NoConnectedClients(t *testing.T) {
	m := NewManager()

	// Add a config but remove the client during iteration to test the !ok path
	m.mu.Lock()
	m.configs["test"] = Config{Name: "test", Command: "cmd"}
	m.mu.Unlock()

	ctx := context.Background()
	results := m.HealthCheck(ctx, 100*time.Millisecond)

	// No clients connected, so no results
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestManager_Connect_AlreadyConnected(t *testing.T) {
	m := NewManager()

	// Add a mock client that's already "connected"
	mockClient := &Client{
		serverID: "existing",
		pending:  make(map[int64]chan *Message),
	}

	cfg := Config{Name: "existing", Command: "cmd"}
	m.mu.Lock()
	m.configs["existing"] = cfg
	m.clients["existing"] = mockClient
	m.mu.Unlock()

	ctx := context.Background()
	err := m.Connect(ctx)
	// Should not error since server is already connected
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Should still have the original client
	connected := m.ListConnectedServers()
	if len(connected) != 1 {
		t.Errorf("expected 1 connected server, got %d", len(connected))
	}
}

func TestManager_ConnectServer_AlreadyConnected(t *testing.T) {
	m := NewManager()

	mockClient := &Client{
		serverID: "existing",
		pending:  make(map[int64]chan *Message),
	}

	cfg := Config{Name: "existing", Command: "cmd"}
	m.mu.Lock()
	m.configs["existing"] = cfg
	m.clients["existing"] = mockClient
	m.mu.Unlock()

	ctx := context.Background()
	err := m.ConnectServer(ctx, "existing")

	// Should return nil (already connected)
	if err != nil {
		t.Errorf("expected nil for already connected server, got %v", err)
	}
}

func TestManager_ServerStatus_Connected(t *testing.T) {
	m := NewManager()

	mockClient := &Client{
		serverID: "connected",
		serverInfo: &ServerInfo{
			Name:        "Connected Server",
			Version:     "2.0.0",
			ProtocolVer: "2024-11-05",
		},
		tools: []ToolDefinition{
			{Name: "tool1"},
			{Name: "tool2"},
		},
		resources: []Resource{
			{URI: "file:///a"},
			{URI: "file:///b"},
			{URI: "file:///c"},
		},
		pending: make(map[int64]chan *Message),
	}

	cfg := Config{Name: "connected", Command: "/path/to/server"}
	m.mu.Lock()
	m.configs["connected"] = cfg
	m.clients["connected"] = mockClient
	m.mu.Unlock()

	statuses := m.ServerStatus()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	status := statuses[0]
	if !status.Connected {
		t.Error("expected Connected=true")
	}
	if status.Version != "2.0.0" {
		t.Errorf("Version = %v, want 2.0.0", status.Version)
	}
	if status.Protocol != "2024-11-05" {
		t.Errorf("Protocol = %v", status.Protocol)
	}
	if status.ToolCount != 2 {
		t.Errorf("ToolCount = %d, want 2", status.ToolCount)
	}
	if status.ResourceCount != 3 {
		t.Errorf("ResourceCount = %d, want 3", status.ResourceCount)
	}
}

func TestManager_ServerStatus_NilServerInfo(t *testing.T) {
	m := NewManager()

	mockClient := &Client{
		serverID:   "no-info",
		serverInfo: nil, // No server info
		tools:      []ToolDefinition{{Name: "tool1"}},
		pending:    make(map[int64]chan *Message),
	}

	cfg := Config{Name: "no-info", Command: "cmd"}
	m.mu.Lock()
	m.configs["no-info"] = cfg
	m.clients["no-info"] = mockClient
	m.mu.Unlock()

	statuses := m.ServerStatus()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	status := statuses[0]
	if !status.Connected {
		t.Error("expected Connected=true even without server info")
	}
	// Version and Protocol should be empty
	if status.Version != "" {
		t.Errorf("expected empty Version, got %v", status.Version)
	}
	if status.Protocol != "" {
		t.Errorf("expected empty Protocol, got %v", status.Protocol)
	}
}
