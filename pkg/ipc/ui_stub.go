//go:build !webui

// Package ipc's no-op web UI stub. This is the default build: it keeps the
// ~1.1MB embedded UI assets out of the binary. Build with -tags webui (see
// `make build-webui`) to embed the real UI from ui.go.
package ipc

import (
	"fmt"
	"io/fs"
)

// GetEmbeddedUI always fails in builds without the webui tag. Callers (see
// Server.mountBrowserUI) fall back to a placeholder page on error.
func GetEmbeddedUI() (fs.FS, error) {
	return nil, fmt.Errorf("web UI not built into this binary; rebuild with -tags webui (see `make build-webui`)")
}
