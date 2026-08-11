//go:build !windows

package execmode

import (
	"os/exec"
	"syscall"
)

// configureProcess isolates a run in its own process group so cancelling a
// `go run` invocation also terminates the compiled child it starts.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
}
