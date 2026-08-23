//go:build linux

package launchimage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openRootBinding(path string) (*rootBinding, error) {
	path = filepath.Clean(path)
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, errors.New("artifact root binding is unavailable")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "launch-artifact-root")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("artifact root binding is unavailable")
	}
	fail := func(cause error) (*rootBinding, error) {
		_ = file.Close()
		return nil, cause
	}
	opened, err := file.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return fail(errors.New("artifact root changed while binding"))
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(opened, after) {
		return fail(errors.New("artifact root changed while binding"))
	}
	source := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), fd)
	bound, err := os.Stat(source)
	if err != nil || !bound.IsDir() || !os.SameFile(opened, bound) {
		return fail(errors.New("artifact proc binding is unavailable"))
	}
	return &rootBinding{file: file, source: source, info: opened}, nil
}

func validateLaunchArtifactPath(path string, uid uint32) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return errors.New("artifact path is not canonical")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("artifact path has an invalid component")
		}
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		if err := syscall.Close(fd); err != nil {
			_ = syscall.Close(next)
			return err
		}
		fd = next
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Uid != uid || stat.Mode&0o077 != 0 {
		return errors.New("artifact directory ownership or mode is unsafe")
	}
	return nil
}
