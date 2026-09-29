package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/acp"
)

func fakeToolTracker(buf *bytes.Buffer, now *time.Time, gitOut map[string]string) *oneShotToolTracker {
	t := &oneShotToolTracker{
		writer:  buf,
		workDir: "/work",
		now:     func() time.Time { return *now },
		calls:   map[string]*oneShotToolCall{},
		git: func(dir string, args ...string) (string, error) {
			return gitOut[args[0]], nil
		},
		baseHead: "abc",
	}
	return t
}

func toolStart(id, name string, input map[string]any) acp.SessionUpdate {
	return acp.SessionUpdate{SessionUpdate: acp.SessionUpdateToolCall, ToolCallID: id, ToolName: name, RawInput: input}
}

func TestOneShotToolLines_SummaryDurationCounts(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1000, 0)
	tr := fakeToolTracker(&buf, &now, map[string]string{"status": " M a.go\n?? b.go", "rev-list": "2"})

	tr.start(toolStart("c1", "run_shell", map[string]any{"command": "go test ./pkg/...\n-run X"}))
	now = now.Add(3200 * time.Millisecond)
	tr.update(acp.SessionUpdate{ToolCallID: "c1", Status: acp.ToolCallStatusCompleted, RawOutput: "SECRET OUTPUT"})
	tr.start(toolStart("c2", "read_file", map[string]any{"path": "pkg/x/y.go", "content": "FILE BODY"}))
	now = now.Add(time.Second)
	tr.update(acp.SessionUpdate{ToolCallID: "c2", Status: acp.ToolCallStatusFailed})

	out := buf.String()
	for _, want := range []string{
		"One-shot tool #1 start run_shell: go test ./pkg/... -run X",
		"One-shot tool #1 end run_shell ok in 3.2s files_changed=2 commits=2",
		"One-shot tool #2 start read_file: pkg/x/y.go",
		"One-shot tool #2 end read_file FAIL in 1s",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"SECRET OUTPUT", "FILE BODY"} {
		if strings.Contains(out, bad) {
			t.Fatalf("leaked %q in:\n%s", bad, out)
		}
	}
}

func TestOneShotToolLines_InProgressUpdatesAreSilent(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1, 0)
	tr := fakeToolTracker(&buf, &now, nil)
	tr.start(toolStart("c1", "run_shell", map[string]any{"command": "sleep 1"}))
	before := buf.Len()
	tr.update(acp.SessionUpdate{ToolCallID: "c1", Status: acp.ToolCallStatusInProgress})
	if buf.Len() != before {
		t.Fatalf("in-progress update wrote output: %q", buf.String())
	}
}

func TestOneShotToolLines_SummaryTruncatedTo100(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1, 0)
	tr := fakeToolTracker(&buf, &now, nil)
	tr.start(toolStart("c1", "run_shell", map[string]any{"command": strings.Repeat("x", 300)}))
	want := strings.Repeat("x", 100) + "..."
	if !strings.Contains(buf.String(), ": "+want+"\n") {
		t.Fatalf("summary not truncated to 100 chars: %q", buf.String())
	}
}

func TestOneShotToolLines_VCSEvents(t *testing.T) {
	for _, tc := range []struct{ cmd, want string }{
		{"git commit -m x", "One-shot vcs: commit ok"},
		{"cd x && git push -u origin HEAD", "One-shot vcs: push ok"},
		{"buckley commit --yes --minimal-output -- a.go", "One-shot vcs: commit ok"},
		{"git -C /w commit --amend", "One-shot vcs: commit ok"},
	} {
		var buf bytes.Buffer
		now := time.Unix(1, 0)
		tr := fakeToolTracker(&buf, &now, map[string]string{"rev-list": "1"})
		tr.start(toolStart("c", "run_shell", map[string]any{"command": tc.cmd}))
		tr.update(acp.SessionUpdate{ToolCallID: "c", Status: acp.ToolCallStatusCompleted})
		if !strings.Contains(buf.String(), tc.want) {
			t.Fatalf("%q: missing %q in %q", tc.cmd, tc.want, buf.String())
		}
	}
	var buf bytes.Buffer
	now := time.Unix(1, 0)
	tr := fakeToolTracker(&buf, &now, nil)
	tr.start(toolStart("c", "run_shell", map[string]any{"command": "git status && echo commit"}))
	tr.update(acp.SessionUpdate{ToolCallID: "c", Status: acp.ToolCallStatusCompleted})
	if strings.Contains(buf.String(), "vcs:") {
		t.Fatalf("false vcs event: %q", buf.String())
	}
}

func TestRedactOneShotSecrets(t *testing.T) {
	for _, tc := range []struct{ in, gone string }{
		{"GITHUB_TOKEN=ghp_abcdefghijklmnop gh pr list", "ghp_abcdefghijklmnop"},
		{"OPENROUTER_API_KEY=sk-or-v1-1234567890 buckley", "sk-or-v1-1234567890"},
		{`DB_PASSWORD="hunter two" run`, "hunter"},
		{"curl -H 'Authorization: Bearer abcdefghijklmnop' x", "abcdefghijklmnop"},
		{"tool --api-key hunter22secret go", "hunter22secret"},
		{"echo sk-abcdefghijklmnop", "sk-abcdefghijklmnop"},
	} {
		got := redactOneShotSecrets(tc.in)
		if strings.Contains(got, tc.gone) {
			t.Errorf("redact(%q) = %q still contains %q", tc.in, got, tc.gone)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("redact(%q) = %q has no marker", tc.in, got)
		}
	}
	if got := redactOneShotSecrets("go test ./... -count=1 KEEP=1"); got != "go test ./... -count=1 KEEP=1" {
		t.Errorf("over-redacted: %q", got)
	}
}

func TestOneShotProgress_CountersThrottledTo30s(t *testing.T) {
	if oneShotProgressMinInterval != 30*time.Second {
		t.Fatalf("interval = %v, want 30s", oneShotProgressMinInterval)
	}
	var buf bytes.Buffer
	now := time.Unix(1, 0)
	p := &oneShotProgress{
		writer: &buf, now: func() time.Time { return now },
		minInterval: oneShotProgressMinInterval, phase: "starting",
		tools: fakeToolTracker(&buf, &now, nil),
	}
	for i := 0; i < 5; i++ {
		_ = p.Stream(toolStart("c", "read_file", map[string]any{"path": "a"}))
		now = now.Add(5 * time.Second)
	}
	if got := strings.Count(buf.String(), "One-shot progress:"); got != 1 {
		t.Fatalf("counter lines = %d in 25s, want 1", got)
	}
	if got := strings.Count(buf.String(), "One-shot tool #"); got != 5 {
		t.Fatalf("tool lines = %d, want 5 (never throttled)", got)
	}
	now = now.Add(31 * time.Second)
	_ = p.Stream(acp.NewAgentThoughtChunk("x"))
	if got := strings.Count(buf.String(), "One-shot progress:"); got != 2 {
		t.Fatalf("counter lines = %d after 30s, want 2", got)
	}
}
