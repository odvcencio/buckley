package builtin

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func resolutionTestRepo(t *testing.T, name, attrs, style string) string {
	t.Helper()
	dir := statusTestRepo(t)
	if attrs != "" {
		writeFile(t, filepath.Join(dir, ".gitattributes"), attrs)
		statusTestGit(t, dir, "add", ".gitattributes")
	}
	if style != "" {
		statusTestGit(t, dir, "config", "merge.conflictStyle", style)
	}
	for _, file := range []string{name, "other.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte("base\n"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	statusTestGit(t, dir, "add", "--", ".")
	statusTestGit(t, dir, "commit", "-qm", "Base conflict files")
	branch := strings.TrimSpace(statusTestGit(t, dir, "branch", "--show-current"))
	statusTestGit(t, dir, "checkout", "-qb", "conflict-side")
	for _, file := range []string{name, "other.txt"} {
		writeFile(t, filepath.Join(dir, file), "theirs\n")
	}
	statusTestGit(t, dir, "commit", "-qam", "Side change")
	statusTestGit(t, dir, "checkout", "-q", branch)
	for _, file := range []string{name, "other.txt"} {
		writeFile(t, filepath.Join(dir, file), "ours\n")
	}
	statusTestGit(t, dir, "commit", "-qam", "Main change")
	cmd := exec.Command("git", "merge", "--no-edit", "conflict-side")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf("merge should conflict: %v %s", err, output)
	}
	return dir
}

func resolutionTestIndex(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMarkResolvedTool_RejectsMarkersAndStagesOnlyLiteralFile(t *testing.T) {
	for _, tc := range []struct{ name, attrs, style string }{
		{name: "--all"},
		{name: "[one].txt", style: "diff3"},
		{name: " trailing ", style: "zdiff3"},
		{name: ":(glob)*"},
		{name: "small-markers.txt", attrs: "* conflict-marker-size=3\n"},
		{name: "binary-marked.txt", attrs: "* -diff\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.ContainsAny(tc.name, ":*") {
				t.Skip("filename unsupported on Windows")
			}
			if runtime.GOOS == "windows" && strings.TrimSpace(tc.name) != tc.name {
				t.Skip("trailing-space filenames unsupported on Windows")
			}
			dir := resolutionTestRepo(t, tc.name, tc.attrs, tc.style)
			file := filepath.Join(dir, tc.name)
			if tc.attrs == "* -diff\n" {
				writeFile(t, file, "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> side\n")
			}
			if err := os.Chmod(file, 0640); err != nil {
				t.Fatal(err)
			}
			tool := &MarkResolvedTool{}
			tool.SetWorkDir(dir)
			index := resolutionTestIndex(t, dir)
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			r, e := tool.Execute(map[string]any{"path": tc.name})
			if e != nil || r.Success || !strings.Contains(r.Error, "validation failed") {
				t.Fatalf("unresolved file staged: %+v %v", r, e)
			}
			if !bytes.Equal(index, resolutionTestIndex(t, dir)) {
				t.Fatal("rejected resolution changed the index")
			}
			after, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("rejected resolution changed the source")
			}
			// Whitespace policy must not prevent a deliberate resolution.
			resolved := "resolved \t \n  \n"
			writeFile(t, file, resolved)
			writeFile(t, filepath.Join(dir, "unrelated.txt"), "leave unstaged\n")
			r, e = tool.ExecuteWithContext(context.Background(), map[string]any{"path": tc.name})
			if e != nil || !r.Success || r.Data["path"] != tc.name || r.Data["staged"] != true {
				t.Fatalf("resolved file rejected: %+v %v", r, e)
			}
			if got := statusTestGit(t, dir, "show", ":./"+tc.name); got != resolved {
				t.Fatalf("staged source=%q want=%q", got, resolved)
			}
			if got := statusTestGit(t, dir, "diff", "--name-only", "--diff-filter=U", "-z"); got != "other.txt\x00" {
				t.Fatalf("other conflicts changed: %q", got)
			}
			if got := statusTestGit(t, dir, "ls-files", "--", "unrelated.txt"); got != "" {
				t.Fatal("unrelated file staged")
			}
			info, err := os.Stat(file)
			if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0640) {
				t.Fatal("source mode changed")
			}
		})
	}
}

func TestMarkResolvedTool_ResolvedDeletion(t *testing.T) {
	dir := resolutionTestRepo(t, "deleted.txt", "", "")
	if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	tool := &MarkResolvedTool{}
	tool.SetWorkDir(dir)
	r, e := tool.Execute(map[string]any{"path": "deleted.txt"})
	if e != nil || !r.Success {
		t.Fatalf("deletion resolution: %+v %v", r, e)
	}
	if got := statusTestGit(t, dir, "ls-files", "--", "deleted.txt"); got != "" {
		t.Fatalf("deleted path remains indexed: %q", got)
	}
}

func TestMarkResolvedTool_ValidationAndBudgetsPreserveIndex(t *testing.T) {
	dir := resolutionTestRepo(t, "source.txt", "", "")
	tool := &MarkResolvedTool{}
	tool.SetWorkDir(dir)
	index := resolutionTestIndex(t, dir)
	for _, params := range []map[string]any{{}, {"path": ""}, {"path": false}, {"path": "../escape"}, {"path": "."}, {"path": "tracked.txt"}, {"path": "missing"}, {"path": "bad\x00name"}} {
		r, e := tool.Execute(params)
		if e != nil || r.Success || r.Error == "" || !bytes.Equal(index, resolutionTestIndex(t, dir)) {
			t.Fatalf("params=%v result=%+v err=%v", params, r, e)
		}
	}
	tool.SetMaxFileSizeBytes(1)
	r, e := tool.Execute(map[string]any{"path": "source.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "too large") {
		t.Fatalf("file budget: %+v %v", r, e)
	}
	tool.SetMaxFileSizeBytes(0)
	tool.SetMaxOutputBytes(20)
	r, e = tool.Execute(map[string]any{"path": "source.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "output budget") {
		t.Fatalf("result budget: %+v %v", r, e)
	}
	tool.SetMaxOutputBytes(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e = tool.ExecuteWithContext(ctx, map[string]any{"path": "source.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "canceled") {
		t.Fatalf("canceled: %+v %v", r, e)
	}
	if !bytes.Equal(index, resolutionTestIndex(t, dir)) {
		t.Fatal("failed preflight changed index")
	}
}

func TestMarkResolvedTool_BoundedErrorsAndProcessCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := resolutionTestRepo(t, "source.txt", "", "")
	bin := t.TempDir()
	fake := filepath.Join(bin, "git")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\ncase \"$arg\" in\nls-files) printf 'source.txt\\0'; exit 0;;\ncheck-attr) /usr/bin/head -c 33554432 /dev/zero; exit 2;;\nadd) exit 99;;\nesac\ndone\nexit 98\n"
	writeFile(t, fake, script)
	if err := os.Chmod(fake, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	tool := &MarkResolvedTool{}
	tool.SetWorkDir(dir)
	index := resolutionTestIndex(t, dir)
	r, e := tool.Execute(map[string]any{"path": "source.txt"})
	if e != nil || r.Success || len(r.Error) > DefaultMarkResolvedOutputBytes {
		t.Fatalf("bounded error: len=%d result=%+v err=%v", len(r.Error), r, e)
	}
	writeFile(t, fake, "#!/bin/sh\n/bin/sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, e = tool.ExecuteWithContext(ctx, map[string]any{"path": "source.txt"})
	if e != nil || r.Success || !strings.Contains(r.Error, "deadline") {
		t.Fatalf("stream cancellation: %+v %v", r, e)
	}
	if !bytes.Equal(index, resolutionTestIndex(t, dir)) {
		t.Fatal("failed command changed index")
	}
}

func TestCheckResolutionFile_ChunkBoundariesAndMarkerRules(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		size          int64
		bad           bool
	}{
		{"opening", "<<<<<<< HEAD\n", 7, true},
		{"closing at EOF", ">>>>>>> branch", 7, true},
		{"separator", "=======\n", 7, true},
		{"base", "||||||| base\r\n", 7, true},
		{"binary before marker", "binary\x00data\n<<<<<<< HEAD\n", 7, true},
		{"crosses chunk", strings.Repeat("x", (32<<10)-3) + "\n<<<<<<< HEAD\n", 7, true},
		{"large marker", strings.Repeat("<", (32<<10)+3) + " HEAD\n", (32 << 10) + 3, true},
		{"long ordinary line", strings.Repeat("x", 1<<20) + "\n", 7, false},
		{"inline operator", "text <<<<<<< value\n", 7, false},
		{"different marker size", "========\n", 7, false},
		{"ordinary whitespace", "text \t \n\n", 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			writeFile(t, path, tc.content)
			err := checkResolutionFile(context.Background(), path, DefaultMutationFileBytes, tc.size)
			if (err != nil) != tc.bad {
				t.Fatalf("validation error=%v want error=%v", err, tc.bad)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "source")
	writeFile(t, path, "complete")
	if err := checkResolutionFile(context.Background(), path, 8, 7); err != nil {
		t.Fatalf("exact byte budget: %v", err)
	}
	if err := checkResolutionFile(context.Background(), path, 7, 7); err == nil {
		t.Fatal("file exceeding byte budget accepted")
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(filepath.Dir(path), "target")
		writeFile(t, target, "<<<<<<< HEAD\n")
		link := filepath.Join(filepath.Dir(path), "link")
		if err := os.Symlink("target", link); err != nil {
			t.Fatal(err)
		}
		if err := checkResolutionFile(context.Background(), link, DefaultMutationFileBytes, 7); err != nil {
			t.Fatalf("symlink target content must not replace link blob content: %v", err)
		}
	}
}
