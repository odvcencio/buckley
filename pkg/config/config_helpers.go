package config

import (
	"fmt"
	"net"
	"strings"

	"github.com/odvcencio/buckley/pkg/sandbox"
)

func isLoopbackBindAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	switch strings.ToLower(host) {
	case "localhost":
		return true
	case "0.0.0.0", "::":
		return false
	default:
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		return ip.IsLoopback()
	}
}

// ExecutionMode returns the normalized execution mode.
func (c *Config) ExecutionMode() string {
	if c == nil {
		return DefaultExecutionMode
	}
	return normalizeMode(c.Execution.Mode, DefaultExecutionMode)
}

// OneshotMode returns the normalized oneshot mode.
func (c *Config) OneshotMode() string {
	if c == nil {
		return DefaultOneshotMode
	}
	return normalizeMode(c.Oneshot.Mode, DefaultOneshotMode)
}

func normalizeMode(mode, fallback string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return fallback
	}
	return mode
}

func parseSandboxMode(mode string) (sandbox.Mode, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "workspace":
		return sandbox.ModeWorkspace, nil
	case "readonly", "read-only", "read_only":
		return sandbox.ModeReadOnly, nil
	case "strict":
		return sandbox.ModeStrict, nil
	case "disabled", "disable", "off", "none":
		return sandbox.ModeDisabled, nil
	default:
		return sandbox.ModeWorkspace, fmt.Errorf("invalid sandbox mode: %s (valid: disabled, readonly, workspace, strict)", mode)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}
