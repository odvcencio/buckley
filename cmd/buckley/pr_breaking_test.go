package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitFiles commits the files on branch, creating it from main.
func commitFiles(t *testing.T, r prRepo, branch string, files map[string]string) {
	t.Helper()
	runGitIn(t, r.work, "checkout", "-q", "-b", branch, "main")
	for name, content := range files {
		if err := os.MkdirAll(filepath.Join(r.work, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, r.work, name, content)
		runGitIn(t, r.work, "add", name)
	}
	runGitIn(t, r.work, "commit", "-q", "-m", "change "+branch)
}

// The pull request follows the rule of the commit: a branch that changes only
// internal Go code and tests gets no Breaking Changes section.
func TestRunPRCommand_BreakingSectionNeedsAFileOutsideUsersCanReach(t *testing.T) {
	const raw = `{"action":"refactor","title":"port the lex automaton","summary":"Ports the automaton.","changes":["Move the DFA walk"],"breaking":true}`
	cases := []struct {
		name     string
		files    map[string]string
		wantKept bool
	}{
		{"internal code and tests only", map[string]string{"internal/ccore/lexer.go": "package ccore\n", "internal/ccore/lexer_test.go": "package ccore\n"}, false},
		{"an exported package too", map[string]string{"internal/ccore/lexer.go": "package ccore\n", "parser.go": "package p\n"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			commitFiles(t, r, "feature", tc.files)
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubPRModelWith(t, raw)

			var err error
			var out string
			stderr := captureStderr(t, func() {
				out = captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
			})
			if err != nil {
				t.Fatalf("runPRCommand: %v", err)
			}
			if kept := strings.Contains(out, "## Breaking Changes"); kept != tc.wantKept {
				t.Errorf("Breaking Changes section present = %v, want %v:\n%s", kept, tc.wantKept, out)
			}
			if warned := strings.Contains(stderr, "left out the Breaking Changes section"); warned == tc.wantKept {
				t.Errorf("stderr = %q, want a warning = %v", stderr, !tc.wantKept)
			}
		})
	}
}
