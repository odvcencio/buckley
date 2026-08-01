package config

import (
	"os/exec"
	"testing"
)

func TestMCPConfig_Validate_EmptyName(t *testing.T) {
	cfg := MCPConfig{Servers: []MCPServerConfig{{Name: ""}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty server name")
	}
}

func TestMCPConfig_Validate_DuplicateName(t *testing.T) {
	cfg := MCPConfig{Servers: []MCPServerConfig{
		{Name: "fs"},
		{Name: "fs"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate server name")
	}
}

func TestMCPConfig_Validate_NegativeMaxTools(t *testing.T) {
	cfg := MCPConfig{MaxTools: -1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative max_tools")
	}
}

func TestMCPConfig_Validate_NegativeTimeout(t *testing.T) {
	cfg := MCPConfig{Servers: []MCPServerConfig{{Name: "fs", Timeout: -1}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative timeout")
	}
}

func TestMCPConfig_Validate_DisabledServerSkipsCommandCheck(t *testing.T) {
	cfg := MCPConfig{Servers: []MCPServerConfig{{Name: "fs", Enabled: false, Command: ""}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled server should not require a command, got: %v", err)
	}
}

func TestMCPConfig_Validate_EnabledServerRequiresResolvableCommand(t *testing.T) {
	cfg := MCPConfig{Servers: []MCPServerConfig{{
		Name:    "fs",
		Enabled: true,
		Command: "definitely-not-a-real-binary-xyz",
	}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unresolvable relative command")
	}
}

func TestMCPConfig_Validate_EnabledServerAbsolutePathOK(t *testing.T) {
	echo, err := exec.LookPath("echo")
	if err != nil {
		t.Skip("echo not found on PATH")
	}
	cfg := MCPConfig{Servers: []MCPServerConfig{{
		Name:    "fs",
		Enabled: true,
		Command: echo,
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected absolute, existing command to validate, got: %v", err)
	}
}

func TestMCPConfig_Validate_EnabledServerPATHResolvableOK(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not found on PATH")
	}
	cfg := MCPConfig{Servers: []MCPServerConfig{{
		Name:    "fs",
		Enabled: true,
		Command: "echo",
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected PATH-resolvable command to validate, got: %v", err)
	}
}

func TestValidateMCPCommand_Empty(t *testing.T) {
	if err := ValidateMCPCommand(""); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestMaxToolsOrDefault(t *testing.T) {
	if got := (MCPConfig{}).MaxToolsOrDefault(); got != DefaultMCPMaxTools {
		t.Fatalf("expected default %d, got %d", DefaultMCPMaxTools, got)
	}
	if got := (MCPConfig{MaxTools: 5}).MaxToolsOrDefault(); got != 5 {
		t.Fatalf("expected explicit 5, got %d", got)
	}
}

func TestExpandMCPEnv(t *testing.T) {
	t.Setenv("BUCKLEY_MCP_TEST_TOKEN", "secret-value")

	env := map[string]string{
		"TOKEN":   "${BUCKLEY_MCP_TEST_TOKEN}",
		"LITERAL": "unchanged",
		"UNSET":   "${BUCKLEY_MCP_TEST_UNSET_VAR}",
	}
	got := ExpandMCPEnv(env)

	if got["TOKEN"] != "secret-value" {
		t.Fatalf("expected TOKEN to expand, got %q", got["TOKEN"])
	}
	if got["LITERAL"] != "unchanged" {
		t.Fatalf("expected LITERAL to stay unchanged, got %q", got["LITERAL"])
	}
	if got["UNSET"] != "" {
		t.Fatalf("expected unset var to expand to empty string, got %q", got["UNSET"])
	}
}

func TestExpandMCPEnv_Empty(t *testing.T) {
	if got := ExpandMCPEnv(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestMergeMCPConfig_ServersByName(t *testing.T) {
	base := DefaultConfig()
	base.MCP.Servers = []MCPServerConfig{
		{Name: "fs", Command: "mcp-fs", Enabled: true},
		{Name: "git", Command: "mcp-git", Enabled: true},
	}

	override := &Config{MCP: MCPConfig{Servers: []MCPServerConfig{
		{Name: "fs", Command: "mcp-fs-v2", Enabled: false},
		{Name: "search", Command: "mcp-search", Enabled: true},
	}}}
	raw := map[string]any{
		"mcp": map[string]any{
			"servers": []any{},
		},
	}

	mergeMCPConfig(base, override, raw)

	if len(base.MCP.Servers) != 3 {
		t.Fatalf("expected 3 merged servers, got %d: %+v", len(base.MCP.Servers), base.MCP.Servers)
	}
	byName := make(map[string]MCPServerConfig, len(base.MCP.Servers))
	for _, s := range base.MCP.Servers {
		byName[s.Name] = s
	}
	if byName["fs"].Command != "mcp-fs-v2" || byName["fs"].Enabled {
		t.Fatalf("expected fs server to be overridden, got %+v", byName["fs"])
	}
	if byName["git"].Command != "mcp-git" {
		t.Fatalf("expected git server to survive unmodified, got %+v", byName["git"])
	}
	if byName["search"].Command != "mcp-search" {
		t.Fatalf("expected search server to be added, got %+v", byName["search"])
	}
}

func TestMergeMCPConfig_EnabledAndMaxToolsOverride(t *testing.T) {
	base := DefaultConfig()
	if base.MCP.Enabled {
		t.Fatal("expected MCP disabled by default")
	}

	override := &Config{MCP: MCPConfig{Enabled: true, MaxTools: 5}}
	raw := map[string]any{
		"mcp": map[string]any{
			"enabled":   true,
			"max_tools": 5,
		},
	}

	mergeMCPConfig(base, override, raw)

	if !base.MCP.Enabled {
		t.Fatal("expected mcp.enabled to be overridden to true")
	}
	if base.MCP.MaxTools != 5 {
		t.Fatalf("expected mcp.max_tools to be overridden to 5, got %d", base.MCP.MaxTools)
	}
}

func TestMergeMCPConfig_PreservesDefaultsWhenNotOverridden(t *testing.T) {
	base := DefaultConfig()
	override := &Config{}
	raw := map[string]any{}

	mergeMCPConfig(base, override, raw)

	if base.MCP.Enabled {
		t.Fatal("expected mcp.enabled to remain false when not present in raw config")
	}
	if base.MCP.MaxTools != DefaultMCPMaxTools {
		t.Fatalf("expected mcp.max_tools to remain default, got %d", base.MCP.MaxTools)
	}
}

func TestDefaultConfig_MCP(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MCP.Enabled {
		t.Fatal("expected MCP disabled by default (conservative posture toward untrusted servers)")
	}
	if cfg.MCP.MaxTools != DefaultMCPMaxTools {
		t.Fatalf("expected default max_tools %d, got %d", DefaultMCPMaxTools, cfg.MCP.MaxTools)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate cleanly, got: %v", err)
	}
}
