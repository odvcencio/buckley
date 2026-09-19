//go:build linux

package workspaceguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// OpenRootBinding opens a directory without following the final path
// component and verifies the proc descriptor before returning it.
func OpenRootBinding(path string) (*RootBinding, error) {
	path = filepath.Clean(path)
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, ErrRootBindingUnavailable
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrRootBindingUnavailable, err)
	}
	file := os.NewFile(uintptr(fd), "workspace-root")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrRootBindingUnavailable
	}
	fail := func(err error) (*RootBinding, error) {
		_ = file.Close()
		return nil, errors.Join(ErrRootBindingUnavailable, err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return fail(errors.New("workspace root changed while binding"))
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(opened, after) {
		return fail(errors.New("workspace root changed while binding"))
	}
	source := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), fd)
	bound, err := os.Stat(source)
	if err != nil || !bound.IsDir() || !os.SameFile(opened, bound) {
		return fail(errors.New("workspace proc binding is unavailable"))
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok {
		return fail(errors.New("workspace root identity is unavailable"))
	}
	identity := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	return &RootBinding{file: file, source: source, info: opened, identity: identity}, nil
}
