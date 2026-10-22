package execmode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one program run.
	DefaultTimeout = 120 * time.Second
	// maxOutputBytes bounds captured stdout/stderr each.
	maxOutputBytes = 128 * 1024
	// maxTranspilerStderrBytes keeps syntax diagnostics compact for callers.
	maxTranspilerStderrBytes = 8 * 1024
	// MaxSourceBytes bounds program source supplied by library and CLI callers.
	MaxSourceBytes = 256 * 1024
	// DefaultRuntimeScratchBytes bounds allocated file data in the runtime's
	// ephemeral scratch/tmp filesystem, independently of compilation and heap memory.
	DefaultRuntimeScratchBytes int64 = 64 << 20
	// DefaultCompilerTempBytes bounds allocated data in the sandboxed compiler's
	// temporary filesystem, separately from its build cache and heap memory.
	DefaultCompilerTempBytes int64 = 512 << 20
)

// Result is one program run's outcome.
type Result struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	Duration        time.Duration
	StdoutTruncated bool
	StderrTruncated bool
}

// Runner executes model-written Go programs against a jailed broker. The
// zero value is unusable; construct with NewRunner.
type Runner struct {
	workspaceRoot    string
	audit            AuditSink
	timeout          time.Duration
	isolation        string
	allowUnsandboxed bool
	capabilities     []string
	outline          OutlineFunc
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

// WithCapabilitySet restricts every run to a capability grant (see
// execmode.ReadOnlySet, execmode.MinimalSet). The default is the full
// read-only surface.
func WithCapabilitySet(capabilities ...string) RunnerOption {
	return func(r *Runner) { r.capabilities = capabilities }
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
		capabilities:  ReadOnlySet,
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
// Isolation boundary: by default the process runs under bubblewrap with
// no network, a read-only system view, no workspace mount, and writes
// confined to scratch; the environment is scrubbed and GOPROXY is off.
// Run refuses to execute when that sandbox is unavailable unless the
// caller passed WithoutIsolation, which downgrades the surface to
// shell-equivalent risk and is for library and test use only.
func (r *Runner) Run(ctx context.Context, source string) (result Result, retErr error) {
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("execmode: run interrupted: %w", err)
	}
	if len(source) > MaxSourceBytes {
		return Result{}, fmt.Errorf("execmode: source exceeds %d bytes", MaxSourceBytes)
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

	// The token outlives the program by a margin only, and the grant is
	// the run's, not the surface's.
	broker, err := NewBroker(r.workspaceRoot, r.audit,
		WithCapabilities(r.capabilities...),
		WithTokenTTL(r.timeout+time.Minute))
	if err != nil {
		return Result{}, err
	}
	broker.outline = r.outline
	if err := broker.Start(filepath.Join(scratch, "caps.sock")); err != nil {
		return Result{}, err
	}
	defer broker.Close()

	goCache := sharedGoCache()
	cache, err := acquireBuildCache(runCtx, goCache, DefaultBuildCacheBytes, DefaultBuildCacheEntries)
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("execmode: acquire build cache: %w", err)
	}
	defer func() {
		if err := cache.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("execmode: trim build cache: %w", err))
		}
	}()
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
	// Trim scratch paths so identical programs share build artifacts too.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + scratch,
		"GOCACHE=" + goCache,
		"GOFLAGS=-mod=mod -trimpath",
		"GOPROXY=off",
		"BUCKLEY_CAPS_SOCKET=" + broker.SocketPath(),
		"BUCKLEY_CAPS_TOKEN=" + broker.Token(),
	}
	// `go run` does not forward a SIGKILL to the compiled child on Unix, so
	// configureProcess puts the program in its own process group there. The
	// Windows implementation leaves the standard os/exec cancellation path
	// intact; the helper keeps this runner portable across release targets.
	configureProcess(cmd)
	// WaitDelay backstops pipe readers held by orphans.
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	stdoutW := &limitedWriter{buf: &stdout}
	stderrW := &limitedWriter{buf: &stderr}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	started := time.Now()
	err = cmd.Run()
	result = Result{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		Duration:        time.Since(started),
		StdoutTruncated: stdoutW.truncated,
		StderrTruncated: stderrW.truncated,
	}
	if err != nil {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			result.ExitCode = -1
			if ctxErr == context.DeadlineExceeded {
				return result, fmt.Errorf("execmode: program timeout: %w", ctxErr)
			}
			return result, fmt.Errorf("execmode: run interrupted: %w", ctxErr)
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
	if len(fwSource) > MaxSourceBytes {
		return Result{}, fmt.Errorf("execmode: fw source exceeds %d bytes", MaxSourceBytes)
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("execmode: fw transpile interrupted: %w", err)
	}
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
	cmd := exec.CommandContext(runCtx, transpiler, "emit", fwPath)
	configureProcess(cmd)
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	stdoutW := &limitedWriter{buf: &stdout, limit: MaxSourceBytes, onTruncate: cancel}
	stderrW := &limitedWriter{buf: &stderr, limit: maxTranspilerStderrBytes}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	err = cmd.Run()
	if stdoutW.truncated {
		return Result{}, fmt.Errorf("execmode: fw generated Go exceeds %d bytes", MaxSourceBytes)
	}
	if runCtx.Err() != nil {
		return Result{ExitCode: -1}, fmt.Errorf("execmode: fw transpile interrupted: %w", runCtx.Err())
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		if stderrW.truncated {
			detail += fmt.Sprintf(" [stderr truncated at %d bytes]", maxTranspilerStderrBytes)
		}
		return Result{}, fmt.Errorf("execmode: fw transpile failed: %s", detail)
	}
	return r.Run(runCtx, stdout.String())
}

// sharedGoCache returns the warm build cache all exec programs share.
// The cache is content-addressed, so sharing it across runs is safe and
// turns the per-run compile from tens of seconds into well under one.
func sharedGoCache() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	// Older binaries do not hold leases; keep their live compiler inputs separate.
	dir := filepath.Join(base, "buckley-execmode", "gocache-v2")
	return dir
}

type limitedWriter struct {
	buf        *bytes.Buffer
	limit      int
	truncated  bool
	onTruncate func()
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	limit := w.limit
	if limit == 0 {
		limit = maxOutputBytes
	}
	keep := min(len(p), max(0, limit-w.buf.Len()))
	if keep < len(p) && !w.truncated {
		w.truncated = true
		if w.onTruncate != nil {
			w.onTruncate()
		}
	}
	w.buf.Write(p[:keep])
	return len(p), nil
}

// scaffold writes the scratch module: go.mod, the caps client package,
// and the model's program as main.go.
func scaffold(dir, source string) error {
	files := map[string]string{
		"go.mod":       "module execprogram\n\ngo 1.24\n",
		"main.go":      source,
		"caps/caps.go": capsClientSource,
	}
	// Fresh compiler homes otherwise collect Go counters that every run discards.
	if runtime.GOOS != "windows" {
		configDir := ".config"
		if runtime.GOOS == "darwin" {
			configDir = filepath.Join("Library", "Application Support")
		}
		files[filepath.Join(configDir, "go", "telemetry", "mode")] = "off\n"
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
	"strconv"
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

// ReadFileChunk returns up to 256 KiB of a workspace-relative file starting
// at byte offset. Continue large files by advancing offset by int64(len(data))
// until truncated is false.
func ReadFileChunk(path string, offset int64) (data []byte, truncated bool, err error) {
	var out struct {
		Data      []byte ` + "`json:\"data\"`" + `
		Truncated bool   ` + "`json:\"truncated\"`" + `
	}
	err = call("/v1/files/read", map[string]any{"path": path, "offset": strconv.FormatInt(offset, 10)}, &out)
	return out.Data, out.Truncated, err
}

// OutlineFile returns one bounded page of Canopy's live structural JSON.
// Keep coverage warnings and follow next_cursor unchanged; edits invalidate it.
func OutlineFile(path string, limit int, cursor string) (json.RawMessage, error) {
	var out json.RawMessage
	err := call("/v1/code/outline", map[string]any{"path": path, "limit": limit, "cursor": cursor}, &out)
	return out, err
}

type listPage struct {
	Entries    []string ` + "`json:\"entries\"`" + `
	Capped     bool     ` + "`json:\"capped\"`" + `
	NextCursor int      ` + "`json:\"next_cursor\"`" + `
}

func listAll(dir string, recursive bool) ([]string, error) {
	var entries []string
	cursor := 0
	for {
		var out listPage
		params := map[string]any{"dir": dir, "recursive": recursive, "cursor": cursor}
		if err := call("/v1/files/list", params, &out); err != nil {
			return nil, err
		}
		entries = append(entries, out.Entries...)
		if !out.Capped {
			return entries, nil
		}
		if out.NextCursor <= cursor {
			return nil, fmt.Errorf("caps files/list pagination did not advance from cursor %d", cursor)
		}
		cursor = out.NextCursor
	}
}

// ListDir lists a complete workspace-relative directory; directories end in
// "/". Broker response pagination is transparent to callers.
func ListDir(dir string) ([]string, error) {
	return listAll(dir, false)
}

// WalkDir lists a complete directory tree in one program-level call, returning
// workspace-relative paths (directories end in "/"). Broker response
// pagination is transparent to callers.
func WalkDir(dir string) ([]string, error) {
	return listAll(dir, true)
}

// Match is one text-search hit. Text is a bounded excerpt, not the complete line.
type Match struct {
	File string ` + "`json:\"file\"`" + `
	Line int    ` + "`json:\"line\"`" + `
	Text string ` + "`json:\"text\"`" + `
	TextTruncated bool ` + "`json:\"text_truncated\"`" + `
}

// SearchText finds literal-substring matches across the workspace.
func SearchText(pattern string) ([]Match, bool, error) {
	return SearchTextGlob(pattern, "")
}

// SearchTextGlob restricts SearchText to files whose base name matches a
// glob, for example "*.go". One call replaces search-then-filter.
func SearchTextGlob(pattern, glob string) ([]Match, bool, error) {
	var out struct {
		Matches []Match ` + "`json:\"matches\"`" + `
		Capped  bool    ` + "`json:\"capped\"`" + `
	}
	err := call("/v1/search/text", map[string]any{"pattern": pattern, "glob": glob}, &out)
	return out.Matches, out.Capped, err
}
`

// CapsAPICard is the compact API reference and canonical example the
// exec_program tool description embeds. Teaching the surface up front
// costs a few hundred prompt tokens once; discovering it by trial costs
// whole model turns (the first live run spent seven).
const CapsAPICard = "API (import \"execprogram/caps\"):\n" +
	"  caps.OutlineFile(path string, limit int, cursor string) (json.RawMessage, error) // optional Canopy; 0=40 symbols, max 100; first cursor=\"\"\n" +
	"  caps.ReadFile(path string) (content string, truncated bool, err error)\n" +
	"  caps.ReadFileChunk(path string, offset int64) (data []byte, truncated bool, err error)\n" +
	"  caps.ListDir(dir string) (entries []string, err error)        // one level; dirs end in \"/\"\n" +
	"  caps.WalkDir(dir string) (entries []string, err error)        // whole tree, workspace-relative paths\n" +
	"  caps.SearchText(pattern string) (m []caps.Match, capped bool, err error)\n" +
	"  caps.SearchTextGlob(pattern, glob string) (m []caps.Match, capped bool, err error)\n" +
	"  type Match struct { File string; Line int; Text string; TextTruncated bool }\n" +
	"Search Text is an excerpt: at most 2 KiB per hit and 64 KiB total. TextTruncated marks clipped or omitted text; File/Line still identify the hit. Use targeted file/chunk reads for complete source. capped reports the match-count limit, independently of text truncation.\n" +
	"OutlineFile returns a JSON object: {symbols:[{kind,name,start_line,end_line}],coverage:{...},next_cursor?,source_sha256,source_bytes}. Decode coverage as json.RawMessage and print it unchanged; check json.Unmarshal errors. Use symbol ranges for targeted source reads. Follow next_cursor unchanged; pages count as calls and edits invalidate it. An outline is navigation, not inspected source or absence proof. If unavailable, use search/read.\n" +
	"Paths are workspace-relative. One program may make at most 32 broker operations (rejected attempts count; exhaustion closes the broker); paginated WalkDir calls count, so prefer targeted ListDir/SearchTextGlob and read only selected files. Large files: use ReadFileChunk and continue by advancing offset by int64(len(data)) until truncated is false; every call counts toward the 32-operation budget, so check errors and stop promptly. Chunk reads do not pin a file snapshot. Data is raw bytes, so carry a line split across chunk boundaries when doing line-based extraction. Managed .git/.worktrees, node_modules, and vendor directories are omitted from recursive discovery. Example:\n" +
	"package main\n\n" +
	"import (\n\t\"fmt\"\n\n\t\"execprogram/caps\"\n)\n\n" +
	"func main() {\n" +
	"\tmatches, capped, err := caps.SearchTextGlob(\"TODO\", \"*.go\")\n" +
	"\tif err != nil {\n\t\tpanic(err)\n\t}\n" +
	"\tlimit := len(matches)\n" +
	"\tif limit > 5 {\n\t\tlimit = 5\n\t}\n" +
	"\tfmt.Printf(\"matches=%d capped=%v\\n\", len(matches), capped)\n" +
	"\tfor _, match := range matches[:limit] {\n" +
	"\t\tfmt.Printf(\"%s:%d text_truncated=%v %s\\n\", match.File, match.Line, match.TextTruncated, match.Text)\n" +
	"\t}\n" +
	"}"
