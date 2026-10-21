package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScopedCommitSnapshot_SubdirectoryScope(t *testing.T) {
	for _, scope := range []string{"selected.txt", "."} {
		t.Run(scope, func(t *testing.T) {
			repo := initTempGitRepo(t)
			sub := filepath.Join(repo, "sub")
			if err := os.Mkdir(sub, 0755); err != nil {
				t.Fatal(err)
			}
			for path, body := range map[string]string{filepath.Join(sub, "selected.txt"): "selected\n", filepath.Join(repo, "outside.txt"): "unrelated\n"} {
				if err := os.WriteFile(path, []byte(body), 0640); err != nil {
					t.Fatal(err)
				}
			}
			runGit(t, repo, "add", "--", "sub/selected.txt", "outside.txt")
			t.Chdir(sub)
			original, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := collectStagedChangeMetadata([]string{scope})
			if err != nil {
				t.Fatal(err)
			}
			if err := createCommitWithMetadata("fix: scoped subdirectory fixture", true, false, []string{scope}, metadata, true); err != nil {
				t.Fatal(err)
			}
			names := gitOutputInDir(t, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
			if names != "sub/selected.txt" {
				t.Fatalf("committed paths outside scope: %q", names)
			}
			after, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
			if err != nil || !bytes.Equal(after, original) {
				t.Fatalf("original staging changed: %v", err)
			}
			want := []string{"../outside.txt"}
			files, err := listStagedFiles()
			if err != nil || !reflect.DeepEqual(files, want) {
				t.Fatalf("workdir-relative staged names: %q %v", files, err)
			}
			t.Logf("scope=%q expected_committed=sub/selected.txt actual_committed=%q expected_remaining=../outside.txt actual_remaining=%q original_index_preserved=true", scope, names, files)
		})
	}
}

func TestCommitIndex_SubdirectoryExclusive(t *testing.T) {
	repo := initTempGitRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(sub, "selected.txt"), filepath.Join(repo, "outside.txt")} {
		if err := os.WriteFile(path, []byte("staged fixture\n"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "--", "sub/selected.txt", "outside.txt")
	runGit(t, repo, "config", "diff.relative", "true")
	t.Chdir(sub)
	before := gitOutputInDir(t, repo, "rev-parse", "HEAD")
	err := prepareCommitIndex(commitCommandOptions{paths: []string{"."}, exclusive: true, compactOutput: true})
	if err == nil || !strings.Contains(err.Error(), "--exclusive") || !strings.Contains(err.Error(), "../outside.txt") {
		t.Fatalf("exclusive scope hid staged outsider: %v", err)
	}
	if after := gitOutputInDir(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatal("exclusive rejection changed HEAD")
	}
	rejection := err.Error()
	files, err := listStagedFiles()
	if err != nil || !reflect.DeepEqual(files, []string{"../outside.txt", "selected.txt"}) {
		t.Fatalf("staged entries lost or hidden: %q %v", files, err)
	}
	t.Logf("scope=. expected=reject_outside expected_head_change=false observed_error=%q remaining_staged=%q", rejection, files)
}

func TestScopedCommitSnapshot_SelectedEntryFlags(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Chdir(repo)
	for _, name := range []string{"selected.txt", "pending.txt", "outside.txt"} {
		if err := os.WriteFile(name, []byte("fixture\n"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "--", "selected.txt", "outside.txt")
	runGit(t, repo, "update-index", "--assume-unchanged", "--", "selected.txt")
	runGit(t, repo, "add", "-N", "--", "pending.txt")
	before := gitOutputInDir(t, repo, "ls-files", "--stage", "--debug", "--", "selected.txt", "pending.txt")
	head := gitOutputInDir(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "ORIG_HEAD", head)
	for _, key := range []string{"GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS"} {
		t.Setenv(key, "1")
	}
	env, cleanup, err := prepareScopedCommitIndex(context.Background(), []string{"selected.txt", "pending.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd := exec.Command("git", "ls-files", "--stage", "--debug", "--", "selected.txt", "pending.txt")
	cmd.Env = env
	after, err := cmd.Output()
	if err != nil || strings.TrimSuffix(string(after), "\n") != before {
		t.Fatalf("selected entry flags changed: %q %v", after, err)
	}
	cmd = exec.Command("git", "diff", "--cached", "--ita-invisible-in-index", "--name-only", "-z")
	cmd.Env = env
	names, err := cmd.Output()
	if err != nil || string(names) != "selected.txt\x00" {
		t.Fatalf("intent-to-add became staged or outsider remained: %q %v", names, err)
	}
	if got := gitOutputInDir(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatal("filter moved HEAD")
	}
	if got := gitOutputInDir(t, repo, "rev-parse", "ORIG_HEAD"); got != head {
		t.Fatal("filter moved ORIG_HEAD")
	}
}

func TestScopedCommitSnapshot_SplitIndexArtifactCleanup(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	t.Chdir(repo)
	runGit(t, repo, "config", "core.splitIndex", "true")
	runGit(t, repo, "config", "splitIndex.maxPercentChange", "0")
	runGit(t, repo, "update-index", "--split-index")
	before, err := filepath.Glob(filepath.Join(repo, ".git", "sharedindex.*"))
	if err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := prepareScopedCommitIndex(context.Background(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	after, err := filepath.Glob(filepath.Join(repo, ".git", "sharedindex.*"))
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("private snapshot left shared-index artifacts: before=%q after=%q error=%v", before, after, err)
	}
	metadata, err := collectStagedChangeMetadata([]string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := createCommitWithMetadata("fix: private split-index fixture", true, false, []string{"a"}, metadata, true); err != nil {
		t.Fatal(err)
	}
	after, err = filepath.Glob(filepath.Join(repo, ".git", "sharedindex.*"))
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("private commit left shared-index artifacts: before=%q after=%q error=%v", before, after, err)
	}
	if got := gitOutputInDir(t, repo, "config", "core.splitIndex"); got != "true" {
		t.Fatalf("repository split-index setting changed: %q", got)
	}
	t.Logf("expected_shared_index_files=%d observed_shared_index_files=%d repository_split_index=true", len(before), len(after))
}
