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
