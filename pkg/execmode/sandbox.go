package execmode

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Isolation modes. Bwrap is the enforced default: the program runs in a
// bubblewrap user-namespace sandbox with no network, a read-only system
// view, no workspace mount (the caps socket is its only window into the
// workspace), and writes confined to its scratch directory. None exists
// for library callers and portable tests only, and must be requested
// explicitly — the model-facing tool never runs unsandboxed.
const (
	IsolationBwrap = "bwrap"
	IsolationNone  = "none"
)

// DetectIsolation reports the strongest available isolation mode.
func DetectIsolation() string {
	if _, err := exec.LookPath("bwrap"); err == nil {
		return IsolationBwrap
	}
	return IsolationNone
}

var (
	gorootOnce sync.Once
	gorootPath string
)

// resolveGOROOT asks the go tool once; the sandbox binds it read-only so
// the toolchain and standard library sources are visible inside.
func resolveGOROOT() string {
	gorootOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOROOT").Output()
		if err == nil {
			gorootPath = strings.TrimSpace(string(out))
		}
	})
	return gorootPath
}

// sandboxArgv wraps the program invocation in bwrap. The mount plan is
// deny-by-default: read-only system directories for the dynamic loader
// and toolchain, the scratch module and shared build cache read-write,
// a private /tmp and /proc and /dev — and nothing else. --unshare-all
// removes network, pid, ipc, and user namespaces; the caps socket keeps
// working because unix sockets are files and the scratch bind carries it.
func sandboxArgv(scratch, goCache string) ([]string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("execmode: bwrap not found: %w", err)
	}
	argv := []string{
		bwrap,
		"--unshare-all",
		"--die-with-parent",
		"--new-session",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/alternatives"} {
		if _, err := os.Stat(dir); err == nil {
			argv = append(argv, "--ro-bind", dir, dir)
		}
	}
	if goroot := resolveGOROOT(); goroot != "" && !strings.HasPrefix(goroot, "/usr/") {
		if _, err := os.Stat(goroot); err == nil {
			argv = append(argv, "--ro-bind", goroot, goroot)
		}
	}
	argv = append(argv,
		"--bind", scratch, scratch,
		"--bind", goCache, goCache,
		"--chdir", scratch,
	)
	return append(argv, "go", "run", "."), nil
}
