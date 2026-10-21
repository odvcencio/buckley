//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package filelock

import "os"

// Unsupported platforms skip optional writes rather than write without coordination.
func TryLock(*os.File) (bool, error) { return false, nil }
func Unlock(*os.File)                {}
