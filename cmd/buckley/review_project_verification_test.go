package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestProjectReview_RequiredVerificationStopsBeforeModel(t *testing.T) {
	newVerifyRepo(t)
	runner := &retentionShardRunner{}
	framework := oneshot.NewFramework(nil, nil).WithAgentRunner(runner)
	_, err := runProjectReviewWithPolicy(context.Background(), framework, verifyTestOpts("receipt"))
	if err == nil || exitCodeForError(err) != commands.VerificationGateMissing || runner.calls.Load() != 0 {
		t.Fatalf("project verification gate bypassed: err=%v calls=%d", err, runner.calls.Load())
	}
}

func TestReview_ReceiptMatchesCleanCombinedScope(t *testing.T) {
	for _, mode := range []string{"project", "changes", "dirty", "untracked"} {
		t.Run(mode, func(t *testing.T) {
			repo := newVerifyRepo(t)
			base, err := exec.Command("git", "rev-parse", "HEAD").Output()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "client/a.js"), []byte("new source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "change"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			if err := recordAndPrint(repo, []string{"sh", "-c", "exit 0"}, []string{"client/**", "scene/**"}, "all", "record"); err != nil {
				t.Fatal(err)
			}
			if mode == "dirty" || mode == "untracked" {
				path := "client/a.js"
				if mode == "untracked" {
					path = "client/new.js"
				}
				if err := os.WriteFile(filepath.Join(repo, path), []byte("uncommitted\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &retentionShardRunner{}
			framework := oneshot.NewFramework(nil, nil).WithAgentRunner(runner)
			policy := verifyTestOpts("receipt")
			if mode == "project" {
				_, err = runProjectReviewWithPolicy(context.Background(), framework, policy)
			} else {
				_, err = runBranchReviewWithPolicy(context.Background(), reviewCommandOptions{scope: commands.ReviewScopeChanges, baseBranch: strings.TrimSpace(string(base)), includeUnstaged: true}, framework, policy)
			}
			if mode == "dirty" || mode == "untracked" {
				if exitCodeForError(err) != commands.VerificationGateMissing || runner.calls.Load() != 0 {
					t.Fatalf("uncommitted state used HEAD receipt: %v, calls %d", err, runner.calls.Load())
				}
			} else if exitCodeForError(err) == commands.VerificationGateMissing || runner.calls.Load() == 0 {
				t.Fatalf("clean HEAD receipt did not reach reviewer: %v, calls %d", err, runner.calls.Load())
			}
		})
	}
}
