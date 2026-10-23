package execmode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

var (
	detectOnce sync.Once
	detected   string
)

// DetectIsolation reports the strongest WORKING isolation mode. Presence
// of the bwrap binary is not enough: kernels and container hosts can
// restrict unprivileged user namespaces, so detection probes both the
// compiler sandbox and its nested runtime sandbox. The result is cached
// for the process.
func DetectIsolation() string {
	detectOnce.Do(func() {
		detected = IsolationNone
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			return
		}
		mounts := []string{"--unshare-all", "--die-with-parent",
			"--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev"}
		inner, err := sandboxBaseArgv()
		if err != nil {
			return
		}
		inner = append(inner, runtimeSandboxFlags()...)
		inner = append(inner, "--remount-ro", "/dev", "--remount-ro", "/proc", "--remount-ro", "/", "true")
		probe := exec.Command(bwrap, append(mounts, inner...)...)
		if err := probe.Run(); err == nil {
			detected = IsolationBwrap
		}
	})
	return detected
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

// sandboxBaseArgv supplies common system mounts; each stage adds its own storage.
func sandboxBaseArgv() ([]string, error) {
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
	return argv, nil
}

func runtimeSandboxFlags() []string {
	return []string{
		"--unshare-user", "--disable-userns",
		"--size", fmt.Sprint(DefaultRuntimeScratchBytes), "--perms", "0700", "--tmpfs", "/buckley-scratch",
		"--symlink", "/buckley-scratch", "/tmp",
		"--setenv", "HOME", "/buckley-scratch",
		"--setenv", "TMPDIR", "/buckley-scratch",
		"--setenv", "BUCKLEY_CAPS_SOCKET", "/buckley-scratch/caps.sock",
		"--chdir", "/buckley-scratch",
	}
}

func compilerSandboxFlags(scratch string, tempBytes int64) []string {
	return []string{
		"--size", fmt.Sprint(tempBytes), "--tmpfs", "/tmp",
		"--ro-bind", scratch, scratch,
		"--chdir", scratch,
	}
}

// sandboxArgv gives the Go compiler a writable cache, then uses go run's
// execution hook to launch the binary without that mount. Runtime code
// sees only its read-only executable, system paths and writable scratch.
func sandboxArgv(scratch, goCache string) ([]string, error) {
	argv, err := sandboxBaseArgv()
	if err != nil {
		return nil, err
	}
	goroot := resolveGOROOT()
	if goroot == "" {
		return nil, fmt.Errorf("execmode: cannot resolve the Go toolchain root")
	}
	argsPath := filepath.Join(scratch, ".buckley-runtime-args")
	runtimeArgs := append([]string{}, argv[1:]...)
	runtimeArgs = append(runtimeArgs, runtimeSandboxFlags()...)
	runtimeArgs = append(runtimeArgs,
		"--perms", "0644", "--file", "4", "/buckley-scratch/main.go",
		"--perms", "0644", "--file", "5", "/buckley-scratch/go.mod",
		"--dir", "/buckley-scratch/caps",
		"--perms", "0644", "--file", "6", "/buckley-scratch/caps/caps.go",
		"--ro-bind", filepath.Join(scratch, "caps.sock"), "/buckley-scratch/caps.sock",
	)
	// NUL-separated bwrap arguments preserve literal paths without shell parsing.
	if err := os.WriteFile(argsPath, []byte(strings.Join(runtimeArgs, "\x00")+"\x00"), 0600); err != nil {
		return nil, fmt.Errorf("execmode: runtime arguments: %w", err)
	}
	wrapperPath := filepath.Join(scratch, ".buckley-runtime")
	if err := os.WriteFile(wrapperPath, []byte(runtimeWrapper), 0700); err != nil {
		return nil, fmt.Errorf("execmode: runtime wrapper: %w", err)
	}
	argv = append(argv, compilerSandboxFlags(scratch, DefaultCompilerTempBytes)...)
	argv = append(argv,
		"--bind", goCache, goCache,
		"--ro-bind", argv[0], "/buckley-bwrap",
		"--ro-bind", argsPath, "/buckley-runtime-args",
		"--ro-bind", wrapperPath, "/buckley-runtime",
	)
	return append(argv, filepath.Join(goroot, "bin", "go"), "run", "-exec", "/buckley-runtime", "."), nil
}

const runtimeWrapper = `#!/bin/sh
buckley_program=$1
shift
exec /buckley-bwrap --args 3 --ro-bind "$buckley_program" /buckley-program --remount-ro /dev --remount-ro /proc --remount-ro / -- /buckley-program "$@" 3</buckley-runtime-args 4<"$HOME/main.go" 5<"$HOME/go.mod" 6<"$HOME/caps/caps.go"
`
