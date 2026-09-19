//go:build !linux

package workspaceguard

import "os"

func hasMultipleLinks(os.FileInfo) bool { return false }
