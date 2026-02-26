package tui

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("FLUFFYUI_BACKEND", "sim")
	os.Exit(m.Run())
}
