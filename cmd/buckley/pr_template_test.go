package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
		"### Not verified\n\nThe author supplied no notes. This text is written from the commits and the diff, and no check was run to write it."
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

// A template that Markdown++ cannot read exactly stops the command; the model is
// not called and --template=false is the way out.
func TestRunPRCommand_UnreadableTemplateStopsGeneration(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "## Summary\n<!-- describe -->\n## Checks\n- [X] "+strings.Repeat("a*b_", 5000)+"\n")
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	previous := newPRCommandRuntimeFn
	t.Cleanup(func() { newPRCommandRuntimeFn = previous })
	newPRCommandRuntimeFn = func(prCommandOptions) (*prCommandRuntime, func(), error) {
		t.Error("the model runtime was built for a template that cannot be read")
		return nil, func() {}, fmt.Errorf("must not be called")
	}

	var err error
	captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
	if err == nil || !strings.Contains(err.Error(), "cannot be used") || !strings.Contains(err.Error(), "PULL_REQUEST_TEMPLATE.md") || !strings.Contains(err.Error(), "MDPP-PARSE") {
		t.Fatalf("error = %v", err)
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

	t.Run("a template that Markdown++ cannot read exactly is reported with its source", func(t *testing.T) {
		r := newPRRepo(t)
		r.commit(t, "feature", "f.txt")
		commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "## Summary\n<!-- describe -->\n## Checks\n- [X] "+strings.Repeat("a*b_", 5000)+"\n")
		r.park(t)

		plan, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, nil)
		if err != nil {
			t.Fatalf("preflightPR: %v", err)
		}
		tmpl := loadPRTemplate(plan)
		if tmpl == nil || !strings.Contains(tmpl.Unreadable(), "MDPP-PARSE") || !strings.Contains(tmpl.Source, ".github/PULL_REQUEST_TEMPLATE.md") {
			t.Fatalf("template = %+v, Unreadable %q", tmpl, tmpl.Unreadable())
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

// shapeRun commits the template, writes the notes and runs buckley pr with
// the extra flags. It returns what the command printed, the body that gh
// received (empty when no pull request was created) and the error.
func shapeRun(t *testing.T, template, notes string, flags ...string) (stdout, stderr, body string, err error) {
	t.Helper()
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", template)
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	stubPRModelRecordingTool(t, `{"action":"add","title":"feature file","section_what_does_this_pr_do":"Adds the feature file.","section_why_this_approach":"Readers dominate.","summary":"Adds the feature file.","changes":["Add f.txt"]}`)
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", notes)
	args := append([]string{"--yes", "--cost=false", "--head", "feature", "--context-file", filepath.Join(dir, "notes.md")}, flags...)
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { err = runPRCommand(args) })
	})
	for _, call := range g.calls(t) {
		if strings.Join(call[:2], " ") == "pr create" {
			body = ghBodyArg(t, g, "pr create")
		}
	}
	return stdout, stderr, body, err
}

func readShapeFixture(t *testing.T, name string) string {
	t.Helper()
	// Other tests change the working directory, so find the file from this one.
	_, here, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "pkg", "oneshot", "commands", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A checkbox line of the notes that is no item of the template is not
// written; stderr names it, and --strict refuses (template shape, B2).
func TestRunPRCommand_TemplateShapeWarnsOnAnUnmatchedCheckbox(t *testing.T) {
	template, notes := readShapeFixture(t, "pr_template_shape.md"), readShapeFixture(t, "pr_notes_shape.md")

	_, stderr, body, err := shapeRun(t, template, notes)
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(stderr, "- [x] Added a changelog entry") || !strings.Contains(stderr, "matches no item of the template") {
		t.Errorf("stderr does not name the unmatched checkbox line: %q", stderr)
	}
	if strings.Contains(body, "Added a changelog entry") || !strings.Contains(body, "## Due diligence") || strings.Contains(body, "\n## Testing\n") {
		t.Errorf("the body does not keep the template's shape:\n%s", body)
	}

	_, stderr, body, err = shapeRun(t, template, notes, "--strict")
	if err == nil || !strings.Contains(err.Error(), "--strict") || body != "" || !strings.Contains(stderr, "matches no item of the template") {
		t.Errorf("--strict: error = %v, body = %q, stderr = %q; want a refusal before gh", err, body, stderr)
	}
}

// A template of checklists only has no place for the author's Testing
// section: it follows the template as before, stderr names the section, and
// --strict refuses (template shape, B1c).
func TestRunPRCommand_TemplateShapeChecklistOnly(t *testing.T) {
	template := "## Checklist\n\n- [ ] Tests ran\n- [ ] Docs updated\n"
	notes := "## Testing\n\n- `go test ./cache` passed\n"

	_, stderr, body, err := shapeRun(t, template, notes)
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(stderr, `"Testing"`) || !strings.Contains(stderr, "only checklists") {
		t.Errorf("stderr does not name the section: %q", stderr)
	}
	if !strings.Contains(body, "## Testing\n\n- `go test ./cache` passed") || !strings.Contains(body, "## Checklist\n\n- [ ] Tests ran") {
		t.Errorf("the body lost the section or the template:\n%s", body)
	}

	_, stderr, body, err = shapeRun(t, template, notes, "--strict")
	if err == nil || !strings.Contains(err.Error(), "--strict") || body != "" {
		t.Errorf("--strict: error = %v, body = %q, stderr = %q; want a refusal before gh", err, body, stderr)
	}
}

// --template=false ignores the template: Buckley's own sections, the notes'
// sections as top-level sections, no warning about checkboxes.
func TestRunPRCommand_TemplateShapeFalseIsUnchanged(t *testing.T) {
	template, notes := readShapeFixture(t, "pr_template_shape.md"), readShapeFixture(t, "pr_notes_shape.md")
	_, stderr, body, err := shapeRun(t, template, notes, "--template=false", "--strict")
	if err != nil {
		t.Fatalf("runPRCommand: %v\n%s", err, stderr)
	}
	for _, want := range []string{"## Summary\n\nAdds the feature file.", "## Testing\n\n- `go test ./cache -run TestSplitCacheReaders -count=1` passed", "## Counters\n", "## Not verified\n\n- Windows was not tried."} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Due diligence") || strings.Contains(stderr, "matches no item") {
		t.Errorf("the template was used although --template=false:\n%s\n%s", body, stderr)
	}
}

// Review of #478: with --template=false a notes section headed Checklist is an
// ordinary section, so the sections nested in it still reach the body.
func TestRunPRCommand_TemplateFalseKeepsASectionHeadedChecklist(t *testing.T) {
	template := readShapeFixture(t, "pr_template_shape.md")
	notes := "## Checklist\n\n- [x] Ready\n\n### Testing\n\n- Manual check passed.\n\n### Not verified\n\n- Windows was not tested.\n"
	_, stderr, body, err := shapeRun(t, template, notes, "--template=false", "--strict")
	if err != nil {
		t.Fatalf("runPRCommand: %v\n%s", err, stderr)
	}
	if !strings.Contains(body, "Manual check passed.") || !strings.Contains(body, "Windows was not tested.") {
		t.Errorf("a nested section was lost with --template=false:\n%s", body)
	}
}

// The generated Not verified statement of a checklist-only template warns on
// stderr, and --strict does not refuse it (only an author section that cannot
// be placed does).
func TestRunPRCommand_TemplateShapeChecklistOnlyFallbackWarns(t *testing.T) {
	_, stderr, body, err := shapeRun(t, "## Checklist\n\n- [ ] Tests ran\n", "", "--strict")
	if err != nil {
		t.Fatalf("--strict refused the generated fallback: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Not verified") || !strings.Contains(body, "## Not verified") {
		t.Errorf("stderr = %q, body =\n%s", stderr, body)
	}
}

// Review of #478, round seven, finding 3: a template whose setext heading text
// begins with "#" is a valid template; the command does not refuse it.
func TestRunPRCommand_TemplateWithAHashSetextHeadingIsUsed(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", "## What\n<!-- describe -->\n\n#tag\nmore\n---\n\n- [ ] Done\n")
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModelRecordingTool(t, `{"action":"add","title":"feature file","section_what":"Adds the feature file."}`)

	var err error
	out := captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(out, "#tag\nmore\n---\n\n- [ ] Done") || !strings.Contains(out, "Adds the feature file.") {
		t.Errorf("the body lost the template's heading or box:\n%s", out)
	}
}

// The text that reaches a pull request is tool-neutral: with the gotreesitter
// template, with and without notes, the body and the title carry no tool,
// vendor or model name, and no attribution.
func TestRunPRCommand_BodyNamesNoToolOrAttribution(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "pkg", "oneshot", "commands", "testdata", "gotreesitter_pr_template.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, notes := range map[string]string{
		"no notes":                   "",
		"notes without Not verified": "## Evidence\n\nRan the focused tests.\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newPRRepo(t)
			commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", string(raw))
			runGitIn(t, r.work, "push", "-q", "origin", "main")
			r.commit(t, "feature", "f.txt")
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubPRModelRecordingTool(t, `{"action":"add","title":"feature file","section_what_does_this_pr_do":"Adds the feature file.","section_why_this_approach":"It is the smallest change."}`)
			outFile := filepath.Join(t.TempDir(), "pr.md")
			args := []string{"--dry-run", "--cost=false", "--head", "feature", "--output", outFile}
			if notes != "" {
				notesFile := filepath.Join(t.TempDir(), "notes.md")
				if err := os.WriteFile(notesFile, []byte(notes), 0o644); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--context-file", notesFile)
			}
			captureStdout(t, func() { err = runPRCommand(args) })
			if err != nil {
				t.Fatalf("runPRCommand: %v", err)
			}
			text, err := os.ReadFile(outFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(text), "### Not verified") {
				t.Fatalf("the text has no Not verified statement:\n%s", text)
			}
			for _, word := range []string{"Buckley", "buckley", "Generated with", "Co-Authored-By", "Claude", "Codex", "GPT", "test-model", "Anthropic", "OpenAI"} {
				if strings.Contains(string(text), word) {
					t.Errorf("the text contains %q:\n%s", word, text)
				}
			}
		})
	}
}

// A template whose first non-blank line is indented stops the command before
// the model is called, with a message that says what is wrong.
func TestRunPRCommand_IndentedFirstLineStopsGeneration(t *testing.T) {
	for name, template := range map[string]string{
		"checkbox":   "  - [ ] A  \n",
		"code block": "\n    ## Example\n\n## Summary\n<!-- Describe the change. -->\n",
		"paragraph":  "  Fixed paragraph.\n\n## Summary\n<!-- describe -->\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newPRRepo(t)
			commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", template)
			runGitIn(t, r.work, "push", "-q", "origin", "main")
			r.commit(t, "feature", "f.txt")
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			previous := newPRCommandRuntimeFn
			t.Cleanup(func() { newPRCommandRuntimeFn = previous })
			newPRCommandRuntimeFn = func(prCommandOptions) (*prCommandRuntime, func(), error) {
				t.Error("the model runtime was built for a template with an indented first line")
				return nil, func() {}, fmt.Errorf("must not be called")
			}
			var err error
			captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
			if err == nil || !strings.Contains(err.Error(), "the template's first line is indented") || !strings.Contains(err.Error(), "--template=false") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRunPRCommand_SavedBodyTemplateGuard(t *testing.T) {
	r := newPRRepo(t)
	commitTemplate(t, r, ".github/PULL_REQUEST_TEMPLATE.md", testPRTemplate)
	runGitIn(t, r.work, "push", "-q", "origin", "main")
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	file := filepath.Join(t.TempDir(), "body.md")
	valid := "## What does this PR do?\n\nText\n\n## Correctness\n\n- [x] Focused package or unit tests ran for the touched behavior — passed\n- [ ] Documented anything intentionally left unvalidated in this PR — pending\n\n## Extra\n- [ ] Extra test"
	if err := os.WriteFile(file, []byte("Title: test title\n\n"+valid+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() { err = runPRCommand([]string{"--yes", "--head", "feature", "--body-file", file}) })
	if err != nil {
		t.Fatal(err)
	}
	if got := ghBodyArg(t, g, "pr create"); got != valid {
		t.Fatalf("body changed: %q", got)
	}
	if err := os.WriteFile(file, []byte("Title: test title\n\nOld body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { err = runPRCommand([]string{"--yes", "--head", "feature", "--body-file", file}) })
	if err == nil || !strings.Contains(err.Error(), "missing template heading") {
		t.Fatalf("expected template rejection, got %v", err)
	}
	g = installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	captureStdout(t, func() {
		err = runPRCommand([]string{"--yes", "--head", "feature", "--body-file", file, "--template=false"})
	})
	if err != nil {
		t.Fatalf("restore bypass: %v", err)
	}
	if got := ghBodyArg(t, g, "pr create"); got != "Old body" {
		t.Fatalf("restore changed body: %q", got)
	}
}
