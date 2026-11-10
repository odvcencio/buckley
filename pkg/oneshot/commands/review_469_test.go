package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

// plainTextThenInvoker answers the first call without a tool call, like a
// model that replies in plain text, and then follows the scripted replies.
type plainTextInvoker struct {
	replies []json.RawMessage // nil entry = no tool call
	prompts []string
}

func (i *plainTextInvoker) Invoke(_ context.Context, _, prompt string, _ tools.Definition, _ *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	index := len(i.prompts)
	i.prompts = append(i.prompts, prompt)
	if index >= len(i.replies) {
		index = len(i.replies) - 1
	}
	if i.replies[index] == nil {
		return &oneshot.Result{}, nil, nil
	}
	return &oneshot.Result{ToolCall: &tools.ToolCall{Name: "generate_commit", Arguments: i.replies[index]}}, nil, nil
}

// Review of #469, finding 7: a plain-text answer between two failures must not
// make the next retry forget the first failure.
func TestRetryAfterPlainTextAnswerStillCarriesEarlierChecks(t *testing.T) {
	noBody := json.RawMessage(`{"action":"fix","subject":"tidy the report","body":[]}`)
	good := json.RawMessage(`{"action":"fix","subject":"tidy the report","body":["Keep it short"]}`)
	invoker := &plainTextInvoker{replies: []json.RawMessage{noBody, nil, good}}
	if _, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(invoker.prompts) != 3 {
		t.Fatalf("calls = %d", len(invoker.prompts))
	}
	if !strings.Contains(invoker.prompts[2], "body requires at least one bullet") {
		t.Errorf("the third prompt lost the first failure:\n%s", invoker.prompts[2])
	}
}

// Finding 8: a header that code cut beside another failure was shown to the
// model; if the model copies it, the result must still say it was changed.
func TestPartialRepairIsDisclosedWhenTheModelCopiesIt(t *testing.T) {
	long := longHeaderCommit(t, "update", "regenerate the tables and golden traces for the new lexer automaton") // no body
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{long, json.RawMessage(`{"action":"update","scope":"parser","subject":"regenerate the tables and golden traces","body":["Regenerate the tables"]}`)}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Repairs) == 0 || !strings.Contains(strings.Join(result.Repairs, ";"), "earlier answer was changed") {
		t.Fatalf("repairs = %q, want the earlier cut to be disclosed", result.Repairs)
	}
}

type prMarkupDefinition struct{ PRDefinition }

func (prMarkupDefinition) ContextSources() []oneshot.ContextSource { return nil }

func longTitlePR(t *testing.T) json.RawMessage {
	t.Helper()
	title := "make the pull request title generator keep every detail the reviewer asked for in the long form of the title"
	if len(title) <= 100 {
		t.Fatalf("fixture title has %d characters", len(title))
	}
	raw, _ := json.Marshal(map[string]any{"action": "add", "title": title, "summary": "x", "changes": []string{"a change"}})
	return raw
}

// Finding 9: a PR title over 100 characters goes back to the model once.
func TestPRTitleOverLimitIsReturnedToTheModelBeforeItIsCut(t *testing.T) {
	short, _ := json.Marshal(map[string]any{"action": "add", "title": "keep every detail in the pull request title", "summary": "x", "changes": []string{"a change"}})
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{longTitlePR(t), short}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), prMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err != nil || len(invoker.prompts) != 2 {
		t.Fatalf("err=%v calls=%d, want one retry", err, len(invoker.prompts))
	}
	if got := result.Value.(*PRResult).Title; got != "keep every detail in the pull request title" {
		t.Errorf("title = %q, want the model's own", got)
	}

	invoker = &commitMarkupInvoker{replies: []json.RawMessage{longTitlePR(t), longTitlePR(t)}}
	result, err = oneshot.NewFramework(invoker, nil).Run(context.Background(), prMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err != nil || len(invoker.prompts) != 2 || len(result.Repairs) == 0 {
		t.Fatalf("err=%v calls=%d repairs=%q, want a cut after one retry", err, len(invoker.prompts), result.Repairs)
	}
}
