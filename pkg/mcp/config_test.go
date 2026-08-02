package mcp

import (
	"context"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestManagerFromConfig_Disabled(t *testing.T) {
	m, err := ManagerFromConfig(context.Background(), config.MCPConfig{Enabled: false})
	if err != nil || m != nil {
		t.Fatalf("expected (nil, nil) when MCP is disabled, got (%v, %v)", m, err)
	}
}

func TestManagerFromConfig_NoServers(t *testing.T) {
	m, err := ManagerFromConfig(context.Background(), config.MCPConfig{Enabled: true})
	if err != nil || m != nil {
		t.Fatalf("expected (nil, nil) when no servers are configured, got (%v, %v)", m, err)
	}
}

func TestManagerFromConfig_AllServersDisabled(t *testing.T) {
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{Name: "fs", Command: fakeServerBin, Enabled: false},
		},
	}
	m, err := ManagerFromConfig(context.Background(), cfg)
	if err != nil || m != nil {
		t.Fatalf("expected (nil, nil) when every server is disabled, got (%v, %v)", m, err)
	}
}

func TestManagerFromConfig_InvalidConfig(t *testing.T) {
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{Name: "", Command: fakeServerBin, Enabled: true},
		},
	}
	if _, err := ManagerFromConfig(context.Background(), cfg); err == nil {
		t.Fatal("expected a validation error for an unnamed server")
	}
}

func TestManagerFromConfig_ConnectsEnabledServers(t *testing.T) {
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{Name: "fake", Command: fakeServerBin, Args: []string{"-mode=normal", "-tools=0"}, Enabled: true},
			{Name: "off", Command: fakeServerBin, Args: []string{"-mode=normal"}, Enabled: false},
		},
	}
	m, err := ManagerFromConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ManagerFromConfig: %v", err)
	}
	if m == nil {
		t.Fatal("expected a non-nil manager")
	}
	t.Cleanup(func() { m.Close() })

	connected := m.ListConnectedServers()
	if len(connected) != 1 || connected[0] != "fake" {
		t.Fatalf("expected only the enabled server connected, got %v", connected)
	}
	if _, ok := m.GetClient("off"); ok {
		t.Fatal("expected the disabled server to never be launched")
	}
}

func TestManagerFromConfig_EnvExpansion(t *testing.T) {
	t.Setenv("BUCKLEY_MCP_TEST_CONFIG_VAR", "expanded-value")
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{
				Name:    "fake",
				Command: fakeServerBin,
				Args:    []string{"-mode=normal", "-tools=0"},
				Env:     map[string]string{"BUCKLEY_MCP_FORWARDED": "${BUCKLEY_MCP_TEST_CONFIG_VAR}"},
				Enabled: true,
			},
		},
	}
	m, err := ManagerFromConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ManagerFromConfig: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if len(m.ListConnectedServers()) != 1 {
		t.Fatal("expected the server to connect successfully with expanded env")
	}
}
