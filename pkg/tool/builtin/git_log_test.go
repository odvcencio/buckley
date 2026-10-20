package builtin

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestGitLogTool_CancellationStopsStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := statusTestRepo(t)
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = rev-parse ]; then printf '%s\\n' \"$LOG_TEST_HEAD\"; exit 0; fi; done\n/bin/sleep 10\n"
	writeFile(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("LOG_TEST_HEAD", strings.Repeat("a", 40))
	tool := &GitLogTool{}
	tool.SetWorkDir(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, e := tool.ExecuteWithContext(ctx, map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "deadline") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}

func logTestCommit(t *testing.T, dir, message string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "message")
	writeFile(t, path, message)
	statusTestGit(t, dir, "commit", "--allow-empty", "-q", "-F", path)
	return strings.TrimSpace(statusTestGit(t, dir, "rev-parse", "HEAD"))
}
func TestGitLogTool_CommitCountAndTextRecovery(t *testing.T) {
	dir := statusTestRepo(t)
	id := logTestCommit(t, dir, "subject\n\n"+strings.Repeat("界payload\n", 3000))
	tool := &GitLogTool{}
	tool.SetWorkDir(dir)
	params := map[string]any{"count": 1, "oneline": false, "anchor": id}
	var text strings.Builder
	for pages := 0; pages < 30; pages++ {
		r, e := tool.Execute(params)
		if e != nil || !r.Success || r.Data["count"] != 1 {
			t.Fatalf("result=%+v err=%v", r, e)
		}
		rows := r.Data["commits"].([]string)
		if len(rows) != 1 {
			t.Fatal("count measures lines instead of commits")
		}
		text.WriteString(rows[0])
		next := r.Data["next_text_cursors"].(map[string]string)[id]
		if next == "" {
			break
		}
		params = map[string]any{"text_cursor": next, "count": -1, "oneline": true, "anchor": "", "skip": 999}
	}
	want := statusTestGit(t, dir, "--no-replace-objects", "log", "-1", "-z", "--no-color", "--encoding=UTF-8", "--format=commit %H%nAuthor: %an <%ae>%nDate: %aI%n%n%B", id)
	want = strings.TrimSuffix(want, "\x00")
	if text.String() != want {
		t.Fatalf("text pages differ: got%d want%d", text.Len(), len(want))
	}
}
func TestGitLogTool_PinnedHistoryAcrossMergeAndHeadAdvance(t *testing.T) {
	dir := statusTestRepo(t)
	branch := strings.TrimSpace(statusTestGit(t, dir, "branch", "--show-current"))
	statusTestGit(t, dir, "checkout", "-qb", "side")
	logTestCommit(t, dir, "side")
	statusTestGit(t, dir, "checkout", "-q", branch)
	logTestCommit(t, dir, "main")
	statusTestGit(t, dir, "merge", "--no-ff", "-qm", "merge side", "side")
	tip := logTestCommit(t, dir, "tip")
	want := strings.Fields(statusTestGit(t, dir, "--no-replace-objects", "log", "--topo-order", "--format=%H", tip))
	tool := &GitLogTool{}
	tool.SetWorkDir(dir)
	params := map[string]any{"count": 2}
	var got []string
	anchor := ""
	for page := 0; page < 10; page++ {
		r, e := tool.Execute(params)
		if e != nil || !r.Success {
			t.Fatalf("result=%+v err=%v", r, e)
		}
		for _, line := range r.Data["commits"].([]string) {
			got = append(got, strings.Fields(line)[0])
		}
		anchor = r.Data["anchor"].(string)
		if page == 0 {
			logTestCommit(t, dir, "new HEAD must not shift pinned pages")
		}
		next, more := r.Data["next_skip"].(int64)
		if !more {
			break
		}
		params = map[string]any{"count": 2, "anchor": anchor, "skip": next}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("pinned pages=%v want=%v", got, want)
	}
}
func TestGitLogTool_BoundedResultsAndValidation(t *testing.T) {
	dir := statusTestRepo(t)
	for i := 0; i < 24; i++ {
		logTestCommit(t, dir, fmt.Sprintf("subject%d %s\n\nbody", i, strings.Repeat("s", 300)))
	}
	tool := &GitLogTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultGitLogOutputBytes, 4096} {
		tool.SetMaxOutputBytes(budget)
		r, e := tool.Execute(map[string]any{"count": 100, "oneline": false})
		if e != nil || !r.Success || r.Data["count"].(int) < 1 || len(r.Data["next_text_cursors"].(map[string]string)) == 0 {
			t.Fatalf("result=%+v err=%v", r, e)
		}
		for _, format := range []bool{true, false} {
			data, e := toon.New(format).MarshalString(r)
			if e != nil || len(data) > budget {
				t.Fatalf("toon=%v bytes=%d err=%v", format, len(data), e)
			}
		}
	}
	tool.SetMaxOutputBytes(0)
	for _, params := range []map[string]any{{"count": 0}, {"count": -1}, {"count": 1.5}, {"count": 101}, {"oneline": "false"}, {"skip": -1}, {"skip": maxGitLogSkip + 1}, {"anchor": "--all"}, {"text_cursor": "bad"}, {"path": false}} {
		r, e := tool.Execute(params)
		if e != nil || r.Success || r.Error == "" {
			t.Fatalf("params=%v result=%+v err=%v", params, r, e)
		}
	}
	r, e := tool.Execute(map[string]any{"count": 1, "anchor": "", "text_cursor": nil})
	if e != nil || !r.Success {
		t.Fatalf("empty optional selectors: %+v %v", r, e)
	}
	tool.SetMaxOutputBytes(20)
	r, e = tool.Execute(map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "budget") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
func TestGitLogTool_LengthFramingAndMalformedRecords(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := statusTestRepo(t)
	bin := t.TempDir()
	payload := filepath.Join(t.TempDir(), "payload")
	id := strings.Repeat("a", 40)
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = rev-parse ]; then printf '%s\\n' \"$LOG_TEST_HEAD\"; exit 0; fi; done\n/bin/cat \"$LOG_TEST_PAYLOAD\"\n"
	writeFile(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("LOG_TEST_HEAD", id)
	t.Setenv("LOG_TEST_PAYLOAD", payload)
	frame := func(text string) string {
		raw := id + "\x00" + text
		return fmt.Sprintf("log size %d\n%s\x00", len(raw), raw)
	}
	tool := &GitLogTool{}
	tool.SetWorkDir(dir)
	text := "first\nlog size 999\nembedded\x00data\n"
	writeFile(t, payload, frame(text))
	r, e := tool.Execute(map[string]any{"count": 1})
	if e != nil || !r.Success || r.Data["count"] != 1 || r.Data["commits"].([]string)[0] != text {
		t.Fatalf("framing corrupted content: %+v %v", r, e)
	}
	for _, data := range []string{"bad\n", "log size -1\n", "log size 4\nnope", "log size 99\nshort", strings.TrimSuffix(frame(text), "\x00") + "X", fmt.Sprintf("log size %d\n", DefaultGitLogScanBytes+1)} {
		writeFile(t, payload, data)
		r, e := tool.Execute(map[string]any{"count": 1})
		if e != nil || r.Success {
			t.Fatalf("accepted malformed record: %+v %v", r, e)
		}
	}
	writeFile(t, payload, frame(text)+fmt.Sprintf("log size %d\n", DefaultGitLogScanBytes+1))
	r, e = tool.Execute(map[string]any{"count": 1})
	if e != nil || !r.Success || r.Data["count"] != 1 || r.Data["has_more"] != true {
		t.Fatalf("lookahead consumed an unrequested huge message: %+v %v", r, e)
	}
	raw, _ := json.Marshal(gitLogTextCursor{Version: 1, Commit: id, Offset: 999, Oneline: true})
	writeFile(t, payload, frame(text))
	r, e = tool.Execute(map[string]any{"text_cursor": base64.RawURLEncoding.EncodeToString(raw)})
	if e != nil || r.Success {
		t.Fatalf("accepted cursor beyond message: %+v %v", r, e)
	}
}
func TestGitLogTool_ContextAndRecordHeaderBound(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 5000)), 4096)
	if _, err := gitLogRecordSize(reader); err == nil {
		t.Fatal("accepted unbounded header")
	}
	dir := statusTestRepo(t)
	tool := &GitLogTool{}
	tool.SetWorkDir(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := tool.ExecuteWithContext(ctx, map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "canceled") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	r, e = tool.ExecuteWithContext(ctx, map[string]any{})
	if e != nil || r.Success {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
