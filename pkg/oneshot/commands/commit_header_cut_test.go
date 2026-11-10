package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

// longHeaderCommit builds tool arguments whose header is over the limit.
func longHeaderCommit(t *testing.T, action, subject string, body ...string) json.RawMessage {
	t.Helper()
	header := action + "(parser): " + subject
	if n := utf8.RuneCountInString(header); n <= commitmsg.HeaderLimit {
		t.Fatalf("fixture header has %d characters; it must exceed %d", n, commitmsg.HeaderLimit)
	}
	raw, err := json.Marshal(map[string]any{"action": action, "scope": "parser", "subject": subject, "body": body})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A commit failed with "validation: header exceeds 72 characters (77)" after
// three attempts and passed on the next run. A header over the limit alone is
// cut by code; this is the case that already worked.
func TestCommitHeaderOverLimit_IsCutWithoutAnotherModelCall(t *testing.T) {
	raw := longHeaderCommit(t, "update", "regenerate the tables and golden traces for the new lexer automaton", "Regenerate the tables")
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{raw}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err != nil || len(invoker.prompts) != 1 {
		t.Fatalf("err=%v calls=%d, want the header cut in one call", err, len(invoker.prompts))
	}
	if header := result.Value.(*CommitResult).Header(); utf8.RuneCountInString(header) > commitmsg.HeaderLimit {
		t.Fatalf("header %q is still over the limit", header)
	}
}

// The cut was thrown away when anything else was wrong with the answer. The
// model was then told about the header length only, and a run that ended that
// way reported the header although another check blocked the message.
func TestCommitHeaderOverLimit_DoesNotHideTheCheckThatBlocks(t *testing.T) {
	// The subject repeats the action verb, which code cannot repair.
	raw := longHeaderCommit(t, "update", "update the generated tables and golden traces for the new lexer automaton", "Regenerate the tables")
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{raw, raw, raw}}
	_, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err == nil {
		t.Fatal("a subject that repeats the verb three times must fail")
	}
	if strings.Contains(err.Error(), "header exceeds") {
		t.Errorf("the error blames the header length, which code can cut: %v", err)
	}
	if !strings.Contains(err.Error(), "subject repeats the action") {
		t.Errorf("the error does not name the check that blocks: %v", err)
	}
	retry := invoker.prompts[1]
	if strings.Contains(retry, "header exceeds") || !strings.Contains(retry, "subject repeats the action") {
		t.Errorf("the retry prompt must name the check the model has to fix, not the length:\n%s", retry)
	}
	// The arguments shown to the model already have the cut subject.
	if strings.Contains(retry, `for the new lexer automaton"`) {
		t.Errorf("the retry prompt shows the subject before the cut:\n%s", retry)
	}
}

// With a private term in the message, the run ended with the header error and
// not with the safety error, so the caller's "commit blocked" message never
// appeared and the model was never told what to remove.
func TestCommitHeaderOverLimit_DoesNotHideTheSafetyCheck(t *testing.T) {
	withPolicy(t, commitmsg.Policy{DenyTerms: []string{"quuxcorp"}})
	raw := longHeaderCommit(t, "update", "regenerate the tables and golden traces for the new lexer automaton", "Regenerate the quuxcorp tables")
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{raw, raw, raw}}
	_, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})

	var leak *commitmsg.LeakError
	if !errors.As(err, &leak) {
		t.Fatalf("error = %v, want the safety error", err)
	}
	for i, prompt := range invoker.prompts[1:] {
		if !strings.Contains(prompt, "safety check") {
			t.Errorf("retry %d does not tell the model about the safety check:\n%s", i+1, prompt)
		}
		if strings.Contains(prompt, "quuxcorp") {
			t.Errorf("retry %d echoes the rejected text", i+1)
		}
	}
}

// fixesWhatItIsToldInvoker models a model that fixes the one problem the
// prompt names: told about the safety check it drops the private term, told
// about the header it shortens the header and keeps the term.
type fixesWhatItIsToldInvoker struct {
	t       *testing.T
	prompts []string
}

func (i *fixesWhatItIsToldInvoker) Invoke(_ context.Context, _, prompt string, _ tools.Definition, _ *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	i.prompts = append(i.prompts, prompt)
	long := "regenerate the tables and golden traces for the new lexer automaton"
	var raw json.RawMessage
	switch {
	case strings.Contains(prompt, "safety check"):
		raw = longHeaderCommit(i.t, "update", long, "Regenerate the tables")
	case strings.Contains(prompt, "header exceeds"):
		raw = json.RawMessage(`{"action":"update","scope":"parser","subject":"regenerate the tables","body":["Regenerate the quuxcorp tables"]}`)
	default:
		raw = longHeaderCommit(i.t, "update", long, "Regenerate the quuxcorp tables")
	}
	return &oneshot.Result{ToolCall: &tools.ToolCall{Name: "generate_commit", Arguments: raw}}, nil, nil
}

func TestCommitHeaderOverLimit_ModelHearsTheRealProblemFirst(t *testing.T) {
	withPolicy(t, commitmsg.Policy{DenyTerms: []string{"quuxcorp"}})
	invoker := &fixesWhatItIsToldInvoker{t: t}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(invoker.prompts) != 2 {
		t.Fatalf("model calls = %d, want 2: the first retry must name the safety check, not the header", len(invoker.prompts))
	}
	message := result.Value.(*CommitResult).Format()
	if strings.Contains(message, "quuxcorp") || utf8.RuneCountInString(strings.SplitN(message, "\n", 2)[0]) > commitmsg.HeaderLimit {
		t.Fatalf("accepted message:\n%s", message)
	}
}
