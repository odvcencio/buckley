package model

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ReviewSandboxCommand resolves an explicitly configured absolute Codex
// executable for reuse as the API review sandbox launcher. Bare commands are
// deliberately left unresolved so the sandbox package performs its fixed,
// ambient-PATH-independent trusted lookup instead.
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
	if command == "" || !filepath.IsAbs(command) {
		return ""
	}

	resolved := filepath.Clean(command)
	if canonical, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = canonical
	} else {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return ""
	}
	return filepath.Clean(resolved)
}
