//go:build !windows

package execmode

import "syscall"

// A symlink or replaced file may be a FIFO; opening it must not stall the broker.
const brokerReadFlags = syscall.O_RDONLY | syscall.O_NONBLOCK
