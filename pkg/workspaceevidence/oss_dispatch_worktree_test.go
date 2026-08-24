package workspaceevidence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOSSBlobRule_ClaimRequiresExactCleanWorktreeAtDispatch(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, string, string)
		restore func(*testing.T, string, string)
	}{
		{
			name: "changed HEAD",
			mutate: func(t *testing.T, root, _ string) {
				writeLicenseTestFile(t, root, "README.md", "new committed state\n")
				commitLicenseTestRepo(t, root, "move dispatch HEAD")
			},
			restore: func(t *testing.T, root, commit string) {
				gitLicenseTestRun(t, root, "checkout", "--quiet", "--detach", commit)
			},
		},
		{
			name: "tracked worktree change",
			mutate: func(t *testing.T, root, _ string) {
				writeLicenseTestFile(t, root, "task.md", "locally changed prompt\n")
			},
			restore: func(t *testing.T, root, _ string) {
				gitLicenseTestRun(t, root, "checkout", "--", "task.md")
			},
		},
		{
			name: "staged tracked change",
			mutate: func(t *testing.T, root, _ string) {
				writeLicenseTestFile(t, root, "task.md", "staged prompt change\n")
				gitLicenseTestRun(t, root, "add", "--", "task.md")
			},
			restore: func(t *testing.T, root, _ string) {
				gitLicenseTestRun(t, root, "reset", "--quiet", "HEAD", "--", "task.md")
				gitLicenseTestRun(t, root, "checkout", "--", "task.md")
			},
		},
		{
			name: "untracked worktree change",
			mutate: func(t *testing.T, root, _ string) {
				writeLicenseTestFile(t, root, "dispatch-scratch.txt", "untracked\n")
			},
			restore: func(t *testing.T, root, _ string) {
				if err := os.Remove(filepath.Join(root, "dispatch-scratch.txt")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := []byte("Exact dispatch prompt.\n")
			root, commit, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", prompt)
			rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
			if err != nil {
				t.Fatal(err)
			}

			tt.mutate(t, root, commit)
			if _, err := rule.ClaimForDispatch(t.Context(), gotPrompt); !errors.Is(err, ErrEvidenceStale) {
				t.Fatalf("dirty claim error = %v, want ErrEvidenceStale", err)
			}

			tt.restore(t, root, commit)
			if _, err := rule.ClaimForDispatch(t.Context(), gotPrompt); err != nil {
				t.Fatalf("claim after restoring exact clean state = %v; failed validation must not spend the rule", err)
			}
		})
	}
}

func TestOSSBlobRule_ClaimAllowsDifferentBranchAtSameCleanHEAD(t *testing.T) {
	prompt := []byte("Branch-independent dispatch prompt.\n")
	root, _, evidence := newOSSBlobRuleTestRepo(t, "LICENSE", mitTestLicense(), "task.md", prompt)
	rule, gotPrompt, err := MintTrackedPromptOSSBlobRule(t.Context(), evidence, "task.md")
	if err != nil {
		t.Fatal(err)
	}

	gitLicenseTestRun(t, root, "checkout", "--quiet", "-b", "same-head-dispatch")
	if _, err := rule.ClaimForDispatch(t.Context(), gotPrompt); err != nil {
		t.Fatalf("ClaimForDispatch() on a different branch at the bound clean HEAD = %v", err)
	}
}
