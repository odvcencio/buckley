package workspaceguard

import (
	"errors"
	"os"
)

var ErrRootBindingUnavailable = errors.New("workspace root binding unavailable")

// RootBinding keeps an exact directory identity alive and exposes a
// process-local proc descriptor used by rooted file access. Docker receives
// the canonical host path and a trusted in-container probe compares its mount
// identity with this retained no-follow anchor before launch publication.
type RootBinding struct {
	file     *os.File
	source   string
	info     os.FileInfo
	identity string
}

func (b *RootBinding) Source() string {
	if b == nil {
		return ""
	}
	return b.source
}

// Identity returns the stable device/inode tuple compared by the trusted
// launch-image probe. It contains no workspace path.
func (b *RootBinding) Identity() string {
	if b == nil || b.file == nil {
		return ""
	}
	return b.identity
}

func (b *RootBinding) Close() error {
	if b == nil || b.file == nil {
		return nil
	}
	err := b.file.Close()
	b.file = nil
	return err
}

func (b *RootBinding) matches(info os.FileInfo) bool {
	return b != nil && b.file != nil && b.info != nil && info != nil && os.SameFile(b.info, info)
}
