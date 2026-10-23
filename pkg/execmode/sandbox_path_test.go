package execmode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunner_SandboxUsesMountedGoThroughHiddenPathAlias(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("native sandbox unavailable")
	}
	aliases := t.TempDir()
	for _, name := range []string{"go", "bwrap"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(aliases, name)); err != nil {
			t.Fatal(err)
		}
	}
	// Host launchers remain usable, while their PATH directory is absent from
	// the compiler and runtime sandboxes. The mounted toolchain must suffice.
	t.Setenv("PATH", aliases)
	sink := &recordingSink{}
	runner, err := NewRunner(newWorkspace(t), sink, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), `package main
import("fmt";"execprogram/caps")
func main(){s,_,err:=caps.ReadFile("greeting.txt");if err!=nil{panic(err)};fmt.Print(s)}`)
	if err != nil || result.ExitCode != 0 || result.Stdout != "hello from the workspace\n" {
		t.Fatalf("sandboxed alias execution: %+v, %v", result, err)
	}
	if records := sink.byMethod(CapFilesRead); len(records) != 1 || records[0].Outcome != "ok" {
		t.Fatalf("snapshot read was not audited: %+v", records)
	}
}
