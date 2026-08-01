package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeServerBin holds the path to the compiled testdata/fakeserver binary,
// built once by TestMain and reused across every test in this package.
var fakeServerBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "buckley-mcp-fakeserver-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp test setup: mkdtemp:", err)
		os.Exit(1)
	}

	bin := filepath.Join(dir, "fakeserver")
	build := exec.Command("go", "build", "-o", bin, "./testdata/fakeserver")
	build.Dir = mustGetwd()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "mcp test setup: building fakeserver: %v\n%s\n", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	fakeServerBin = bin

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
