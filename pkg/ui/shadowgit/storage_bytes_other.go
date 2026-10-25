//go:build !unix

package shadowgit

import "io/fs"

func allocatedBytes(info fs.FileInfo) int64 {
	const unit = 4096
	if info.IsDir() || info.Size() == 0 {
		return unit
	}
	return (info.Size() + unit - 1) / unit * unit
}
