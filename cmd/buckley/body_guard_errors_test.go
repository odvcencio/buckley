package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedBodyTemplateLoadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		large         bool
	}{
		{"lookup error", "ls-tree", false},
		{"read error", "show", false},
		{"oversize", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			template := testPRTemplate
			if tc.large {
				template = "## Too large\n" + strings.Repeat("a", prTemplateMaxBytes)
			}
			commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", template)
			runGitIn(t, r.work, "push", "-q", "origin", "main")
			r.commit(t, "feature", "f.txt")
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubGh(t)
			installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
			file := filepath.Join(t.TempDir(), "body.md")
			if err := os.WriteFile(file, []byte("Title: test\n\n"+testPRTemplate), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.failure != "" {
				git, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				wrapper := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --no-pager ] && [ \"$2\" = %s ]; then exit 98; fi\nexec %q \"$@\"\n", tc.failure, git)
				if err := os.WriteFile(filepath.Join(dir, "git"), []byte(wrapper), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			var err error
			captureStdout(t, func() { err = runPRCommand([]string{"--yes", "--head", "feature", "--body-file", file}) })
			expect := "exceeds"
			if tc.failure == "ls-tree" {
				expect = "inspect template"
			}
			if tc.failure == "show" {
				expect = "read template"
			}
			if err == nil || !strings.Contains(err.Error(), expect) {
				t.Fatalf("error = %v; want %q", err, expect)
			}
		})
	}
}

func TestSavedBodyTemplateMissingRefFailsClosed(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	plan := &prPlan{Base: prBase{Ref: "refs/heads/no-such-template-ref"}, Head: prHead{Ref: "refs/heads/feature"}}
	tmpl, err := loadPRTemplateForBody(plan)
	if err == nil || tmpl != nil || !strings.Contains(err.Error(), "inspect template") {
		t.Fatalf("template=%v err=%v", tmpl, err)
	}
}

func TestSavedBodyHeadinglessTemplateOutputRoundTrip(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "- [ ] Required check\n")
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	stubPRModel(t)
	out := filepath.Join(t.TempDir(), "generated.md")
	var err error
	captureStdout(t, func() {
		err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "--output", out})
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	original, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), "- [ ] Required check") {
		t.Fatalf("output omitted checkbox: %s", original)
	}
	captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--head", "feature", "--body-file", out}) })
	if err != nil {
		t.Fatalf("read generated output: %v\n%s", err, original)
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("body file changed")
	}
}

func TestSavedBodyRootTemplateFromSubdirectory(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", testPRTemplate)
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	dir := filepath.Join(r.work, "nested")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(file, []byte("Title: test\n\nwrong\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() {
		err = runPRCommand([]string{"--dry-run", "--head", "feature", "--dir", dir, "--body-file", file})
	})
	if err == nil || !strings.Contains(err.Error(), "missing template heading") {
		t.Fatalf("root template not enforced: %v", err)
	}
}

func TestSavedBodyTemplateRejectsBeforeWrite(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", testPRTemplate)
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	file := filepath.Join(t.TempDir(), "body.md")
	out := filepath.Join(t.TempDir(), "out.md")
	if err := os.WriteFile(file, []byte("Title: test\n\nwrong body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() {
		err = runPRCommand([]string{"--yes", "--head", "feature", "--body-file", file, "--output", out})
	})
	if err == nil {
		t.Fatal("invalid body accepted")
	}
	for _, call := range g.calls(t) {
		if len(call) >= 2 && call[0] == "pr" && (call[1] == "create" || call[1] == "edit") {
			t.Fatalf("unexpected gh write: %v", call)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("unexpected output file: %v", err)
	}
	refs, err := gitOutput("ls-remote", "origin", "refs/heads/feature")
	if err != nil || refs != "" {
		t.Fatalf("unexpected push: refs=%q err=%v", refs, err)
	}
}

func TestTemplateFalseOutputRepostHint(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	stubPRModel(t)
	out := filepath.Join(t.TempDir(), "output.md")
	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			err = runPRCommand([]string{"--dry-run", "--head", "feature", "--template=false", "--output", out})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "--template=false --body-file "+out) {
		t.Fatalf("incorrect repost hint: %s", stderr)
	}
}

func TestSavedBodyMixedPreambleOutputRoundTrip(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "- [ ] Preamble\n\n## Gate\n- [ ] Required\n")
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	stubPRModel(t)
	out := filepath.Join(t.TempDir(), "generated.md")
	var err error
	captureStdout(t, func() {
		err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "--output", out})
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"- [ ] Preamble", "## Gate", "- [ ] Required"} {
		if !strings.Contains(string(before), s) {
			t.Fatalf("missing %q in %s", s, before)
		}
	}
	captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--head", "feature", "--body-file", out}) })
	if err != nil {
		t.Fatalf("round-trip: %v\n%s", err, before)
	}
	after, err := os.ReadFile(out)
	if err != nil || string(before) != string(after) {
		t.Fatalf("file changed: %v", err)
	}
}

func TestSavedBodyDuplicateGeneratedHeadingsRoundTrip(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "## Summary\n- [ ] Required summary check\n\n## Testing\n- [ ] Required testing check\n")
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	stubPRModel(t)
	out := filepath.Join(t.TempDir(), "generated.md")
	var err error
	captureStdout(t, func() {
		err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "--output", out})
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"- [ ] Required summary check", "- [ ] Required testing check"} {
		if !strings.Contains(string(body), s) {
			t.Fatalf("missing %q in %s", s, body)
		}
	}
	captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--head", "feature", "--body-file", out}) })
	if err != nil {
		t.Fatalf("round-trip: %v\n%s", err, body)
	}
}
