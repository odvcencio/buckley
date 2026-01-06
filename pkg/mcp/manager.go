package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Manager manages multiple MCP server connections
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	configs map[string]Config
}

// NewManager creates a new MCP manager
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		configs: make(map[string]Config),
	}
}

// AddServer adds a server configuration
func (m *Manager) AddServer(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[cfg.Name] = cfg
}

// Connect connects to all configured servers
func (m *Manager) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string

	for name, cfg := range m.configs {
		if _, exists := m.clients[name]; exists {
			continue // Already connected
		}

		client, err := NewClient(cfg)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}

		if err := client.Initialize(ctx); err != nil {
			client.Close()
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}

		// Fetch tools
		if _, err := client.ListTools(ctx); err != nil {
			// Non-fatal, some servers may not have tools
		}

		m.clients[name] = client
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to connect to some servers: %s", strings.Join(errs, "; "))
	}

	return nil
}

// ConnectServer connects to a specific server by name
func (m *Manager) ConnectServer(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.configs[name]
	if !ok {
		return fmt.Errorf("server not configured: %s", name)
	}

	if _, exists := m.clients[name]; exists {
		return nil // Already connected
	}

	client, err := NewClient(cfg)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	if err := client.Initialize(ctx); err != nil {
		client.Close()
		return fmt.Errorf("failed to initialize: %w", err)
	}

	if _, err := client.ListTools(ctx); err != nil {
		// Non-fatal
	}

	m.clients[name] = client
	return nil
}

// DisconnectServer disconnects from a specific server
func (m *Manager) DisconnectServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, ok := m.clients[name]
	if !ok {
		return nil
	}

	delete(m.clients, name)
	return client.Close()
}

// GetClient returns a client by server name
func (m *Manager) GetClient(name string) (*Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[name]
	return client, ok
}

// ListServers returns all configured server names
func (m *Manager) ListServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.configs))
	for name := range m.configs {
		names = append(names, name)
	}
	return names
}

// ListConnectedServers returns all connected server names
func (m *Manager) ListConnectedServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// AllTools returns all tools from all connected servers
func (m *Manager) AllTools() []ToolWithServer {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var tools []ToolWithServer
	for serverName, client := range m.clients {
		for _, tool := range client.Tools() {
			tools = append(tools, ToolWithServer{
				Server: serverName,
				Tool:   tool,
			})
		}
	}
	return tools
}

// ToolWithServer pairs a tool definition with its server
type ToolWithServer struct {
	Server string
	Tool   ToolDefinition
}

// CallTool calls a tool on the appropriate server
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*ToolCallResult, error) {
	m.mu.RLock()
	client, ok := m.clients[serverName]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("server not connected: %s", serverName)
	}

	return client.CallTool(ctx, toolName, args)
}

// FindTool finds a tool by name across all servers
func (m *Manager) FindTool(toolName string) (serverName string, tool *ToolDefinition, found bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for srvName, client := range m.clients {
		for _, t := range client.Tools() {
			if t.Name == toolName {
				return srvName, &t, true
			}
		}
	}
	return "", nil, false
}

// ServerStatus returns the status of all servers
func (m *Manager) ServerStatus() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var statuses []ServerStatus
	for name, cfg := range m.configs {
		status := ServerStatus{
			Name:      name,
			Command:   cfg.Command,
			Connected: false,
		}

		if client, ok := m.clients[name]; ok {
			status.Connected = true
			if info := client.ServerInfo(); info != nil {
				status.Version = info.Version
				status.Protocol = info.ProtocolVer
			}
			status.ToolCount = len(client.Tools())
			status.ResourceCount = len(client.Resources())
		}

		statuses = append(statuses, status)
	}
	return statuses
}

// ServerStatus represents the current status of an MCP server
type ServerStatus struct {
	Name          string
	Command       string
	Connected     bool
	Version       string
	Protocol      string
	ToolCount     int
	ResourceCount int
}

// Refresh reconnects to servers and refreshes tool lists
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.RLock()
	serverNames := make([]string, 0, len(m.clients))
	for name := range m.clients {
		serverNames = append(serverNames, name)
	}
	m.mu.RUnlock()

	for _, name := range serverNames {
		m.mu.RLock()
		client, ok := m.clients[name]
		m.mu.RUnlock()

		if !ok {
			continue
		}

		// Refresh tools list
		if _, err := client.ListTools(ctx); err != nil {
			// Log but continue
		}
	}

	return nil
}

// Close disconnects from all servers
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	m.clients = make(map[string]*Client)

	if len(errs) > 0 {
		return fmt.Errorf("errors closing servers: %s", strings.Join(errs, "; "))
	}
	return nil
}

// HealthCheck checks the health of all connected servers
func (m *Manager) HealthCheck(ctx context.Context, timeout time.Duration) map[string]bool {
	m.mu.RLock()
	serverNames := make([]string, 0, len(m.clients))
	for name := range m.clients {
		serverNames = append(serverNames, name)
	}
	m.mu.RUnlock()

	results := make(map[string]bool)
	for _, name := range serverNames {
		m.mu.RLock()
		client, ok := m.clients[name]
		m.mu.RUnlock()

		if !ok {
			results[name] = false
			continue
		}

		// Try to list tools as a health check
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		_, err := client.ListTools(checkCtx)
		cancel()

		results[name] = err == nil
	}

	return results
}
