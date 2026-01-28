//go:build windows

package ralph

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {
	// Not supported on Windows; no-op.
}

func forceKill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
