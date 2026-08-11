//go:build windows

package execmode

import "os/exec"

// configureProcess keeps the portable os/exec cancellation behavior on
// Windows, where Unix process groups and signals are unavailable.
func configureProcess(_ *exec.Cmd) {}
