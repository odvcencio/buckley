//go:build linux

package tool

import (
	"os"
	"syscall"
)

func launchFileHasMultipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink > 1
}

func launchFileChangeIdentity(info os.FileInfo) (int64, int64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return stat.Ctim.Sec, stat.Ctim.Nsec, true
}
