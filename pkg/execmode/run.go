package execmode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultTimeout bounds one program run.
	DefaultTimeout = 120 * time.Second
	// maxOutputBytes bounds captured stdout/stderr each.
	maxOutputBytes = 128 * 1024
	// maxSourceBytes bounds the program source.
	maxSourceBytes = 256 * 1024
)

// Result is one program run's outcome.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// Runner executes model-written Go programs against a jailed broker. The
// zero value is unusable; construct with NewRunner.
type Runner struct {
	workspaceRoot    string
	audit            AuditSink
	timeout          time.Duration
	isolation        string
	allowUnsandboxed bool
}

// RunnerOption configures NewRunner.
type RunnerOption func(*Runner)

// WithoutIsolation opts a Runner out of OS sandboxing. Library and test
// use only: the program then runs with shell-equivalent risk, and the
// model-facing tool never uses this option.
func WithoutIsolation() RunnerOption {
	return func(r *Runner) {
		r.isolation = IsolationNone
		r.allowUnsandboxed = true
	}
}

// NewRunner wires a Runner. The audit sink is required (see NewBroker).
// Isolation defaults to the strongest available mode and Run refuses to
// execute when that is not bwrap — an unsandboxed run must be an
// explicit caller decision, never a silent fallback.
func NewRunner(workspaceRoot string, audit AuditSink, timeout time.Duration, opts ...RunnerOption) (*Runner, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if audit == nil {
		return nil, fmt.Errorf("execmode: an audit sink is required")
	}
	runner := &Runner{
		workspaceRoot: workspaceRoot,
		audit:         audit,
		timeout:       timeout,
		isolation:     DetectIsolation(),
	}
	for _, opt := range opts {
		opt(runner)
	}
	return runner, nil
}

// Isolation reports the mode this Runner executes under.
func (r *Runner) Isolation() string { return r.isolation }

// Run scaffolds a scratch module around source, starts a per-run broker,
// and executes the program with a scrubbed environment: the process sees
// only the toolchain basics plus the capability socket and token — no
// inherited secrets, no Buckley state. Source must be a complete
// `package main` program; it may import "execprogram/caps" for the typed
// capability client the scaffold provides.
//
// Isolation boundary: the scrub removes environment secrets and GOPROXY
// is off, but the process is NOT OS-sandboxed — it runs as the local
// user with filesystem and network access. Callers exposing Run to a
// model must say so in their tool contract and treat the surface like
// shell execution until OS-level isolation lands.
func (r *Runner) Run(ctx context.Context, source string) (Result, error) {
	if len(source) > maxSourceBytes {
		return Result{}, fmt.Errorf("execmode: source exceeds %d bytes", maxSourceBytes)
	}
	if !strings.Contains(source, "package main") {
		return Result{}, fmt.Errorf("execmode: source must be a complete package main program")
	}

	scratch, err := os.MkdirTemp("", "buckley-exec-*")
	if err != nil {
		return Result{}, fmt.Errorf("execmode: scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	if err := scaffold(scratch, source); err != nil {
		return Result{}, err
	}

	broker, err := NewBroker(r.workspaceRoot, r.audit)
	if err != nil {
		return Result{}, err
	}
	if err := broker.Start(filepath.Join(scratch, "caps.sock")); err != nil {
		return Result{}, err
	}
	defer broker.Close()

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	goCache := sharedGoCache()
	var cmd *exec.Cmd
	switch {
	case r.isolation == IsolationBwrap:
		argv, err := sandboxArgv(scratch, goCache)
		if err != nil {
			return Result{}, err
		}
		cmd = exec.CommandContext(runCtx, argv[0], argv[1:]...)
	case r.allowUnsandboxed:
		cmd = exec.CommandContext(runCtx, "go", "run", ".")
	default:
		return Result{}, fmt.Errorf("execmode: OS isolation unavailable (install bubblewrap); refusing to run unsandboxed without an explicit WithoutIsolation opt-in")
	}
	cmd.Dir = scratch
	// Scrubbed environment: nothing from Buckley's process leaks in.
	// GOCACHE is a shared, content-addressed warm cache — a cold cache
	// would recompile the stdlib dependency chain on every run.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + scratch,
		"GOCACHE=" + goCache,
		"GOFLAGS=-mod=mod",
		"GOPROXY=off",
		"BUCKLEY_CAPS_SOCKET=" + broker.SocketPath(),
		"BUCKLEY_CAPS_TOKEN=" + broker.Token(),
	}
	// `go run` does not forward a SIGKILL to the compiled child, so the
	// program runs in its own process group and cancellation kills the
	// whole group; WaitDelay backstops pipe readers held by orphans.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout}
	cmd.Stderr = &limitedWriter{buf: &stderr}

	started := time.Now()
	err = cmd.Run()
	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(started),
	}
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			return result, fmt.Errorf("execmode: program exceeded the %s timeout", r.timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("execmode: run: %w", err)
	}
	return result, nil
}

// RunFW transpiles a Ferrous Wheel (.fw) program to Go with the host
// ferrous-wheel toolchain, then runs it exactly like Run. The transpiler
// is a trusted host binary (its output is plain Go compiled in the same
// sandbox); when it is not installed the dialect is unavailable.
func (r *Runner) RunFW(ctx context.Context, fwSource string) (Result, error) {
	transpiler, err := exec.LookPath("ferrous-wheel")
	if err != nil {
		return Result{}, fmt.Errorf("execmode: ferrous-wheel not installed; the fw dialect is unavailable")
	}
	scratch, err := os.MkdirTemp("", "buckley-fw-*")
	if err != nil {
		return Result{}, fmt.Errorf("execmode: fw scratch: %w", err)
	}
	defer os.RemoveAll(scratch)
	fwPath := filepath.Join(scratch, "main.fw")
	if err := os.WriteFile(fwPath, []byte(fwSource), 0o644); err != nil {
		return Result{}, fmt.Errorf("execmode: write fw source: %w", err)
	}
	out, err := exec.CommandContext(ctx, transpiler, "emit", fwPath).Output()
	if err != nil {
		detail := err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			detail = strings.TrimSpace(string(exitErr.Stderr))
		}
		return Result{}, fmt.Errorf("execmode: fw transpile failed: %s", detail)
	}
	return r.Run(ctx, string(out))
}

// sharedGoCache returns the warm build cache all exec programs share.
// The cache is content-addressed, so sharing it across runs is safe and
// turns the per-run compile from tens of seconds into well under one.
func sharedGoCache() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "buckley-execmode", "gocache")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

type limitedWriter struct {
	buf *bytes.Buffer
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := maxOutputBytes - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		return len(p), nil
	}
	return w.buf.Write(p)
}

// scaffold writes the scratch module: go.mod, the caps client package,
// and the model's program as main.go.
func scaffold(dir, source string) error {
	files := map[string]string{
		"go.mod":       "module execprogram\n\ngo 1.24\n",
		"main.go":      source,
		"caps/caps.go": capsClientSource,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("execmode: scaffold: %w", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("execmode: scaffold %s: %w", name, err)
		}
	}
	return nil
}

// capsClientSource is the typed capability client the program imports as
// "execprogram/caps". It speaks plain HTTP over the run's unix socket
// with the run's bearer token; stdlib only, so `go run` needs no network
// (GOPROXY=off).
const capsClientSource = `// Package caps is the typed capability client for Buckley exec programs.
// Every call is brokered, jailed to the workspace, and audited.
package caps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
)

var client = &http.Client{Transport: &http.Transport{
	DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", os.Getenv("BUCKLEY_CAPS_SOCKET"))
	},
}}

func call(path string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", "http://caps"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("BUCKLEY_CAPS_TOKEN"))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(resp.Body)
		return fmt.Errorf("caps %s: %s: %s", path, resp.Status, msg.String())
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ReadFile returns up to 256 KiB of a workspace-relative file.
func ReadFile(path string) (content string, truncated bool, err error) {
	var out struct {
		Content   string ` + "`json:\"content\"`" + `
		Truncated bool   ` + "`json:\"truncated\"`" + `
	}
	err = call("/v1/files/read", map[string]any{"path": path}, &out)
	return out.Content, out.Truncated, err
}

// ListDir lists a workspace-relative directory; directories end in "/".
func ListDir(dir string) ([]string, error) {
	var out struct {
		Entries []string ` + "`json:\"entries\"`" + `
	}
	err := call("/v1/files/list", map[string]any{"dir": dir}, &out)
	return out.Entries, err
}

// Match is one text-search hit.
type Match struct {
	File string ` + "`json:\"file\"`" + `
	Line int    ` + "`json:\"line\"`" + `
	Text string ` + "`json:\"text\"`" + `
}

// SearchText finds literal-substring matches across the workspace.
func SearchText(pattern string) ([]Match, bool, error) {
	var out struct {
		Matches []Match ` + "`json:\"matches\"`" + `
		Capped  bool    ` + "`json:\"capped\"`" + `
	}
	err := call("/v1/search/text", map[string]any{"pattern": pattern}, &out)
	return out.Matches, out.Capped, err
}
`
