//go:build windows

package gts

import (
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func isOOMKill(_ *exec.ExitError) bool {
	return false
}
