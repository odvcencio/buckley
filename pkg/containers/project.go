package containers

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// ProjectName identifies a worktree's Compose resources independently of its basename.
func ProjectName(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve compose project directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve compose project directory: %w", err)
	}
	label := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.ToLower(filepath.Base(canonical)))
	if len(label) > 32 {
		label = label[:32]
	}
	label = strings.Trim(label, "-_")
	if label == "" {
		label = "workspace"
	}
	digest := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("buckley-%s-%x", label, digest[:8]), nil
}
