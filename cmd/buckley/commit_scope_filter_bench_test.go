package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkScopedCommitSnapshot_OutsideChanges(b *testing.B) {
	for _, outside := range []int{0, 20000} {
		b.Run(fmt.Sprintf("outside_%d", outside), func(b *testing.B) {
			repo := b.TempDir()
			b.Chdir(repo)
			git := func(input string, args ...string) []byte {
				b.Helper()
				cmd := exec.Command("git", args...)
				cmd.Stdin = strings.NewReader(input)
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com")
				output, err := cmd.CombinedOutput()
				if err != nil {
					b.Fatalf("git %v: %v\n%s", args, err, output)
				}
				return bytes.TrimSuffix(output, []byte{'\n'})
			}
			git("", "init", "-q")
			git("", "config", "gc.auto", "0")
			git("", "config", "maintenance.auto", "false")
			tree := string(git("", "mktree"))
			head := string(git("initial fixture\n", "commit-tree", tree))
			git("", "update-ref", "HEAD", head)
			blob := string(git("unrelated staged fixture\n", "hash-object", "-w", "--stdin"))
			var entries strings.Builder
			for i := 0; i < outside; i++ {
				fmt.Fprintf(&entries, "100644 %s\toutside/file-%08d.txt\n", blob, i)
			}
			fmt.Fprintf(&entries, "100644 %s\tselected.txt\n", blob)
			git(entries.String(), "update-index", "--index-info")
			indexPath := filepath.Join(repo, ".git", "index")
			original, err := os.ReadFile(indexPath)
			if err != nil {
				b.Fatal(err)
			}
			env, cleanup, err := prepareScopedCommitIndex(context.Background(), []string{"selected.txt"})
			if err != nil {
				b.Fatal(err)
			}
			check := exec.Command("git", "diff", "--cached", "--name-only", "-z")
			check.Env = env
			names, err := check.Output()
			cleanup()
			if err != nil || string(names) != "selected.txt\x00" {
				b.Fatalf("snapshot included unrelated staged paths: %q %v", names, err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, cleanup, err := prepareScopedCommitIndex(context.Background(), []string{"selected.txt"})
				if err != nil {
					b.Fatal(err)
				}
				cleanup()
			}
			b.StopTimer()
			after, err := os.ReadFile(indexPath)
			if err != nil || !bytes.Equal(after, original) {
				b.Fatalf("original index changed: %v", err)
			}
			snapshots, err := filepath.Glob(filepath.Join(repo, ".git", "buckley-commit-index-*"))
			if err != nil || len(snapshots) != 0 {
				b.Fatalf("snapshot artifacts: %v %v", snapshots, err)
			}
			if _, err := os.Stat(indexPath + ".lock"); !os.IsNotExist(err) {
				b.Fatalf("owned index lock remains: %v", err)
			}
		})
	}
}
