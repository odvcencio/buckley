//go:build !windows

package gts

import (
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func isOOMKill(exitErr *exec.ExitError) bool {
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && (status.Signal() == syscall.SIGKILL || status.ExitStatus() == 137)
}
