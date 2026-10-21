package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/buckley/internal/commandoutput"
)

func TestCreateCommit_BoundedHookDiagnostics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix Git hook fixture")
	}
	for _, exit := range []int{0, 1} {
		t.Run(fmt.Sprintf("hook_exit_%d", exit), func(t *testing.T) {
			repo := initTempGitRepo(t)
			t.Chdir(repo)
			if err := os.WriteFile("selected.txt", []byte("selected fixture\n"), 0640); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "--", "selected.txt")
			runGit(t, repo, "config", "core.hooksPath", ".git/hooks")
			head := gitOutputInDir(t, repo, "rev-parse", "HEAD")
			indexPath := filepath.Join(repo, ".git", "index")
			index, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			hook := fmt.Sprintf("#!/bin/sh\nprintf 'hook diagnostics begin\\n' >&2\nhead -c 2097152 /dev/zero | tr '\\000' x >&2\nprintf '\\nfatal: fixture hook declined\\n' >&2\nexit %d\n", exit)
			if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte(hook), 0755); err != nil {
				t.Fatal(err)
			}
			err = createCommit("add: selected fixture", true, false, []string{"selected.txt"})
			if exit != 0 {
				if err == nil || len(err.Error()) > commandoutput.DefaultCaptureBytes+128 || !strings.Contains(err.Error(), "hook diagnostics begin") || !strings.Contains(err.Error(), "fatal: fixture hook declined") || !strings.Contains(err.Error(), "output omitted") {
					text := ""
					if err != nil {
						text = err.Error()
					}
					t.Fatalf("lost bounded hook diagnostic: bytes=%d start=%q end=%q", len(text), text[:min(len(text), 128)], text[max(0, len(text)-128):])
				}
				if after := gitOutputInDir(t, repo, "rev-parse", "HEAD"); after != head {
					t.Fatal("failed hook changed HEAD")
				}
			} else {
				if err != nil || gitOutputInDir(t, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD") != "selected.txt" {
					t.Fatalf("successful hook blocked commit: %v", err)
				}
			}
			after, readErr := os.ReadFile(indexPath)
			if readErr != nil || !bytes.Equal(after, index) {
				t.Fatalf("original index changed: %v", readErr)
			}
			if _, statErr := os.Stat(filepath.Join(repo, ".git", "index.lock")); !os.IsNotExist(statErr) {
				t.Fatalf("index lock not cleaned up: %v", statErr)
			}
			errorBytes := 0
			if err != nil {
				errorBytes = len(err.Error())
			}
			t.Logf("native_git_hook_exit=%d emitted_stderr_bytes>2097152 error_bytes=%d final_diagnostic_preserved=true head_state_correct=true original_index_preserved=true index_lock_removed=true", exit, errorBytes)
		})
	}
}
