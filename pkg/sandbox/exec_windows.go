//go:build windows

package sandbox

import (
	"context"
	"os/exec"
)

// shellCommandContext returns the shell command for Windows systems with context.
func shellCommandContext(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/c", command)
}

// setSysProcAttr sets Windows-specific process attributes.
// On Windows, Setpgid is not available, so this is a no-op.
func setSysProcAttr(cmd *exec.Cmd) {
	// No-op on Windows - Setpgid is not available
}
