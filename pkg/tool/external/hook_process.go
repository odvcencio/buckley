package external

// Plugin hook mode: CLI and wire contract
//
// A plugin manifest that declares a hooks: section (see manifest.go) is
// spawned once per session as a long-lived process, in addition to (never
// instead of) its normal one-shot tool invocation:
//
//	<executable> hook
//
// The plugin is expected to keep running -- reading from stdin and
// writing to stdout -- until Buckley closes its stdin (clean session
// shutdown) or kills the process. It must still behave as an ordinary
// external tool (manifest.go's Executable, invoked with no arguments and
// a single JSON object on stdin) for its normal tool calls; hook mode is
// an additional invocation shape, not a replacement.
//
// Communication uses newline-delimited JSON (JSONL): each line, on
// either stream, is exactly one JSON object.
//
// Buckley writes two kinds of messages to the plugin's stdin:
//
//	Event delivery (fire-and-forget; no response is read for these):
//	  {"kind":"event","event":{"type":"tool.completed","timestamp":"...","sessionId":"...","data":{...}}}
//
//	  "event" carries a telemetry.Event, already redacted and
//	  byte-bounded (pkg/telemetry's NormalizeAndSanitize) -- the plugin
//	  never receives raw tool arguments or secrets.
//
//	Pre-tool veto request (a response is required):
//	  {"kind":"pre_tool","id":"<request-id>","tool":"write_file","args":{...sanitized...}}
//
//	  "args" is the tool call's arguments after the same
//	  redaction/bounding pass.
//
// The plugin answers "pre_tool" requests on stdout, one JSON object per
// line, echoing the request's id. Event messages get no response:
//
//	{"id":"<request-id>","decision":"allow","reason":""}
//	{"id":"<request-id>","decision":"deny","reason":"blocked by policy"}
//
// "decision" must be exactly "allow" or "deny" (case-insensitive); any
// other value, or no response within the manifest's pre_tool.timeout_ms,
// is treated as advisory-allow (with a logged warning) unless the
// manifest sets pre_tool.enforcing: true, in which case it denies. Any
// stdout line that isn't a well-formed {"id":...} response is ignored:
// the plugin may freely write its own logs to stderr, which Buckley
// captures for diagnostics but never parses as protocol output.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"m31labs.dev/buckley/v2/pkg/telemetry"
)

// hookMessageKind distinguishes the two message shapes written to a hook
// process's stdin.
type hookMessageKind string

const (
	hookMessageEvent   hookMessageKind = "event"
	hookMessagePreTool hookMessageKind = "pre_tool"
)

// hookMessage is the JSONL envelope written to a hook process's stdin.
type hookMessage struct {
	Kind  hookMessageKind  `json:"kind"`
	ID    string           `json:"id,omitempty"`
	Event *telemetry.Event `json:"event,omitempty"`
	Tool  string           `json:"tool,omitempty"`
	Args  map[string]any   `json:"args,omitempty"`
}

// hookResponse is the JSONL object a plugin's hook process writes back on
// stdout, in answer to a "pre_tool" request.
type hookResponse struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// hookResult carries either a parsed hookResponse or the reason none will
// ever arrive (process exited, stdin write failed, and so on).
type hookResult struct {
	resp *hookResponse
	err  error
}

// VetoDecision is the outcome of a single pre-tool veto request.
type VetoDecision struct {
	Denied bool
	Reason string
}

// stderrTailBytes bounds how much of a hook process's stderr is retained
// for the crash/warning message; it is diagnostic context, not a log
// store.
const stderrTailBytes = 4096

// HookProcess manages one plugin process running in hook mode: spawned
// once, kept alive for the session, receiving telemetry events and
// pre-tool veto requests over stdin and answering veto requests over
// stdout. See the package-level doc comment above for the full CLI/wire
// contract.
type HookProcess struct {
	pluginName string
	executable string
	workDir    string
	env        map[string]string

	// onWarn, when set, is called with a printf-style format/args for
	// conditions worth surfacing to an operator (a crash, a malformed
	// response) without failing the caller. A nil onWarn discards them.
	onWarn func(format string, args ...any)

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	closing bool
	dead    bool
	deadErr error

	pendingMu sync.Mutex
	pending   map[string]chan hookResult

	stderrMu   sync.Mutex
	stderrTail bytes.Buffer

	nextID   int64
	waitDone sync.WaitGroup
}

// NewHookProcess constructs a HookProcess for pluginName, ready to Start.
// executablePath must be the resolved, already-validated path to the
// plugin's executable (see DiscoverPlugins); workDir and env mirror
// ExternalTool's SetWorkDir/SetEnv.
func NewHookProcess(pluginName, executablePath, workDir string, env map[string]string) *HookProcess {
	return &HookProcess{
		pluginName: pluginName,
		executable: executablePath,
		workDir:    workDir,
		env:        env,
		pending:    make(map[string]chan hookResult),
	}
}

// SetWarnFunc installs the callback used to report crashes and malformed
// responses. Must be called before Start to see the process's own startup
// failures.
func (p *HookProcess) SetWarnFunc(fn func(format string, args ...any)) {
	if p == nil {
		return
	}
	p.onWarn = fn
}

// Start spawns "<executable> hook" and begins reading its stdout for
// pre-tool responses. It is safe to call at most once; a second call is a
// no-op once the process has been started.
func (p *HookProcess) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil
	}

	cmd := exec.Command(p.executable, "hook")
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}
	cmd.Env = mergeEnv(nil, p.env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("plugin %s: creating hook stdin pipe: %w", p.pluginName, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("plugin %s: creating hook stdout pipe: %w", p.pluginName, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("plugin %s: creating hook stderr pipe: %w", p.pluginName, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin %s: starting hook process: %w", p.pluginName, err)
	}

	p.cmd = cmd
	p.stdin = stdin

	p.waitDone.Add(1)
	go p.readLoop(stdout)
	go p.drainStderr(stderr)
	go p.waitLoop()

	return nil
}

// readLoop parses stdout as JSONL, dispatching well-formed {"id":...}
// responses to the matching in-flight RequestVeto call. Any line that
// isn't a recognizable response (blank, plugin debug output, malformed
// JSON, or an id with no matching pending request) is silently ignored:
// stdout is not a general-purpose log stream.
func (p *HookProcess) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp hookResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if strings.TrimSpace(resp.ID) == "" {
			continue
		}
		p.deliver(resp.ID, hookResult{resp: &resp})
	}
}

// drainStderr keeps the last stderrTailBytes of a hook process's stderr
// so a crash/timeout warning can include useful context, without holding
// unbounded plugin output in memory.
func (p *HookProcess) drainStderr(stderr io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			p.stderrMu.Lock()
			p.stderrTail.Write(buf[:n])
			if p.stderrTail.Len() > stderrTailBytes {
				trimmed := p.stderrTail.Bytes()
				trimmed = trimmed[len(trimmed)-stderrTailBytes:]
				p.stderrTail.Reset()
				p.stderrTail.Write(trimmed)
			}
			p.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (p *HookProcess) stderrSnapshot() string {
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	return strings.TrimSpace(p.stderrTail.String())
}

// waitLoop owns the single call to cmd.Wait(): it blocks until the
// process exits (crash or clean shutdown), marks the process dead, wakes
// every in-flight RequestVeto with an error, and -- unless Close()
// initiated the exit -- reports a warning naming the plugin.
func (p *HookProcess) waitLoop() {
	defer p.waitDone.Done()

	err := p.cmd.Wait()

	p.mu.Lock()
	p.dead = true
	if err == nil {
		err = fmt.Errorf("hook process exited")
	}
	p.deadErr = err
	closing := p.closing
	p.mu.Unlock()

	dead := fmt.Errorf("plugin %s hook process is no longer running: %w", p.pluginName, err)
	p.pendingMu.Lock()
	for id, ch := range p.pending {
		ch <- hookResult{err: dead}
		delete(p.pending, id)
	}
	p.pendingMu.Unlock()

	if !closing {
		tail := p.stderrSnapshot()
		if tail != "" {
			p.warn("plugin %s hook process exited unexpectedly: %v (stderr: %s)", p.pluginName, err, tail)
		} else {
			p.warn("plugin %s hook process exited unexpectedly: %v", p.pluginName, err)
		}
	}
}

func (p *HookProcess) warn(format string, args ...any) {
	if p.onWarn != nil {
		p.onWarn(format, args...)
	}
}

func (p *HookProcess) deliver(id string, result hookResult) {
	p.pendingMu.Lock()
	ch, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.pendingMu.Unlock()
	if ok {
		ch <- result
	}
}

func (p *HookProcess) nextRequestID() string {
	n := atomic.AddInt64(&p.nextID, 1)
	return fmt.Sprintf("%s-%d", p.pluginName, n)
}

// writeLine marshals msg and writes it, newline-terminated, to the
// process's stdin.
func (p *HookProcess) writeLine(msg hookMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("plugin %s: marshaling hook message: %w", p.pluginName, err)
	}
	data = append(data, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return fmt.Errorf("plugin %s hook process is not running: %w", p.pluginName, p.deadErr)
	}
	if p.stdin == nil {
		return fmt.Errorf("plugin %s hook process has not been started", p.pluginName)
	}
	_, err = p.stdin.Write(data)
	if err != nil {
		return fmt.Errorf("plugin %s: writing to hook stdin: %w", p.pluginName, err)
	}
	return nil
}

// SendEvent forwards event to the plugin's hook process as a "kind":
// "event" message. It is fire-and-forget: no response is expected or
// read. Callers should treat a non-nil error as advisory (log and
// continue) rather than fatal -- a dead hook process must never break the
// tool call or telemetry path it's merely observing.
func (p *HookProcess) SendEvent(event telemetry.Event) error {
	return p.writeLine(hookMessage{Kind: hookMessageEvent, Event: &event})
}

// RequestVeto sends a "kind":"pre_tool" request for tool/args and waits
// up to timeout for a matching response. The returned error is non-nil
// for every failure mode that isn't a clean allow/deny answer: a write
// failure, a dead process, a timeout, or a response whose "decision"
// field isn't "allow" or "deny". Callers (HookRunner.Veto) decide whether
// that maps to advisory-allow or enforcing-deny.
func (p *HookProcess) RequestVeto(ctx context.Context, tool string, args map[string]any, timeout time.Duration) (VetoDecision, error) {
	id := p.nextRequestID()
	ch := make(chan hookResult, 1)

	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
	}()

	if err := p.writeLine(hookMessage{Kind: hookMessagePreTool, ID: id, Tool: tool, Args: args}); err != nil {
		return VetoDecision{}, err
	}

	if timeout <= 0 {
		timeout = DefaultPreToolTimeoutMs * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case result := <-ch:
		if result.err != nil {
			return VetoDecision{}, result.err
		}
		return decisionFromResponse(p.pluginName, result.resp)
	case <-timer.C:
		return VetoDecision{}, fmt.Errorf("plugin %s: timed out after %s waiting for pre-tool hook response for %q", p.pluginName, timeout, tool)
	case <-ctx.Done():
		return VetoDecision{}, ctx.Err()
	}
}

func decisionFromResponse(pluginName string, resp *hookResponse) (VetoDecision, error) {
	if resp == nil {
		return VetoDecision{}, fmt.Errorf("plugin %s: empty pre-tool hook response", pluginName)
	}
	switch strings.ToLower(strings.TrimSpace(resp.Decision)) {
	case "allow":
		return VetoDecision{Denied: false}, nil
	case "deny":
		return VetoDecision{Denied: true, Reason: strings.TrimSpace(resp.Reason)}, nil
	default:
		return VetoDecision{}, fmt.Errorf("plugin %s: malformed pre-tool hook decision %q", pluginName, resp.Decision)
	}
}

// Close signals the plugin's hook process to shut down (by closing its
// stdin, so a well-behaved plugin sees EOF and exits on its own) and
// waits for it to exit, killing it if it doesn't within 2 seconds. Close
// is idempotent and safe to call on a process that was never started.
func (p *HookProcess) Close() error {
	p.mu.Lock()
	if p.cmd == nil {
		p.mu.Unlock()
		return nil
	}
	if p.closing {
		p.mu.Unlock()
		p.waitDone.Wait()
		return nil
	}
	p.closing = true
	stdin := p.stdin
	cmd := p.cmd
	p.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}

	done := make(chan struct{})
	go func() {
		p.waitDone.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
	return nil
}
