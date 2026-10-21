//go:build !windows

package builtin

import (
	"os"
	"os/exec"
	"syscall"
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
