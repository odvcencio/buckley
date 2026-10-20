package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCommitCommandOptions_LiteralPaths(t *testing.T) {
	for _, name := range []string{" leading.txt", "trailing.txt ", "\tname.txt\n", "comma,a.txt", "   "} {
		opts, err := parseCommitCommandOptions([]string{"--paths", name, "--push=false"})
		if err != nil || !reflect.DeepEqual(opts.paths, []string{name}) {
			t.Fatalf("literal CLI scope=%q got=%q error=%v", name, opts.paths, err)
		}
	}
}

func TestCommitIndex_LiteralNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX filenames")
	}
	for _, tc := range []struct{ name, neighbor string }{
		{"trailing.txt ", "trailing.txt"}, {" leading.txt", "leading.txt"}, {"tab\tname.txt", "tabname.txt"}, {"line\nname.txt", "linename.txt"}, {"star*.txt", "star-other.txt"}, {"bracket[ab].txt", "bracketa.txt"}, {":(glob)*.txt", "other.txt"}, {"comma,a.txt", "comma.txt"}, {"--option.txt", "option.txt"}, {"   ", "other.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initTempGitRepo(t)
			t.Chdir(repo)
			for name, body := range map[string]string{tc.name: "selected\n", tc.neighbor: "unrelated\n"} {
				if err := os.WriteFile(name, []byte(body), 0640); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepareCommitIndex(commitCommandOptions{paths: []string{tc.name}, exclusive: true, compactOutput: true}); err != nil {
				t.Fatalf("literal staging failed: %v", err)
			}
			files, err := listStagedFiles()
			if err != nil || !reflect.DeepEqual(files, []string{tc.name}) {
				t.Fatalf("staged literal names: %q %v", files, err)
			}
			metadata, err := collectStagedChangeMetadata([]string{tc.name})
			if err != nil || metadata.Files != 1 {
				t.Fatalf("literal identity: %+v %v", metadata, err)
			}
			if err := createCommitWithMetadata("fix: preserve literal scope", true, false, []string{tc.name}, metadata, true); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("git", "show", "HEAD:"+tc.name)
			got, err := cmd.Output()
			if err != nil || string(got) != "selected\n" {
				t.Fatalf("wrong committed bytes: %q %v", got, err)
			}
			cmd = exec.Command("git", "ls-tree", "-r", "--name-only", "-z", "HEAD")
			tree, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range bytes.Split(tree, []byte{0}) {
				if string(name) == tc.neighbor {
					t.Fatalf("neighbor committed: %q", tree)
				}
			}
			got, err = os.ReadFile(tc.neighbor)
			if err != nil || string(got) != "unrelated\n" {
				t.Fatalf("neighbor changed: %q %v", got, err)
			}
			entries, err := filepath.Glob(filepath.Join(repo, ".git", "buckley-commit-index-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("scoped index artifacts: %v %v", entries, err)
			}
		})
	}
}

func TestCommitIndex_LiteralExclusiveGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX filenames")
	}
	repo := initTempGitRepo(t)
	t.Chdir(repo)
	for _, name := range []string{"selected.txt ", "unrelated\nfile.txt"} {
		if err := os.WriteFile(name, []byte(name), 0640); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "--literal-pathspecs", "add", "--", "unrelated\nfile.txt")
	before := gitOutputInDir(t, repo, "rev-parse", "HEAD")
	err := prepareCommitIndex(commitCommandOptions{paths: []string{"selected.txt "}, exclusive: true, compactOutput: true})
	if err == nil || !strings.Contains(err.Error(), "--exclusive") {
		t.Fatalf("unrelated staged scope escaped guard: %v", err)
	}
	if after := gitOutputInDir(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatal("guard changed HEAD")
	}
	files, err := listStagedFiles()
	if err != nil || len(files) != 2 {
		t.Fatalf("staged work lost: %q %v", files, err)
	}
}

func TestCommitIndex_LiteralPartialScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX filenames")
	}
	repo := initTempGitRepo(t)
	t.Chdir(repo)
	name := ":(glob)*.txt"
	for file, body := range map[string]string{name: "selected\n", "unrelated.txt": "staged neighbor\n"} {
		if err := os.WriteFile(file, []byte(body), 0640); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "--literal-pathspecs", "add", "--", name, "unrelated.txt")
	metadata, err := collectStagedChangeMetadata([]string{name})
	if err != nil || metadata.Files != 1 {
		t.Fatalf("scope included staged neighbor: %+v %v", metadata, err)
	}
	before := gitOutputInDir(t, repo, "ls-files", "--stage", "--", "unrelated.txt")
	if err := createCommitWithMetadata("fix: literal partial scope", true, false, []string{name}, metadata, true); err != nil {
		t.Fatal(err)
	}
	if after := gitOutputInDir(t, repo, "ls-files", "--stage", "--", "unrelated.txt"); after != before {
		t.Fatal("unrelated staged bytes changed")
	}
	files, err := listStagedFiles()
	if err != nil || !reflect.DeepEqual(files, []string{"unrelated.txt"}) {
		t.Fatalf("remaining staged names: %q %v", files, err)
	}
}

func TestCommitIndex_RepositoryScope(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Chdir(repo)
	if err := os.WriteFile("selected.txt", []byte("selected\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := prepareCommitIndex(commitCommandOptions{paths: []string{"."}, exclusive: true, compactOutput: true}); err != nil {
		t.Fatal(err)
	}
	if !fileMatchesPaths("selected.txt", []string{"./"}) || !fileMatchesPaths("dir/file.txt", []string{"./dir"}) {
		t.Fatal("canonical directory scope did not match")
	}
}
