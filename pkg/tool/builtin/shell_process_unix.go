//go:build !windows

package builtin

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Buckley handles SIGTERM by cancelling its tools and closing its registry.
// WaitDelay bounds that grace period; the final group kill also removes any
// descendants that ignored SIGTERM or held output pipes after the child exit.
func configureSubagentCancellation(command *exec.Cmd) func() {
	configureCommandCancellation(command)
	force := command.Cancel
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return func() { _ = force() }
}

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
