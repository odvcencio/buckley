package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/odvcencio/buckley/pkg/ralph"
)

// getRalphDataDir returns the base directory for Ralph data (~/.buckley/ralph/).
func getRalphDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return "", fmt.Errorf("could not determine home directory")
	}
	return filepath.Join(home, ".buckley", "ralph"), nil
}

// getProjectName returns a safe project name for organizing Ralph data.
// Uses the git repo name if in a git repo, otherwise the directory basename.
func getProjectName(workDir string) string {
	// Try to get git repo name
	if ralph.IsGitRepo(workDir) {
		if repoRoot, err := ralph.GetRepoRoot(workDir); err == nil {
			return filepath.Base(repoRoot)
		}
	}
	// Fall back to directory basename
	return filepath.Base(workDir)
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
