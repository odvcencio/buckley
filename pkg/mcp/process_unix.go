//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

func configureMCPProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalMCPProcess(cmd *exec.Cmd, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func reapMCPDescendants(cmd *exec.Cmd) { _ = signalMCPProcess(cmd, true) }
