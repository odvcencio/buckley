package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveProjectRoot returns the absolute project root Buckley should operate in.
// Preference order:
//  1. Explicit worktree root configured via worktrees.root_path
//  2. Current working directory if no override is provided
func ResolveProjectRoot(cfg *Config) string {
	if cfg != nil {
		root := strings.TrimSpace(cfg.Worktrees.RootPath)
		root = expandHomeDir(root)
		if root != "" {
			if abs, err := filepath.Abs(root); err == nil {
				return abs
			}
			return root
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func expandHomeDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
