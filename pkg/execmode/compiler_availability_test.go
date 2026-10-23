package execmode

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompilerAvailable_UnusableToolchain(t *testing.T) {
	if os.Getenv("BUCKLEY_TEST_UNUSABLE_COMPILER") == "1" {
		if CompilerAvailable() {
			t.Fatal("advertised an unusable compiler")
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX Go probe wrapper")
	}
	for _, scenario := range []struct {
		name  string
		probe string
	}{
		{"failed probe", "#!/bin/sh\nexit 1\n"},
		{"missing executable", "#!/bin/sh\nprintf '%s\\n' \"$BUCKLEY_TEST_EMPTY_GOROOT\"\n"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(scenario.probe), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCompilerAvailable_UnusableToolchain$")
			cmd.Env = append(os.Environ(), "BUCKLEY_TEST_UNUSABLE_COMPILER=1", "BUCKLEY_TEST_EMPTY_GOROOT="+t.TempDir(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("compiler probe: %v\n%s", err, out)
			}
		})
	}
}
