package builtin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func TestGitBlameTool_PagingWorkingTreeAndChangedEvidence(t *testing.T) {
	dir := statusTestRepo(t)
	file := filepath.Join(dir, "source.txt")
	writeFile(t, file, "first\nsecond\nthird\nfourth\n")
	statusTestGit(t, dir, "add", "source.txt")
	statusTestGit(t, dir, "commit", "-qm", "Add source")
	initial := strings.TrimSpace(statusTestGit(t, dir, "rev-parse", "HEAD"))
	writeFile(t, file, "updated\nsecond\nthird\nfourth\n")
	statusTestGit(t, dir, "commit", "-qam", "Update first line")
	updated := strings.TrimSpace(statusTestGit(t, dir, "rev-parse", "HEAD"))
	writeFile(t, file, "updated\nsecond\nthird\nlocal change\n")
	tool := &GitBlameTool{}
	tool.SetWorkDir(dir)
	params := map[string]any{"file": "source.txt", "limit": 2}
	var evidence strings.Builder
	fingerprint := ""
	for page := 0; page < 3; page++ {
		r, e := tool.Execute(params)
		if e != nil || !r.Success || r.Data["count"] != 2 || r.Data["total_lines"] != int64(4) {
			t.Fatalf("page=%d result=%+v err=%v", page, r, e)
		}
		evidence.WriteString(r.Data["blame"].(string))
		if page == 0 {
			fingerprint = r.Data["blame_sha256"].(string)
		} else if r.Data["blame_sha256"] != fingerprint {
			t.Fatal("unchanged blame fingerprint moved")
		}
		if !r.Data["has_more"].(bool) {
			break
		}
		params = map[string]any{"file": "source.txt", "limit": 2, "start_line": r.Data["next_start_line"], "expected_sha256": fingerprint}
	}
	for _, want := range []string{initial, updated, strings.Repeat("0", len(initial)), "local change", "\"Test\""} {
		if !strings.Contains(evidence.String(), want) {
			t.Fatalf("missing %q in %q", want, evidence.String())
		}
	}
	writeFile(t, file, "updated\nsecond\nchanged third\nlocal change\n")
	r, e := tool.Execute(params)
	if e != nil || r.Success || !strings.Contains(r.Error, "changed") {
		t.Fatalf("changed source accepted: %+v %v", r, e)
	}
	r, e = tool.Execute(map[string]any{"path": "source.txt", "start_line": 3, "limit": 1})
	if e != nil || !r.Success || r.Data["start_line"] != int64(3) || r.Data["end_line"] != int64(3) {
		t.Fatalf("direct line query: %+v %v", r, e)
	}
}

func TestGitBlameTool_BoundedPagesAndLiteralNames(t *testing.T) {
	dir := statusTestRepo(t)
	name := "-L1,1"
	var content strings.Builder
	for i := 0; i < 130; i++ {
		fmt.Fprintf(&content, "%d %s\n", i, strings.Repeat("界", 300))
	}
	writeFile(t, filepath.Join(dir, name), content.String())
	statusTestGit(t, dir, "add", "--", name)
	statusTestGit(t, dir, "commit", "-qm", "Literal filename")
	tool := &GitBlameTool{}
	tool.SetWorkDir(dir)
	for _, budget := range []int{DefaultGitBlameOutputBytes, 4096} {
		tool.SetMaxOutputBytes(budget)
		params := map[string]any{"file": name, "limit": 100}
		returned := 0
		for page := 0; page < 100; page++ {
			r, e := tool.Execute(params)
			if e != nil || !r.Success || r.Data["count"].(int) < 1 || r.Data["total_lines"] != int64(130) {
				t.Fatalf("result=%+v err=%v", r, e)
			}
			for _, useToon := range []bool{true, false} {
				wire, e := toon.New(useToon).MarshalString(r)
				if e != nil || len(wire) > budget {
					t.Fatalf("encoding=%v len=%d err=%v", useToon, len(wire), e)
				}
			}
			count := r.Data["count"].(int)
			if len(r.Data["truncated_lines"].([]int64)) != count || r.Data["start_line"] != int64(returned+1) {
				t.Fatalf("unmarked clip or skipped page: %+v", r.Data)
			}
			returned += count
			if !r.Data["has_more"].(bool) {
				break
			}
			params = map[string]any{"file": name, "limit": 100, "start_line": r.Data["next_start_line"], "expected_sha256": r.Data["blame_sha256"]}
		}
		if returned != 130 {
			t.Fatalf("returned %d lines, want 130", returned)
		}
	}
}

func TestGitBlameTool_ValidationEmptyAndCancellation(t *testing.T) {
	dir := statusTestRepo(t)
	writeFile(t, filepath.Join(dir, "empty"), "")
	statusTestGit(t, dir, "add", "empty")
	statusTestGit(t, dir, "commit", "-qm", "Empty file")
	tool := &GitBlameTool{}
	tool.SetWorkDir(dir)
	r, e := tool.Execute(map[string]any{"file": "empty", "expected_sha256": "", "start_line": nil})
	if e != nil || !r.Success || r.Data["count"] != 0 || r.Data["has_more"] != false {
		t.Fatalf("empty file: %+v %v", r, e)
	}
	for _, extra := range []map[string]any{{"limit": 0}, {"limit": 101}, {"limit": 1.5}, {"start_line": 0}, {"start_line": 999}, {"expected_sha256": "bad"}, {"expected_sha256": false}, {"repo_path": false}, {"file": "../escape"}, {"file": false}, {"file": "missing"}} {
		params := map[string]any{"file": "tracked.txt"}
		for k, v := range extra {
			params[k] = v
		}
		r, e := tool.Execute(params)
		if e != nil || r.Success || r.Error == "" {
			t.Fatalf("params=%v result=%+v err=%v", params, r, e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e = tool.ExecuteWithContext(ctx, map[string]any{"file": "tracked.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "canceled") {
		t.Fatalf("cancellation: %+v %v", r, e)
	}
	tool.SetMaxOutputBytes(20)
	r, e = tool.Execute(map[string]any{"file": "tracked.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "budget") {
		t.Fatalf("tiny budget: %+v %v", r, e)
	}
}

func TestGitBlameTool_NormalizesOnlyUncommittedClocks(t *testing.T) {
	fingerprint := func(input string, zero bool) string {
		h := sha256.New()
		r := bufio.NewReaderSize(strings.NewReader(input), 4096)
		for {
			_, _, e := gitBlameReadLine(r, h, zero)
			if e != nil {
				break
			}
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	a := "author-time 1000\ncommitter-time 1000\n\tauthor-time 1000\n"
	b := "author-time 2000\ncommitter-time 2000\n\tauthor-time 1000\n"
	if fingerprint(a, true) != fingerprint(b, true) || fingerprint(a, false) == fingerprint(b, false) {
		t.Fatal("uncommitted clocks should normalize; committed clocks should not")
	}
	if fingerprint(a, true) == fingerprint(strings.ReplaceAll(a, "\tauthor-time 1000", "\tauthor-time 2000"), true) {
		t.Fatal("source text resembling metadata was normalized")
	}
}

func TestGitBlameTool_MalformedRecordsAndBudgets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := statusTestRepo(t)
	bin := t.TempDir()
	payload := filepath.Join(t.TempDir(), "payload")
	script := "#!/bin/sh\n/bin/cat \"$BLAME_TEST_PAYLOAD\"\n"
	fake := filepath.Join(bin, "git")
	writeFile(t, fake, script)
	if e := os.Chmod(fake, 0755); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin)
	t.Setenv("BLAME_TEST_PAYLOAD", payload)
	tool := &GitBlameTool{}
	tool.SetWorkDir(dir)
	header := strings.Repeat("a", 40) + " 1 1 1\n"
	for _, input := range []string{"bad\n", header, header + "author Test\n\ttext\n", header + "author Test\nauthor-time bad\n\ttext\n", strings.Replace(header, " 1 1 1", " 1 3 1", 1), strings.Repeat("x", 8192) + "\n"} {
		writeFile(t, payload, input)
		r, e := tool.Execute(map[string]any{"file": "tracked.txt"})
		if e != nil || r.Success || !strings.Contains(r.Error, "parsed safely") {
			t.Fatalf("input len=%d result=%+v err=%v", len(input), r, e)
		}
	}
	writeFile(t, payload, header+"author "+strings.Repeat("A", 2000)+"\nauthor-time 1000\nsummary ignored\n\tbefore\x00after\n")
	r, e := tool.Execute(map[string]any{"file": "tracked.txt"})
	if e != nil || !r.Success || len(r.Data["truncated_authors"].([]int64)) != 1 || !strings.Contains(r.Data["blame"].(string), `\x00`) {
		t.Fatalf("quoted content/author clip: %+v %v", r, e)
	}
	writeFile(t, fake, "#!/bin/sh\n/bin/cat \"$BLAME_TEST_PAYLOAD\"\n/usr/bin/head -c 33554432 /dev/zero\n")
	writeFile(t, payload, header+"author Test\nauthor-time 1000\n\t")
	r, e = tool.Execute(map[string]any{"file": "tracked.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "scan budget") {
		t.Fatalf("scan budget: %+v %v", r, e)
	}
	writeFile(t, fake, "#!/bin/sh\n/bin/sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, e = tool.ExecuteWithContext(ctx, map[string]any{"file": "tracked.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "deadline") {
		t.Fatalf("stream cancellation: %+v %v", r, e)
	}
}
