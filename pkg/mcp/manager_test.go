package mcp

import (
	"context"
	"sync"
	"testing"
	"time"
)

func fastRestartPolicy() RestartPolicy {
	return RestartPolicy{
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		Multiplier:     2,
		MaxRestarts:    3,
	}
}

func TestManager_AddServer_ListServers(t *testing.T) {
	m := NewManager()
	m.AddServer(fakeConfig(t, "normal", "-tools=0"))
	if got := m.ListServers(); len(got) != 1 || got[0] != "fake-normal" {
		t.Fatalf("expected [fake-normal], got %v", got)
	}
}

func TestManager_ConnectServer_NotConfigured(t *testing.T) {
	m := NewManager()
	if err := m.ConnectServer(context.Background(), "missing"); err == nil {
		t.Fatal("expected error connecting to an unconfigured server")
	}
}

func TestManager_Connect_ListToolsAndCall(t *testing.T) {
	m := NewManager()
	cfg := fakeConfig(t, "normal", "-tools=1")
	m.AddServer(cfg)
	t.Cleanup(func() { m.Close() })

	if err := m.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	connected := m.ListConnectedServers()
	if len(connected) != 1 || connected[0] != cfg.Name {
		t.Fatalf("expected connected server %q, got %v", cfg.Name, connected)
	}

	tools := m.AllTools()
	if len(tools) != 4 { // echo, fail, mixed_content, synthetic_0
		t.Fatalf("expected 4 tools, got %d: %+v", len(tools), tools)
	}

	srv, tl, found := m.FindTool("echo")
	if !found || srv != cfg.Name || tl.Name != "echo" {
		t.Fatalf("FindTool(echo) = %q, %+v, %v", srv, tl, found)
	}

	res, err := m.CallTool(context.Background(), cfg.Name, "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got %+v", res)
	}
}

func TestManager_CallTool_ServerNotConnected(t *testing.T) {
	m := NewManager()
	if _, err := m.CallTool(context.Background(), "missing", "echo", nil); err == nil {
		t.Fatal("expected error calling a tool on an unconnected server")
	}
}

func TestManager_DisconnectServer(t *testing.T) {
	m := NewManager()
	cfg := fakeConfig(t, "normal", "-tools=0")
	m.AddServer(cfg)
	if err := m.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := m.DisconnectServer(cfg.Name); err != nil {
		t.Fatalf("DisconnectServer: %v", err)
	}
	if _, ok := m.GetClient(cfg.Name); ok {
		t.Fatal("expected client to be removed after DisconnectServer")
	}
	// Give the (now-stopped) supervisor time to notice, then confirm it
	// did not resurrect the connection.
	time.Sleep(50 * time.Millisecond)
	if _, ok := m.GetClient(cfg.Name); ok {
		t.Fatal("supervisor should not reconnect a server that was explicitly disconnected")
	}
}

func TestManager_ServerStatus(t *testing.T) {
	m := NewManager()
	cfg := fakeConfig(t, "normal", "-tools=0")
	m.AddServer(cfg)
	t.Cleanup(func() { m.Close() })
	if err := m.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	statuses := m.ServerStatus()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	s := statuses[0]
	if !s.Connected {
		t.Fatal("expected connected=true")
	}
	if s.Protocol == "" {
		t.Fatal("expected a negotiated protocol version")
	}
	if s.ToolCount != 3 {
		t.Fatalf("expected 3 tools, got %d", s.ToolCount)
	}
}

func TestManager_HealthCheck(t *testing.T) {
	m := NewManager()
	cfg := fakeConfig(t, "normal", "-tools=0")
	m.AddServer(cfg)
	t.Cleanup(func() { m.Close() })
	if err := m.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	results := m.HealthCheck(context.Background(), 2*time.Second)
	if !results[cfg.Name] {
		t.Fatalf("expected healthy=true for %s, got %v", cfg.Name, results)
	}
}

func TestManager_Close_TerminatesServers(t *testing.T) {
	m := NewManager()
	m.AddServer(fakeConfig(t, "normal", "-tools=0"))
	if err := m.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := m.ListConnectedServers(); len(got) != 0 {
		t.Fatalf("expected no connected servers after Close, got %v", got)
	}
	// Close must be idempotent.
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestManager_RestartOnCrash proves the manager detects a crashed server
// process and reconnects it automatically with backoff, restoring tool
// availability without any caller intervention.
func TestManager_RestartOnCrash(t *testing.T) {
	m := NewManager(WithRestartPolicy(fastRestartPolicy()))
	cfg := fakeConfig(t, "crash")
	m.AddServer(cfg)
	t.Cleanup(func() { m.Close() })

	if err := m.ConnectServer(context.Background(), cfg.Name); err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}

	// Kill the server process by invoking its "crash" tool; ignore the
	// resulting transport error (the process exits without responding).
	_, _ = m.CallTool(context.Background(), cfg.Name, "crash", nil)

	deadline := time.Now().Add(5 * time.Second)
	var lastStatus []ServerStatus
	for time.Now().Before(deadline) {
		lastStatus = m.ServerStatus()
		for _, s := range lastStatus {
			if s.Name == cfg.Name && s.Connected && s.RestartCount >= 1 {
				return // restarted successfully
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected manager to restart the crashed server within 5s, last status: %+v", lastStatus)
}

// TestManager_RestartExhaustion proves the backoff cap eventually gives up
// after MaxRestarts consecutive failures against a server that never comes
// back healthy (it crashes on every call).
func TestManager_RestartExhaustion(t *testing.T) {
	policy := RestartPolicy{
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		Multiplier:     2,
		MaxRestarts:    2,
	}
	m := NewManager(WithRestartPolicy(policy))
	cfg := fakeConfig(t, "crash")
	m.AddServer(cfg)
	t.Cleanup(func() { m.Close() })

	if err := m.ConnectServer(context.Background(), cfg.Name); err != nil {
		t.Fatalf("ConnectServer: %v", err)
	}
	_, _ = m.CallTool(context.Background(), cfg.Name, "crash", nil)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.GetClient(cfg.Name); !ok {
			return // manager gave up, as expected
		}
		// Re-crash it each time it comes back, since a fresh "crash"
		// process serves the crash tool again after every restart.
		_, _ = m.CallTool(context.Background(), cfg.Name, "crash", nil)
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected manager to give up restarting after exhausting MaxRestarts")
}

func TestManager_ConcurrentAccess(t *testing.T) {
	m := NewManager()
	cfg := fakeConfig(t, "normal", "-tools=0")
	m.AddServer(cfg)
	t.Cleanup(func() { m.Close() })
	if err := m.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.AllTools()
			m.ServerStatus()
			m.ListConnectedServers()
			_, _ = m.CallTool(context.Background(), cfg.Name, "echo", map[string]any{"text": "x"})
		}()
	}
	wg.Wait()
}

func TestNextBackoff_CapsAtMax(t *testing.T) {
	policy := RestartPolicy{InitialBackoff: time.Second, MaxBackoff: 3 * time.Second, Multiplier: 2}
	got := nextBackoff(2*time.Second, policy)
	if got != 3*time.Second {
		t.Fatalf("expected backoff capped at 3s, got %s", got)
	}
}
