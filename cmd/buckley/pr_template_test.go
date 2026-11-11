package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

const testPRTemplate = `## What does this PR do?

<!-- One or two sentences. State the outcome, not a commit diary. -->

## Correctness

- [ ] Focused package or unit tests ran for the touched behavior
- [ ] Documented anything intentionally left unvalidated in this PR
`

// commitTemplate commits text as the pull request template at path on the
// checked-out branch.
func commitTemplate(t *testing.T, r prRepo, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(r.work, filepath.Dir(path)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.work, path, text)
	runGitIn(t, r.work, "add", path)
	runGitIn(t, r.work, "commit", "-q", "-m", "add the pull request template")
}

// toolRecordingInvoker answers raw and keeps the tool it was offered.
type toolRecordingInvoker struct {
	raw  json.RawMessage
	tool *tools.Definition
}

func (f toolRecordingInvoker) Invoke(_ context.Context, _, _ string, tool tools.Definition, _ *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	*f.tool = tool
	return &oneshot.Result{ToolCall: &tools.ToolCall{Arguments: f.raw}}, nil, nil
}

func stubPRModelRecordingTool(t *testing.T, raw string) *tools.Definition {
	t.Helper()
	t.Setenv("BUCKLEY_PROMPT_PR", "")
	t.Setenv("BUCKLEY_PROMPT_PR_FILE", "")
	tool := new(tools.Definition)
	previous := newPRCommandRuntimeFn
	t.Cleanup(func() { newPRCommandRuntimeFn = previous })
	newPRCommandRuntimeFn = func(prCommandOptions) (*prCommandRuntime, func(), error) {
		return &prCommandRuntime{
			backend:   oneshotBackendAPI,
			modelID:   "test-model",
			ledger:    transparency.NewCostLedger(),
			framework: oneshot.NewFramework(toolRecordingInvoker{raw: json.RawMessage(raw), tool: tool}, nil),
		}, func() {}, nil
	}
	return tool
}

// The gotreesitter repository has a pull request template with sections and
// checkboxes, and buckley pr wrote Summary, Changes and Testing instead.
func TestRunPRCommand_WritesTheBodyInTheRepositoryTemplate(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", testPRTemplate)
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	tool := stubPRModelRecordingTool(t, `{"action":"add","title":"feature file","section_what_does_this_pr_do":"Adds the feature file."}`)

	var err error
	captureStdout(t, func() { err = runPRCommand([]string{"--yes", "--cost=false", "--head", "feature"}) })
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}

	body := ghBodyArg(t, g, "pr create")
	want := "## What does this PR do?\n\nAdds the feature file.\n\n" +
		"## Correctness\n\n" +
		"- [ ] Focused package or unit tests ran for the touched behavior\n" +
		"- [ ] Documented anything intentionally left unvalidated in this PR\n\n" +
		"## Not verified\n\nThe author supplied no notes. Buckley wrote this text from the commits and the diff, and ran nothing."
	if body != want {
		t.Fatalf("the body gh received =\n%s\n\nwant\n%s", body, want)
	}
	if _, ok := tool.Parameters.Properties["section_what_does_this_pr_do"]; !ok {
		t.Errorf("the model was not asked for the template's open section: %v", tool.Parameters.Required)
	}
	if _, ok := tool.Parameters.Properties["summary"]; ok {
		t.Error("the model was asked for a summary although the template sets the body")
	}
}

func TestRunPRCommand_TemplateFalseKeepsBuckleysSections(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", testPRTemplate)
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)

	var err error
	out := captureStdout(t, func() {
		err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "--template=false"})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(out, "## Summary\n\nAdds the feature file.") || strings.Contains(out, "What does this PR do?") {
		t.Errorf("--template=false must keep Buckley's own sections:\n%s", out)
	}
}

func TestLoadPRTemplate(t *testing.T) {
	t.Run("the base branch's template wins", func(t *testing.T) {
		r := newPRRepo(t)
		commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "## Base\n\n<!-- from the base -->\n")
		runGitIn(t, r.work, "push", "-q", "origin", "main")
		r.commit(t, "feature", "f.txt")
		commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "## Head\n\n<!-- from the head -->\n")
		r.park(t)

		plan, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, nil)
		if err != nil {
			t.Fatalf("preflightPR: %v", err)
		}
		tmpl := loadPRTemplate(plan)
		if tmpl == nil || len(tmpl.Sections) != 1 || tmpl.Sections[0].Heading != "Base" {
			t.Fatalf("template = %+v, want the base branch's", tmpl)
		}
	})

	t.Run("a template that only the head has, in the lower-case spelling", func(t *testing.T) {
		r := newPRRepo(t)
		r.commit(t, "feature", "f.txt")
		commitTemplate(t, r, "docs/pull_request_template.md", "## Head\n")
		r.park(t)

		plan, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, nil)
		if err != nil {
			t.Fatalf("preflightPR: %v", err)
		}
		tmpl := loadPRTemplate(plan)
		if tmpl == nil || len(tmpl.Sections) != 1 || tmpl.Sections[0].Heading != "Head" {
			t.Fatalf("template = %+v, want the head's", tmpl)
		}
	})

	t.Run("no template", func(t *testing.T) {
		r := newPRRepo(t)
		r.commit(t, "feature", "f.txt")
		r.park(t)

		plan, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, nil)
		if err != nil {
			t.Fatalf("preflightPR: %v", err)
		}
		if tmpl := loadPRTemplate(plan); tmpl != nil {
			t.Fatalf("template = %+v, want nil", tmpl)
		}
	})

	t.Run("an uncommitted template in the worktree is not read", func(t *testing.T) {
		r := newPRRepo(t)
		r.commit(t, "feature", "f.txt")
		r.park(t)
		if err := os.MkdirAll(filepath.Join(r.work, ".github"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, r.work, ".github/PULL_REQUEST_TEMPLATE.md", "## Worktree\n")

		plan, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, nil)
		if err != nil {
			t.Fatalf("preflightPR: %v", err)
		}
		if tmpl := loadPRTemplate(plan); tmpl != nil {
			t.Fatalf("template = %+v, want nil: PR evidence comes from commits", tmpl)
		}
	})
}
