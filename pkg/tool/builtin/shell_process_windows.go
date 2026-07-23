//go:build windows

package builtin

import (
	"os/exec"
	"strconv"
	"time"
)

func configureCommandCancellation(command *exec.Cmd) {
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
