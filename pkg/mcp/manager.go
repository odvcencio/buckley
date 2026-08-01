package mcp

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// RestartPolicy controls how Manager reconnects a server after its process
// exits unexpectedly. Backoff grows geometrically from InitialBackoff by
// Multiplier, capped at MaxBackoff. MaxRestarts bounds the number of
// consecutive failed restart attempts before the manager gives up on a
// server (0 means unlimited).
type RestartPolicy struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
	MaxRestarts    int
}

// DefaultRestartPolicy is used when a Manager is created without an
// explicit policy.
func DefaultRestartPolicy() RestartPolicy {
	return RestartPolicy{
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     30 * time.Second,
		Multiplier:     2,
		MaxRestarts:    5,
	}
}

func (p RestartPolicy) normalized() RestartPolicy {
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = DefaultRestartPolicy().InitialBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = DefaultRestartPolicy().MaxBackoff
	}
	if p.Multiplier <= 1 {
		p.Multiplier = DefaultRestartPolicy().Multiplier
	}
	return p
}

// ToolWithServer pairs a tool definition with the server that exposes it.
type ToolWithServer struct {
	Server string
	Tool   *sdkmcp.Tool
}

// ServerStatus represents the current status of an MCP server.
type ServerStatus struct {
	Name         string
	Command      string
	Connected    bool
	Version      string
	Protocol     string
	ToolCount    int
	RestartCount int
	LastError    string
}

// Manager manages the lifecycle of multiple MCP server connections:
// configuration, connection, tool discovery, crash detection, and
// restart-with-backoff.
type Manager struct {
	restartPolicy RestartPolicy

	mu      sync.RWMutex
	clients map[string]*Client
	configs map[string]Config
	states  map[string]*serverState
	closed  bool

	healthMu           sync.Mutex
	healthCache        map[string]healthStatus
	healthCheckTTL     time.Duration
	healthCheckTimeout time.Duration
}

type serverState struct {
	restarts int
	lastErr  error
	// stop is closed exactly once, by whichever Manager method (Close or
	// DisconnectServer) removes this server's entry from m.states, to
	// signal the supervisor goroutine to stop without attempting a
	// restart.
	stop chan struct{}
}

type healthStatus struct {
	checkedAt time.Time
	err       error
}

// ManagerOption configures a Manager at construction time.
type ManagerOption func(*Manager)

// WithRestartPolicy overrides the default restart backoff policy. Mainly
// used by tests to shrink backoff windows.
func WithRestartPolicy(p RestartPolicy) ManagerOption {
	return func(m *Manager) {
		m.restartPolicy = p.normalized()
	}
}

// NewManager creates a new MCP manager.
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		restartPolicy:      DefaultRestartPolicy(),
		clients:            make(map[string]*Client),
		configs:            make(map[string]Config),
		states:             make(map[string]*serverState),
		healthCache:        make(map[string]healthStatus),
		healthCheckTTL:     30 * time.Second,
		healthCheckTimeout: 2 * time.Second,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// AddServer registers a server configuration without connecting to it.
func (m *Manager) AddServer(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[cfg.Name] = cfg
}

// Connect connects to every configured server that is not already
// connected. It returns a combined error if any server fails, but leaves
// successfully connected servers running.
func (m *Manager) Connect(ctx context.Context) error {
	m.mu.RLock()
	names := make([]string, 0, len(m.configs))
	for name := range m.configs {
		names = append(names, name)
	}
	m.mu.RUnlock()

	var errs []string
	for _, name := range names {
		if err := m.ConnectServer(ctx, name); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mcp: failed to connect to some servers: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ConnectServer connects to a specific configured server by name and starts
// its crash-supervision goroutine.
func (m *Manager) ConnectServer(ctx context.Context, name string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("mcp: manager closed")
	}
	cfg, ok := m.configs[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp: server not configured: %s", name)
	}
	if _, exists := m.clients[name]; exists {
		m.mu.Unlock()
		return nil // already connected
	}
	m.mu.Unlock()

	client, err := NewClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("mcp: %s: %w", name, err)
	}
	if _, err := client.ListTools(ctx); err != nil {
		// Non-fatal: some servers may not expose tools, but log so
		// operators can see it.
		log.Printf("mcp: %s: tools/list after connect: %v", name, err)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		client.Close()
		return fmt.Errorf("mcp: manager closed")
	}
	m.clients[name] = client
	state := &serverState{stop: make(chan struct{})}
	m.states[name] = state
	m.mu.Unlock()

	go m.superviseServer(name, state)
	return nil
}

// superviseServer blocks on the client's session until it closes, then
// attempts a reconnect with geometric backoff (capped) up to
// RestartPolicy.MaxRestarts consecutive failures. It exits when the server
// is explicitly disconnected/closed or restarts are exhausted.
func (m *Manager) superviseServer(name string, state *serverState) {
	backoff := m.restartPolicy.InitialBackoff
	for {
		m.mu.RLock()
		client, ok := m.clients[name]
		m.mu.RUnlock()
		if !ok {
			return
		}

		waitErr := client.Wait()

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		// If the client was already replaced or removed (DisconnectServer),
		// this crash signal is stale; stop supervising.
		current, stillTracked := m.clients[name]
		if !stillTracked || current != client {
			m.mu.Unlock()
			return
		}
		cfg := m.configs[name]
		state.lastErr = waitErr
		state.restarts++
		restarts := state.restarts
		m.mu.Unlock()

		if m.restartPolicy.MaxRestarts > 0 && restarts > m.restartPolicy.MaxRestarts {
			log.Printf("mcp: %s: exceeded max restarts (%d); giving up", name, m.restartPolicy.MaxRestarts)
			m.mu.Lock()
			delete(m.clients, name)
			delete(m.states, name)
			m.clearHealthLocked(name)
			m.mu.Unlock()
			return
		}

		log.Printf("mcp: %s: server connection closed (%v); restarting in %s (attempt %d)", name, waitErr, backoff, restarts)
		select {
		case <-time.After(backoff):
		case <-state.stop:
			return
		}

		backoff = nextBackoff(backoff, m.restartPolicy)

		newClient, err := NewClient(context.Background(), cfg)
		if err != nil {
			log.Printf("mcp: %s: restart failed: %v", name, err)
			continue
		}
		if _, err := newClient.ListTools(context.Background()); err != nil {
			log.Printf("mcp: %s: tools/list after restart: %v", name, err)
		}

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			newClient.Close()
			return
		}
		m.clients[name] = newClient
		m.clearHealthLocked(name)
		m.mu.Unlock()
		backoff = m.restartPolicy.InitialBackoff // reset after a clean reconnect
	}
}

func nextBackoff(current time.Duration, policy RestartPolicy) time.Duration {
	next := time.Duration(float64(current) * policy.Multiplier)
	if next > policy.MaxBackoff {
		next = policy.MaxBackoff
	}
	if next <= 0 {
		next = policy.InitialBackoff
	}
	return next
}

// DisconnectServer disconnects from a specific server, stopping its
// supervisor and terminating the process cleanly.
func (m *Manager) DisconnectServer(name string) error {
	m.mu.Lock()
	client, ok := m.clients[name]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.clients, name)
	state, hadState := m.states[name]
	delete(m.states, name)
	m.clearHealthLocked(name)
	m.mu.Unlock()

	if hadState {
		close(state.stop)
	}
	return client.Close()
}

// GetClient returns a client by server name.
func (m *Manager) GetClient(name string) (*Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, ok := m.clients[name]
	return client, ok
}

// ListServers returns all configured server names.
func (m *Manager) ListServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.configs))
	for name := range m.configs {
		names = append(names, name)
	}
	return names
}

// ListConnectedServers returns all currently connected server names.
func (m *Manager) ListConnectedServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// AllTools returns all cached tools from all connected servers.
func (m *Manager) AllTools() []ToolWithServer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var tools []ToolWithServer
	for serverName, client := range m.clients {
		for _, t := range client.Tools() {
			tools = append(tools, ToolWithServer{Server: serverName, Tool: t})
		}
	}
	return tools
}

// CallTool calls a tool on the named server after a cheap health check.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	if err := m.ensureHealthy(ctx, serverName); err != nil {
		return nil, err
	}
	m.mu.RLock()
	client, ok := m.clients[serverName]
	m.mu.RUnlock()
	if !ok || client == nil {
		return nil, fmt.Errorf("mcp: server not connected: %s", serverName)
	}
	return client.CallTool(ctx, toolName, args)
}

// FindTool finds a tool by name across all connected servers.
func (m *Manager) FindTool(toolName string) (serverName string, tool *sdkmcp.Tool, found bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for srvName, client := range m.clients {
		for _, t := range client.Tools() {
			if t.Name == toolName {
				return srvName, t, true
			}
		}
	}
	return "", nil, false
}

// ServerStatus returns the status of every configured server.
func (m *Manager) ServerStatus() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var statuses []ServerStatus
	for name, cfg := range m.configs {
		status := ServerStatus{Name: name, Command: cfg.Command}
		if state, ok := m.states[name]; ok {
			status.RestartCount = state.restarts
			if state.lastErr != nil {
				status.LastError = state.lastErr.Error()
			}
		}
		if client, ok := m.clients[name]; ok {
			status.Connected = true
			if info := client.ServerInfo(); info != nil {
				status.Version = info.Version
			}
			status.Protocol = client.ProtocolVersion()
			status.ToolCount = len(client.Tools())
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// Refresh reconnects the tool list for every connected server.
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.RLock()
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	m.mu.RUnlock()

	var errs []string
	for _, name := range names {
		m.mu.RLock()
		client := m.clients[name]
		m.mu.RUnlock()
		if client == nil {
			continue
		}
		if _, err := client.ListTools(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mcp: refresh errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Close disconnects from all servers, stopping every supervisor and
// terminating every server process. It is safe to call multiple times.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	states := m.states
	clients := m.clients
	m.clients = make(map[string]*Client)
	m.states = make(map[string]*serverState)
	m.mu.Unlock()

	for _, state := range states {
		close(state.stop)
	}

	var errs []string
	var wg sync.WaitGroup
	var mu sync.Mutex
	for name, client := range clients {
		wg.Add(1)
		go func(name string, client *Client) {
			defer wg.Done()
			if err := client.Close(); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
				mu.Unlock()
			}
		}(name, client)
	}
	wg.Wait()

	m.healthMu.Lock()
	m.healthCache = make(map[string]healthStatus)
	m.healthMu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("mcp: errors closing servers: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) ensureHealthy(ctx context.Context, serverName string) error {
	if serverName == "" {
		return fmt.Errorf("mcp: server name required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cached, ok := m.cachedHealth(serverName); ok {
		return cached
	}
	if _, ok := m.GetClient(serverName); !ok {
		err := fmt.Errorf("mcp: server not connected: %s", serverName)
		m.recordHealth(serverName, err)
		return err
	}
	err := m.checkServerHealth(ctx, serverName)
	m.recordHealth(serverName, err)
	return err
}

func (m *Manager) checkServerHealth(ctx context.Context, serverName string) error {
	client, ok := m.GetClient(serverName)
	if !ok || client == nil {
		return fmt.Errorf("mcp: server not connected: %s", serverName)
	}
	timeout := m.healthCheckTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := client.ListTools(checkCtx); err != nil {
		return fmt.Errorf("mcp: health check failed: %w", err)
	}
	return nil
}

func (m *Manager) cachedHealth(serverName string) (error, bool) {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	status, ok := m.healthCache[serverName]
	if !ok {
		return nil, false
	}
	ttl := m.healthCheckTTL
	if ttl <= 0 {
		return nil, false
	}
	if time.Since(status.checkedAt) <= ttl {
		return status.err, true
	}
	return nil, false
}

func (m *Manager) recordHealth(serverName string, err error) {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	m.healthCache[serverName] = healthStatus{checkedAt: time.Now(), err: err}
}

func (m *Manager) clearHealthLocked(serverName string) {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	delete(m.healthCache, serverName)
}

// HealthCheck checks the health of all connected servers, returning true
// for servers that respond within timeout.
func (m *Manager) HealthCheck(ctx context.Context, timeout time.Duration) map[string]bool {
	m.mu.RLock()
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	m.mu.RUnlock()

	results := make(map[string]bool)
	for _, name := range names {
		m.mu.RLock()
		client := m.clients[name]
		m.mu.RUnlock()
		if client == nil {
			results[name] = false
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		_, err := client.ListTools(checkCtx)
		cancel()
		results[name] = err == nil
	}
	return results
}
