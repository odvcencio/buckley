//go:build windows

package graft

import (
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func isKillSignal(_ *exec.ExitError) bool {
	return false
}
