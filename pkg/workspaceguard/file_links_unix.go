//go:build linux

package workspaceguard

import (
	"os"
	"syscall"
)

func hasMultipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}
