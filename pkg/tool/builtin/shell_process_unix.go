//go:build !windows

package builtin

import (
	"os/exec"
	"syscall"
	"time"
)

// configureCommandCancellation gives each shell invocation its own process
// group so cancelling the tool cannot leave descendants holding output pipes.
func configureCommandCancellation(command *exec.Cmd) {
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
