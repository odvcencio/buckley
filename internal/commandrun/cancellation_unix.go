//go:build !windows

package commandrun

import (
	"os/exec"
	"syscall"
	"time"
)

// ConfigureCancellation isolates the command so cancellation also kills descendants.
func ConfigureCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
}
