package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

// scriptedInvoker answers each model call with the next entry of answers and
// repeats the last one. It keeps every prompt it was given.
type scriptedInvoker struct {
	answers []string
	prompts *[]string
}

func (s scriptedInvoker) Invoke(_ context.Context, _, userPrompt string, _ tools.Definition, _ *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	n := len(*s.prompts)
	*s.prompts = append(*s.prompts, userPrompt)
	if n >= len(s.answers) {
		n = len(s.answers) - 1
	}
	return &oneshot.Result{ToolCall: &tools.ToolCall{Arguments: json.RawMessage(s.answers[n])}}, nil, nil
}

// naturalMergeAnswer is what a model writes when it is told "Merging A into
// B": the action "merge" and git's own subject. As a generated subject it
// breaks three rules at once: it starts with a capital letter, it repeats the
// action verb, and with a long branch name the header passes 72 characters.
const naturalMergeAnswer = `{"action":"merge","scope":"cgo_harness",
	"subject":"Merge remotes/origin/main into codex/compact-correctness-qualification-20261027",
	"body":["Bring in the upstream harness changes","f.txt: kept the incoming version"]}`

// A merge commit failed with a validation error about the subject, and the
// same command passed on the next try. The subject the model writes for a
// merge never reaches the commit (completeMergeWithRunner replaces it), so
// it must not fail the run.
func TestMergeCommitDefinition_DoesNotJudgeTheSubjectItReplaces(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("rules.NewEngine: %v", err)
	}
	for name, eng := range map[string]*rules.Engine{"built-in policy": nil, "arbiter policy": engine} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir()) // no private deny-list, no global git config
			stageRepo(t, map[string]string{"f.txt": "line1\nfeature change\n"})
			var prompts []string
			framework := oneshot.NewFramework(scriptedInvoker{answers: []string{naturalMergeAnswer}, prompts: &prompts}, eng)

			result, err := framework.Run(context.Background(), mergeCommitDefinition{source: "feature", target: "main"}, oneshot.RunOpts{})
			if err != nil {
				t.Fatalf("Run: %v\n(the model's subject is replaced after generation; it must not fail validation)", err)
			}
			if len(prompts) != 1 {
				t.Errorf("model calls = %d, want 1: nothing in the answer needed a retry", len(prompts))
			}
			if result.Value == nil {
				t.Fatal("no commit result")
			}
		})
	}
}

// The body is what a merge commit keeps from the model, so its rules still
// apply, and the retry prompt names the rule.
func TestMergeCommitDefinition_StillChecksTheBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no private deny-list, no global git config
	stageRepo(t, map[string]string{"f.txt": "line1\nfeature change\n"})
	var prompts []string
	noBody := `{"action":"merge","subject":"Merge feature into main","body":[]}`
	framework := oneshot.NewFramework(scriptedInvoker{answers: []string{noBody, naturalMergeAnswer}, prompts: &prompts}, nil)

	if _, err := framework.Run(context.Background(), mergeCommitDefinition{source: "feature", target: "main"}, oneshot.RunOpts{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prompts) != 2 {
		t.Fatalf("model calls = %d, want 2: an empty body is rejected once", len(prompts))
	}
	if !strings.Contains(prompts[1], "body requires at least one bullet") {
		t.Errorf("the retry prompt does not carry the validator's message:\n%s", prompts[1])
	}

	prompts = nil
	always := oneshot.NewFramework(scriptedInvoker{answers: []string{noBody}, prompts: &prompts}, nil)
	if _, err := always.Run(context.Background(), mergeCommitDefinition{source: "feature", target: "main"}, oneshot.RunOpts{}); err == nil || !strings.Contains(err.Error(), "body requires at least one bullet") {
		t.Fatalf("Run() error = %v, want the body rule after the retries", err)
	}
}

func TestMergeCommitDefinition_PromptSaysWhoWritesTheHeader(t *testing.T) {
	def := mergeCommitDefinition{source: "feature", target: "main"}
	prompt := def.BuildPrompt(&oneshot.Context{Sources: map[string]string{}})
	if !strings.Contains(prompt, `"merge(<scope>): Merge feature into main"`) || !strings.Contains(prompt, "are not used") {
		t.Errorf("the prompt does not say that Buckley writes the header:\n%s", prompt)
	}
}

// End to end through the framework: the natural answer completes the merge.
func TestCompleteMerge_NaturalModelAnswerCommits(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no private deny-list, no global git config
	repo := conflictedMergeRepo(t)
	theirs := runGitIn(t, repo, "show", "MERGE_HEAD:f.txt")
	writeFile(t, repo, "f.txt", theirs+"\n")
	runGitIn(t, repo, "add", "f.txt")
	chdirTemp(t, repo)

	var prompts []string
	def := mergeCommitDefinition{source: "feature", target: "main"}
	runner := &frameworkCommitRunner{
		framework: oneshot.NewFramework(scriptedInvoker{answers: []string{naturalMergeAnswer}, prompts: &prompts}, nil),
		def:       def,
	}
	opts := commitCommandOptions{yes: true, push: false, compactOutput: true, showCost: false}
	if err := completeMergeWithRunner(opts, context.Background(), runner, nil, "feature", "main"); err != nil {
		t.Fatalf("completeMergeWithRunner: %v", err)
	}
	if subject := runGitIn(t, repo, "log", "-1", "--format=%s"); subject != "merge(cgo_harness): Merge feature into main" {
		t.Fatalf("subject = %q", subject)
	}
	if parents := strings.Fields(runGitIn(t, repo, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("parents = %v, want 2", parents)
	}
}

// A message regenerated at the confirmation prompt went out with the model's
// own header, because the fixed header was set only after the first
// generation.
func TestMergeHeaderRunner_FixesTheHeaderOfEveryGeneration(t *testing.T) {
	inner := &fakeCommitRunner{result: &commitRunResult{Commit: &commands.CommitResult{
		Action: "update", Scope: "core", Subject: "bring in the feature branch", Body: []string{"Keep the incoming version"},
	}}}
	runner := mergeHeaderRunner{commitRunner: inner, source: "feature", target: "main"}

	for attempt := 1; attempt <= 2; attempt++ {
		inner.result.Commit.Action, inner.result.Commit.Subject = "update", "bring in the feature branch"
		result, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if header := result.Commit.Header(); header != "merge(core): Merge feature into main" {
			t.Fatalf("generation %d: header = %q", attempt, header)
		}
	}

	// At the prompt, [r] regenerates through the same runner.
	stdin, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("r\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	previous := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = previous })

	inner.result.Commit.Action, inner.result.Commit.Subject = "update", "bring in the feature branch"
	var action, message string
	captureStdout(t, func() {
		action, message = handleCommitPrompt("old message", runner, context.Background(), commitCommandOptions{compactOutput: true}, nil, commitmsg.ChangeMetadata{})
	})
	if action != "regenerate" || !strings.HasPrefix(message, "merge(core): Merge feature into main\n") {
		t.Fatalf("regenerated message = %q (action %q), want the fixed merge header", message, action)
	}

	failing := mergeHeaderRunner{commitRunner: &fakeCommitRunner{err: errors.New("model down")}, source: "feature", target: "main"}
	if _, err := failing.Run(context.Background()); err == nil {
		t.Fatal("the runner's error was swallowed")
	}
}

// A merge without conflicts must say so in the prompt. Otherwise the model
// invents a conflict and how it was resolved (live check, 10 November 2026).
func TestMergeCommitPrompt_SaysWhenThereWereNoConflicts(t *testing.T) {
	ctx := &oneshot.Context{Sources: map[string]string{}}
	clean := mergeCommitDefinition{source: "topic", target: "main"}.BuildPrompt(ctx)
	if !strings.Contains(clean, "There were no conflicts in this merge") {
		t.Errorf("clean merge prompt lacks the no-conflict line:\n%s", clean)
	}
	resolved := mergeCommitDefinition{source: "topic", target: "main", resolutions: []string{"f.txt: kept ours"}}.BuildPrompt(ctx)
	if strings.Contains(resolved, "There were no conflicts") || !strings.Contains(resolved, "f.txt: kept ours") {
		t.Errorf("conflicted merge prompt is wrong:\n%s", resolved)
	}
}
