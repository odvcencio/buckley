package execmode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunner_ScratchOwnsTemporaryFiles(t *testing.T) {
	for _, exitCode := range []int{0, 2} {
		t.Run(fmt.Sprintf("exit_%d", exitCode), func(t *testing.T) {
			tempRoot := t.TempDir()
			t.Setenv("TMPDIR", tempRoot)
			t.Setenv("TMP", tempRoot)
			t.Setenv("TEMP", tempRoot)
			runner, err := NewRunner(t.TempDir(), &recordingSink{}, DefaultTimeout, WithoutIsolation())
			if err != nil {
				t.Fatal(err)
			}
			program := fmt.Sprintf(`package main
import ("fmt"; "os"; "path/filepath")
func main() {
	home := os.Getenv("HOME")
	for _, name := range []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"} {
		if os.Getenv(name) != filepath.Join(home, "tmp") { panic(name + " escaped scratch") }
	}
	file, err := os.CreateTemp("", "program-*")
	if err != nil { panic(err) }
	fmt.Println(file.Name())
	file.Close()
	os.Exit(%d)
}`, exitCode)
			result, err := runner.Run(context.Background(), program)
			if err != nil || (result.ExitCode == 0) != (exitCode == 0) {
				t.Fatalf("Run = %+v, %v", result, err)
			}
			created := strings.TrimSpace(result.Stdout)
			scratch := filepath.Dir(filepath.Dir(created))
			if !strings.HasPrefix(filepath.Base(scratch), "buckley-exec-") {
				t.Fatalf("program temporary file escaped scratch: %q", created)
			}
			if _, err := os.Stat(scratch); !os.IsNotExist(err) {
				t.Fatalf("scratch survived completed run: %v", err)
			}
			entries, err := os.ReadDir(tempRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("compiler temporary files survived: %v, %v", entries, err)
			}
		})
	}
}
