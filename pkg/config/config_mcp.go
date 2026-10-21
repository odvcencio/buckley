package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// DefaultMCPMaxMessageBytes bounds incoming JSON before MCP decoding.
	DefaultMCPMaxMessageBytes = 8 << 20
	// MaxMCPMessageBytes is the largest supported per-server message budget.
	MaxMCPMessageBytes = 64 << 20
	// MaxMCPTools bounds the configured per-server catalog size.
	MaxMCPTools = 1024
)

// ResolveMCPToolLimit applies the existing default and enforces a hard ceiling.
func ResolveMCPToolLimit(limit int) (int, error) {
	if limit < 0 || limit > MaxMCPTools {
		return 0, fmt.Errorf("mcp.max_tools must be between 0 and %d", MaxMCPTools)
	}
	if limit == 0 {
		return DefaultMCPMaxTools, nil
	}
	return limit, nil
}

// ResolveMCPMessageLimit applies the default while rejecting unbounded settings.
func ResolveMCPMessageLimit(bytes int) (int, error) {
	if bytes < 0 || bytes > MaxMCPMessageBytes {
		return 0, fmt.Errorf("max_message_bytes must be between 0 and %d", MaxMCPMessageBytes)
	}
	if bytes == 0 {
		return DefaultMCPMaxMessageBytes, nil
	}
	return bytes, nil
}

// Validate checks the mcp section: server names are non-empty and unique,
// enabled servers carry a resolvable command, and numeric settings are
// non-negative. Disabled servers are not launched, so their command is not
// required to resolve (it may reference a tool not yet installed).
func (c MCPConfig) Validate() error {
	if _, err := ResolveMCPToolLimit(c.MaxTools); err != nil {
		return err
	}
	seen := make(map[string]bool, len(c.Servers))
	for i, srv := range c.Servers {
		name := strings.TrimSpace(srv.Name)
		if name == "" {
			return fmt.Errorf("mcp.servers[%d].name is required", i)
		}
		if seen[name] {
			return fmt.Errorf("mcp.servers: duplicate server name %q", name)
		}
		seen[name] = true
		if srv.Timeout < 0 {
			return fmt.Errorf("mcp.servers[%s].timeout must be >= 0", name)
		}
		if _, err := ResolveMCPMessageLimit(srv.MaxMessageBytes); err != nil {
			return fmt.Errorf("mcp.servers[%s].%w", name, err)
		}
		if !srv.Enabled {
			continue
		}
		if err := ValidateMCPCommand(srv.Command); err != nil {
			return fmt.Errorf("mcp.servers[%s].command: %w", name, err)
		}
	}
	return nil
}

// ValidateMCPCommand reports an error unless command is an absolute path or
// resolvable via the current PATH.
func ValidateMCPCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("command is required")
	}
	if filepath.IsAbs(command) {
		return nil
	}
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("command %q is not absolute and not found on PATH: %w", command, err)
	}
	return nil
}

// ExpandMCPEnv expands ${VAR} (and $VAR) references in server env values
// against the ambient process environment. References to unset variables
// expand to an empty string, matching os.Expand semantics.
func ExpandMCPEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return env
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = os.Expand(v, os.Getenv)
	}
	return out
}
