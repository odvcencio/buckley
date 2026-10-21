//go:build windows

package mcp

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func configureMCPProcess(_ *exec.Cmd) {}

func signalMCPProcess(cmd *exec.Cmd, force bool) error {
	if !force {
		return os.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func reapMCPDescendants(_ *exec.Cmd) {}
