package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/acp"
)

const oneShotSummaryLimit = 100

// oneShotToolTracker prints one concise line per tool call to the one-shot
// progress writer. It prints tool names, short argument summaries (a command
// or a path), durations, and repository counters. It never prints file
// contents or tool output.
type oneShotToolTracker struct {
	mu       sync.Mutex
	writer   io.Writer
	workDir  string
	now      func() time.Time
	git      func(dir string, args ...string) (string, error)
	baseHead string
	seq      int
	calls    map[string]*oneShotToolCall
}

type oneShotToolCall struct {
	seq     int
	name    string
	summary string
	started time.Time
	vcs     string
}

func newOneShotToolTracker(writer io.Writer, workDir string) *oneShotToolTracker {
	t := &oneShotToolTracker{
		writer:  writer,
		workDir: workDir,
		now:     time.Now,
		git:     runOneShotGit,
		calls:   map[string]*oneShotToolCall{},
	}
	if workDir != "" {
		if head, err := t.git(workDir, "rev-parse", "HEAD"); err == nil {
			t.baseHead = head
		}
	}
	return t
}

func runOneShotGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func (t *oneShotToolTracker) start(u acp.SessionUpdate) {
	if t == nil || t.writer == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	name := u.ToolName
	if name == "" {
		name = u.Title
	}
	call := &oneShotToolCall{
		seq:     t.seq,
		name:    name,
		summary: summarizeOneShotToolInput(u),
		started: t.now(),
	}
	call.vcs = classifyOneShotVCS(call.name, u.RawInput)
	t.calls[u.ToolCallID] = call
	fmt.Fprintf(t.writer, "One-shot tool #%d start %s: %s\n", call.seq, call.name, call.summary)
}

func (t *oneShotToolTracker) update(u acp.SessionUpdate) {
	if t == nil || t.writer == nil {
		return
	}
	if u.Status != acp.ToolCallStatusCompleted && u.Status != acp.ToolCallStatusFailed {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	call, ok := t.calls[u.ToolCallID]
	if !ok {
		return
	}
	delete(t.calls, u.ToolCallID)
	result := "ok"
	if u.Status == acp.ToolCallStatusFailed {
		result = "FAIL"
	}
	dur := t.now().Sub(call.started).Round(100 * time.Millisecond)
	files, commits := t.repoCounts()
	fmt.Fprintf(t.writer, "One-shot tool #%d end %s %s in %s files_changed=%s commits=%s\n",
		call.seq, call.name, result, dur, files, commits)
	if call.vcs != "" {
		fmt.Fprintf(t.writer, "One-shot vcs: %s %s (%s) commits=%s\n", call.vcs, result, call.summary, commits)
	}
}

// repoCounts returns the number of changed files in the work tree and the
// number of commits made since the run began. Unknown values print as "?".
func (t *oneShotToolTracker) repoCounts() (files, commits string) {
	files, commits = "?", "?"
	if t.workDir == "" || t.git == nil {
		return
	}
	if out, err := t.git(t.workDir, "status", "--porcelain"); err == nil {
		n := 0
		if out != "" {
			n = strings.Count(out, "\n") + 1
		}
		files = fmt.Sprint(n)
	}
	if t.baseHead != "" {
		if out, err := t.git(t.workDir, "rev-list", "--count", t.baseHead+"..HEAD"); err == nil {
			commits = out
		}
	}
	return
}

var oneShotVCSPattern = regexp.MustCompile(`(?:^|[;&|(\s])(git(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+(commit|push)|buckley\s+(commit|pr))\b`)

func classifyOneShotVCS(name string, raw any) string {
	if name != "run_shell" {
		return ""
	}
	params, _ := raw.(map[string]any)
	cmd, _ := params["command"].(string)
	m := oneShotVCSPattern.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	switch {
	case m[2] != "":
		return m[2]
	case m[3] == "commit":
		return "commit"
	default:
		return "pr"
	}
}

func summarizeOneShotToolInput(u acp.SessionUpdate) string {
	params, _ := u.RawInput.(map[string]any)
	str := func(k string) string {
		v, _ := params[k].(string)
		return v
	}
	var s string
	switch {
	case str("command") != "":
		s = redactOneShotSecrets(str("command"))
	case str("path") != "":
		s = str("path")
	case str("query") != "":
		s = redactOneShotSecrets(str("query"))
	case str("target") != "":
		s = str("target")
	default:
		s = "-"
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > oneShotSummaryLimit {
		s = string(r[:oneShotSummaryLimit]) + "..."
	}
	return s
}

var (
	oneShotEnvSecret = regexp.MustCompile(`(?i)\b([A-Za-z0-9_]*(?:token|key|secret|passw(?:or)?d|credential|auth|bearer)[A-Za-z0-9_]*)=("[^"]*"|'[^']*'|\S+)`)
	oneShotFlagSec   = regexp.MustCompile(`(?i)(--?[a-z-]*(?:token|key|secret|password|auth)[a-z-]*)(=|\s+)("[^"]*"|'[^']*'|\S+)`)
	oneShotBearer    = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	oneShotTokenLike = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|AKIA[0-9A-Z]{12,}|xox[abp]-[A-Za-z0-9-]{8,})`)
)

func redactOneShotSecrets(s string) string {
	s = oneShotEnvSecret.ReplaceAllString(s, "$1=[redacted]")
	s = oneShotFlagSec.ReplaceAllString(s, "$1$2[redacted]")
	s = oneShotBearer.ReplaceAllString(s, "$1 [redacted]")
	s = oneShotTokenLike.ReplaceAllString(s, "[redacted]")
	return s
}
