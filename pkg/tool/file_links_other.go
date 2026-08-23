//go:build !linux

package tool

import "os"

func launchFileHasMultipleLinks(os.FileInfo) bool { return false }

func launchFileChangeIdentity(os.FileInfo) (int64, int64, bool) { return 0, 0, false }
