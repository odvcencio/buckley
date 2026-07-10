package model

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ReviewSandboxCommand resolves the exact Codex executable configured for the
// model manager. API-backed review verification reuses this binary only as the
// OS sandbox launcher; it must not rediscover a different executable through
// an ambient PATH inside the review tool.
func (m *Manager) ReviewSandboxCommand() string {
	if m == nil {
		return ""
	}

	command := ""
	if provider, ok := m.providers[codexProviderID].(*CodexCLIProvider); ok && provider != nil {
		command = strings.TrimSpace(provider.command)
	}
	if command == "" && m.config != nil {
		command = strings.TrimSpace(m.config.Providers.Codex.Command)
	}
	if command == "" {
		command = defaultCodexCommand
	}

	resolved, err := exec.LookPath(command)
	if err != nil {
		return ""
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return ""
	}
	if canonical, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = canonical
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return ""
	}
	return filepath.Clean(resolved)
}
