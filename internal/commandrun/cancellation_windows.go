package commandrun

import (
	"os/exec"
	"strconv"
	"time"
)

// ConfigureCancellation isolates the command so cancellation also kills descendants.
func ConfigureCancellation(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		killTree := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(command.Process.Pid))
		if err := killTree.Run(); err == nil {
			return nil
		}
		return command.Process.Kill()
	}
	command.WaitDelay = 2 * time.Second
}
