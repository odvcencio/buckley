//go:build !windows

package sandbox

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommandContext returns the shell command for Unix systems with context.
func shellCommandContext(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// setSysProcAttr sets Unix-specific process attributes.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}
