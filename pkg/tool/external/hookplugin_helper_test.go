package external

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// hookPluginBin holds the path to the compiled testdata/hookplugin
// binary, built once by TestMain and reused across every test in this
// package (see pkg/mcp/fakeserver_helper_test.go for the same pattern).
var hookPluginBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "buckley-external-hookplugin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "external test setup: mkdtemp:", err)
		os.Exit(1)
	}

	bin := filepath.Join(dir, "hookplugin")
	build := exec.Command("go", "build", "-o", bin, "./testdata/hookplugin")
	build.Dir = mustGetwd()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "external test setup: building hookplugin: %v\n%s\n", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	hookPluginBin = bin

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return wd
}
