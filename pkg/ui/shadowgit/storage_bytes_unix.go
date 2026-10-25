//go:build unix

package shadowgit

import (
	"io/fs"
	"syscall"
)

func allocatedBytes(info fs.FileInfo) int64 {
	return info.Sys().(*syscall.Stat_t).Blocks * 512
}
