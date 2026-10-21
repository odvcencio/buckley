//go:build windows

package builtin

import (
	"os/exec"
)

func configureSubagentCancellation(command *exec.Cmd) func() {
	configureCommandCancellation(command)
	return func() {}
}
