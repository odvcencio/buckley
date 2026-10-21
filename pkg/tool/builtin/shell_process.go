package builtin

import (
	"os/exec"

	"m31labs.dev/buckley/internal/commandrun"
)

func configureCommandCancellation(command *exec.Cmd) {
	commandrun.ConfigureCancellation(command)
}
