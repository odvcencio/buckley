//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package model

import "os"

// Unsupported platforms skip optional logging rather than append without coordination.
func tryLockNetworkLog(*os.File) (bool, error) { return false, nil }
func unlockNetworkLog(*os.File)                {}
