package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestGitStatusTool_RejectsMalformedAndOversizedStreams(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := statusTestRepo(t)
	bin := t.TempDir()
	payload := filepath.Join(t.TempDir(), "payload")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n if [ \"$arg\" = rev-parse ]; then printf '%s\\n' \"$STATUS_TEST_ROOT\"; exit 0; fi\ndone\n/bin/cat \"$STATUS_TEST_PAYLOAD\"\n"
	writeFile(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STATUS_TEST_ROOT", dir)
	t.Setenv("STATUS_TEST_PAYLOAD", payload)
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	for _, raw := range []string{"invalid\x00", "?? unterminated", "R  new\x00", "R  new\x00\x00", "?? " + strings.Repeat("x", maxGitStatusTokenBytes) + "\x00"} {
		writeFile(t, payload, raw)
		r, e := tool.Execute(map[string]any{})
		if e != nil || r.Success || r.Error == "" {
			t.Fatalf("accepted malformed stream: result=%+v err=%v", r, e)
		}
	}
	file, err := os.Create(payload)
	if err != nil {
		t.Fatal(err)
	}
	line := "?? " + strings.Repeat("x", 4092) + "\x00"
	for i := 0; i < DefaultGitStatusScanBytes/len(line)+1; i++ {
		if _, err := file.WriteString(line); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	r, e := tool.Execute(map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "scan budget") {
		t.Fatalf("unbounded stream: result=%+v err=%v", r, e)
	}
}
func TestGitStatusTool_CancellationStopsStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := statusTestRepo(t)
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = rev-parse ]; then printf '%s\\n' \"$STATUS_TEST_ROOT\"; exit 0; fi; done\nprintf '?? file\\000'\n/bin/sleep 10\n"
	writeFile(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STATUS_TEST_ROOT", dir)
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, e := tool.ExecuteWithContext(ctx, map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "deadline") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}

func statusTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
func statusTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	statusTestGit(t, dir, "init", "-q")
	for k, v := range map[string]string{"user.name": "Test", "user.email": "test@example.com", "commit.gpgSign": "false", "maintenance.auto": "false", "gc.auto": "0", "core.hooksPath": t.TempDir()} {
		statusTestGit(t, dir, "config", k, v)
	}
	writeFile(t, filepath.Join(dir, "tracked.txt"), "tracked\n")
	statusTestGit(t, dir, "add", "--", "tracked.txt")
	statusTestGit(t, dir, "commit", "-qm", "baseline")
	return dir
}
func TestGitStatusTool_PagesAndSnapshots(t *testing.T) {
	dir := statusTestRepo(t)
	for i := 0; i < 137; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), "data")
	}
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	var all strings.Builder
	offset := int64(0)
	hash := ""
	for pages := 0; pages < 20; pages++ {
		params := map[string]any{"offset": offset, "limit": 17}
		if hash != "" {
			params["expected_sha256"] = hash
		}
		r, e := tool.Execute(params)
		if e != nil || !r.Success || r.Data["count"] != 137 || r.Data["clean"] != false || r.Data["repository_root"] != filepath.ToSlash(dir) && r.Data["repository_root"] != dir {
			t.Fatalf("result=%+v err=%v", r, e)
		}
		all.WriteString(r.Data["status"].(string))
		hash = r.Data["status_sha256"].(string)
		next, more := r.Data["next_offset"].(int64)
		if !more {
			break
		}
		if next <= offset {
			t.Fatal("page did not advance")
		}
		offset = next
	}
	var want strings.Builder
	for i := 0; i < 137; i++ {
		fmt.Fprintf(&want, "?? f%03d.txt\n", i)
	}
	if all.String() != want.String() {
		t.Fatalf("pages lost or repeated entries: %d vs%d", all.Len(), want.Len())
	}
	writeFile(t, filepath.Join(dir, "new.txt"), "new")
	r, e := tool.Execute(map[string]any{"offset": 17, "expected_sha256": hash})
	if e != nil || r.Success || !strings.Contains(r.Error, "changed") {
		t.Fatalf("stale page accepted: %+v %v", r, e)
	}
}
func TestGitStatusTool_LiteralScopeAndCleanPage(t *testing.T) {
	dir := statusTestRepo(t)
	writeFile(t, filepath.Join(dir, "file[1].txt"), "literal")
	writeFile(t, filepath.Join(dir, "file1.txt"), "other")
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	r, e := tool.Execute(map[string]any{"file": "file[1].txt"})
	if e != nil || !r.Success || r.Data["count"] != 1 || r.Data["status"] != "?? file[1].txt\n" {
		t.Fatalf("result=%+v err=%v", r, e)
	}
	hash := r.Data["status_sha256"].(string)
	r, e = tool.Execute(map[string]any{"file": "file1.txt", "expected_sha256": hash})
	if e != nil || r.Success || !strings.Contains(r.Error, "scope changed") {
		t.Fatalf("mixed scopes: %+v %v", r, e)
	}
	r, e = tool.Execute(map[string]any{"file": "tracked.txt"})
	if e != nil || !r.Success || r.Data["clean"] != true || r.Data["file"] != "tracked.txt" || r.Data["status"] != "" {
		t.Fatalf("clean scope: %+v %v", r, e)
	}
	r, e = tool.Execute(map[string]any{"file": "file[1].txt", "offset": 1, "expected_sha256": hash})
	if e != nil || !r.Success || r.Data["returned"] != 0 || r.Data["clean"] != false || r.Data["truncated"] != false {
		t.Fatalf("empty final page is not clean: %+v %v", r, e)
	}
}
func TestGitStatusTool_RenameAndQuotedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix filename bytes")
	}
	dir := statusTestRepo(t)
	statusTestGit(t, dir, "mv", "--", "tracked.txt", "renamed\nname.txt")
	writeFile(t, filepath.Join(dir, "renamed\nname.txt"), "changed")
	for _, name := range []string{"a b.txt", "colon:tab\t.txt", "界.txt", "back\\slash.txt", string([]byte{0xff, 'x'})} {
		writeFile(t, filepath.Join(dir, name), "data")
	}
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	r, e := tool.Execute(map[string]any{})
	if e != nil || !r.Success || r.Data["count"] != 6 {
		t.Fatalf("result=%+v err=%v", r, e)
	}
	text := r.Data["status"].(string)
	for _, want := range []string{"RM tracked.txt -> \"renamed\\nname.txt\"\n", "?? \"a b.txt\"\n", "?? \"colon:tab\\t.txt\"\n", "?? 界.txt\n", "?? \"back\\\\slash.txt\"\n", "?? \"\\xffx\"\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.Count(text, "\n") != 6 {
		t.Fatal("filename injected a status line")
	}
}
func TestGitStatusTool_BoundedEncoding(t *testing.T) {
	dir := statusTestRepo(t)
	for i := 0; i < 150; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("%03d_%s.txt", i, strings.Repeat("n", 180))), "")
	}
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultGitStatusOutputBytes, 2048} {
		tool.SetMaxOutputBytes(budget)
		r, e := tool.Execute(map[string]any{"limit": 500})
		if e != nil || !r.Success || r.Data["count"] != 150 || r.Data["truncated"] != true || r.Data["returned"].(int) < 1 {
			t.Fatalf("result=%+v err=%v", r, e)
		}
		for _, format := range []bool{true, false} {
			data, e := toon.New(format).MarshalString(r)
			if e != nil || len(data) > budget {
				t.Fatalf("toon=%v bytes=%d err=%v", format, len(data), e)
			}
		}
	}
	tool.SetMaxOutputBytes(20)
	r, e := tool.Execute(map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "budget") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
func TestGitStatusTool_DoesNotRefreshIndex(t *testing.T) {
	dir := statusTestRepo(t)
	path := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "tracked.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	r, e := tool.Execute(map[string]any{})
	if e != nil || !r.Success || r.Data["clean"] != true {
		t.Fatalf("result=%+v err=%v", r, e)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("status rewrote the index")
	}
	statusTestGit(t, dir, "status", "--porcelain")
	control, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, control) {
		t.Fatal("fixture did not demonstrate an optional index refresh")
	}
}
func TestGitStatusTool_ParametersAndCancellation(t *testing.T) {
	dir := statusTestRepo(t)
	tool := &GitStatusTool{}
	tool.SetWorkDir(dir)
	for _, hash := range []any{nil, ""} {
		result, err := tool.Execute(map[string]any{"expected_sha256": hash, "offset": 0})
		if err != nil || !result.Success || result.Data["clean"] != true {
			t.Fatalf("first page hash=%v result=%+v err=%v", hash, result, err)
		}
	}
	for _, params := range []map[string]any{
		{"offset": -1}, {"offset": 1}, {"offset": 1.5}, {"limit": 0}, {"limit": 501}, {"expected_sha256": "bad"}, {"path": false}, {"repo_path": 1}, {"file": "../escape"},
	} {
		r, e := tool.Execute(params)
		if e != nil || r.Success || r.Error == "" {
			t.Fatalf("params=%+v result=%+v err=%v", params, r, e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := tool.ExecuteWithContext(ctx, map[string]any{})
	if e != nil || r.Success || !strings.Contains(r.Error, "canceled") {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
