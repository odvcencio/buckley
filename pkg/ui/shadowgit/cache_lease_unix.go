//go:build !windows

package shadowgit

import (
	"os"
	"os/exec"
)

func openCacheLease(name string, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	return os.OpenFile(name, flags, 0600)
}

func cacheLeaseBusy(error) bool { return false }

func inheritCacheLease(cmd *exec.Cmd, file *os.File) {
	// flock remains held by this shared open-file description if the UI dies
	// while Git or an inherited clean filter is still using the cache.
	cmd.ExtraFiles = append(cmd.ExtraFiles, file)
}
