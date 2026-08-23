//go:build !linux

package config

import (
	"errors"
	"os"
)

func launchOperatorFileHasMultipleLinks(os.FileInfo) bool { return true }

func openLaunchOperatorSource(string) (*os.File, *os.File, os.FileInfo, os.FileInfo, bool, error) {
	return nil, nil, nil, nil, false, errors.New("operator config no-follow source is unavailable")
}

func validateLaunchArtifactDirectory(string, string, string, uint64) (string, error) {
	return "", errors.New("operator artifact source is unavailable")
}
