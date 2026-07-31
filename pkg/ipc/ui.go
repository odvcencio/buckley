//go:build webui

// Package ipc's embedded web UI. Built only with -tags webui; see
// ui_stub.go for the default build, which omits the ~1.1MB embed.
package ipc

import (
	"embed"
	"io/fs"
)

//go:embed ui
var embeddedUI embed.FS

// GetEmbeddedUI returns the embedded UI filesystem
func GetEmbeddedUI() (fs.FS, error) {
	return fs.Sub(embeddedUI, "ui")
}
