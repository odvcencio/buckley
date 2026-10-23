package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
)

const tooLongBullet = "Detect generated files from headers and build output siblings, then collapse them into grouped diff summaries so reviewers see one line per group"

func TestCommitRepairsOverlongBulletWithoutAnotherModelCall(t *testing.T) {
	if n := len(strings.Fields(tooLongBullet)); n <= commitmsg.MaxBulletWords {
		t.Fatalf("fixture bullet has %d words; it must exceed the limit", n)
	}
	bad, err := json.Marshal(map[string]any{
		"action":  "add",
		"subject": "group generated files in review summaries",
		"body":    []string{tooLongBullet, "Keep hand-written files in the per-file ledger"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The stub has one reply. A second model call fails the test.
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{bad}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 2})
	if err != nil || result == nil || result.Value == nil {
		t.Fatalf("run failed: result=%+v err=%v calls=%d", result, err, len(invoker.prompts))
	}
	if len(invoker.prompts) != 1 {
		t.Fatalf("model called %d times, want 1", len(invoker.prompts))
	}
	cr := result.Value.(*CommitResult)
	if len(cr.Body) != 2 {
		t.Fatalf("body = %q, want both bullets kept", cr.Body)
	}
	if n := len(strings.Fields(cr.Body[0])); n > commitmsg.MaxBulletWords {
		t.Fatalf("repaired bullet has %d words: %q", n, cr.Body[0])
	}
	if !strings.HasPrefix(tooLongBullet, cr.Body[0]) {
		t.Fatalf("repair changed the wording instead of shortening it: %q", cr.Body[0])
	}
	if cr.Body[1] != "Keep hand-written files in the per-file ledger" {
		t.Fatalf("valid bullet changed: %q", cr.Body[1])
	}
}

func TestCommitRepairsExtraBulletsAndKeepsOtherViolationsForTheModel(t *testing.T) {
	var body []string
	for _, w := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		body = append(body, "Change number "+w)
	}
	extra, _ := json.Marshal(map[string]any{"action": "fix", "subject": "tidy the report", "body": body})
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{extra}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 2})
	if err != nil || len(invoker.prompts) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(invoker.prompts))
	}
	if got := len(result.Value.(*CommitResult).Body); got != commitmsg.MaxBullets {
		t.Fatalf("body has %d bullets, want %d", got, commitmsg.MaxBullets)
	}

	// A violation with no mechanical fix still goes back to the model, with the rule named.
	good, _ := json.Marshal(map[string]any{"action": "fix", "subject": "tidy the report", "body": []string{"Keep it short"}})
	invoker = &commitMarkupInvoker{replies: []json.RawMessage{
		json.RawMessage(`{"action":"fix","subject":"fix the report","body":["` + tooLongBullet + `"]}`), good}}
	result, err = oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 2})
	if err != nil || len(invoker.prompts) != 2 {
		t.Fatalf("err=%v calls=%d", err, len(invoker.prompts))
	}
	if !strings.Contains(invoker.prompts[1], "repeats the action") {
		t.Fatalf("retry prompt lacks the violation: %s", invoker.prompts[1])
	}
}
