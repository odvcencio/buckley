//go:build linux

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func launchOperatorFileHasMultipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink > 1
}

func openLaunchOperatorSource(home string) (*os.File, *os.File, os.FileInfo, os.FileInfo, bool, error) {
	homeFD, err := syscall.Open(home, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	defer syscall.Close(homeFD)
	dirFD, err := syscall.Openat(homeFD, ".buckley", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil, nil, nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	dir := os.NewFile(uintptr(dirFD), "launch-operator-config-dir")
	if dir == nil {
		_ = syscall.Close(dirFD)
		return nil, nil, nil, nil, false, errors.New("operator config directory is unavailable")
	}
	fileFD, err := syscall.Openat(dirFD, "config.yaml", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		_ = dir.Close()
		return nil, nil, nil, nil, false, nil
	}
	if err != nil {
		_ = dir.Close()
		return nil, nil, nil, nil, false, err
	}
	file := os.NewFile(uintptr(fileFD), "launch-operator-config")
	if file == nil {
		_ = syscall.Close(fileFD)
		_ = dir.Close()
		return nil, nil, nil, nil, false, errors.New("operator config is unavailable")
	}
	dirInfo, dirErr := dir.Stat()
	fileInfo, fileErr := file.Stat()
	if dirErr != nil || fileErr != nil {
		_ = file.Close()
		_ = dir.Close()
		return nil, nil, nil, nil, false, errors.New("operator config source is unavailable")
	}
	return dir, file, dirInfo, fileInfo, true, nil
}

func validateLaunchArtifactDirectory(raw, workspace, operatorDir string, uid uint64) (string, error) {
	if raw == "" || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw || strings.ContainsRune(raw, 0) {
		return "", errors.New("artifact directory is not canonical")
	}
	if pathContains(workspace, raw) || pathContains(raw, workspace) || pathContains(operatorDir, raw) || pathContains(raw, operatorDir) {
		return "", errors.New("artifact directory overlaps an authority source")
	}
	parts := strings.Split(strings.TrimPrefix(raw, string(filepath.Separator)), string(filepath.Separator))
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = syscall.Close(fd) }()
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("artifact directory contains an invalid component")
		}
		next, openErr := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if openErr != nil {
			return "", openErr
		}
		if closeErr := syscall.Close(fd); closeErr != nil {
			_ = syscall.Close(next)
			return "", closeErr
		}
		fd = next
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return "", errors.New("artifact directory identity is unavailable")
	}
	if uint64(stat.Uid) != uid {
		return "", errors.New("artifact directory owner is unsafe")
	}
	if stat.Mode&0o077 != 0 {
		return "", errors.New("artifact directory mode is unsafe")
	}
	return raw, nil
}
