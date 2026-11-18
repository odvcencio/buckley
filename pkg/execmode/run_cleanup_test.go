package execmode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunner_ScratchOwnsTemporaryFiles(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		exitCode int
		isolated bool
	}{
		{name: "exit_0"},
		{name: "exit_2", exitCode: 2},
		{name: "bubblewrap", isolated: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			opts := []RunnerOption{WithoutIsolation()}
			if scenario.isolated {
				if DetectIsolation() != IsolationBwrap {
					t.Skip("bubblewrap isolation unavailable")
				}
				opts = nil
			}
			tempRoot := t.TempDir()
			if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
				tempRoot = filepath.Join(tempRoot, strings.Repeat("deep", 30))
				if err := os.Mkdir(tempRoot, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMPDIR", tempRoot)
			t.Setenv("TMP", tempRoot)
			t.Setenv("TEMP", tempRoot)
			runner, err := NewRunner(newWorkspace(t), &recordingSink{}, DefaultTimeout, opts...)
			if err != nil {
				t.Fatal(err)
			}
			program := fmt.Sprintf(`package main
import ("execprogram/caps"; "fmt"; "os"; "path/filepath")
func main() {
	home := os.Getenv("HOME")
	for _, name := range []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"} {
		if os.Getenv(name) != filepath.Join(home, "tmp") { panic(name + " escaped scratch") }
	}
	file, err := os.CreateTemp("", "program-*")
	if err != nil { panic(err) }
	content, truncated, err := caps.ReadFile("greeting.txt")
	if err != nil || truncated || content != "hello from the workspace\n" { panic(fmt.Sprint(content, truncated, err)) }
	fmt.Println(file.Name())
	fmt.Println(os.Getenv("BUCKLEY_CAPS_SOCKET"))
	file.Close()
	os.Exit(%d)
}`, scenario.exitCode)
			result, err := runner.Run(context.Background(), program)
			if err != nil || (result.ExitCode == 0) != (scenario.exitCode == 0) {
				t.Fatalf("Run = %+v, %v", result, err)
			}
			created, socket, ok := strings.Cut(strings.TrimSpace(result.Stdout), "\n")
			if !ok {
				t.Fatalf("program omitted scratch or socket path: %q", result.Stdout)
			}
			scratch := filepath.Dir(filepath.Dir(created))
			if !strings.HasPrefix(filepath.Base(scratch), "buckley-exec-") {
				t.Fatalf("program temporary file escaped scratch: %q", created)
			}
			if _, err := os.Stat(scratch); !os.IsNotExist(err) {
				t.Fatalf("scratch survived completed run: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
				t.Fatalf("capability socket directory survived completed run: %v", err)
			}
			entries, err := os.ReadDir(tempRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("compiler temporary files survived: %v, %v", entries, err)
			}
		})
	}
}
