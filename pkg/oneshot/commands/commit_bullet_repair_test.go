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

func overlongBulletReply(t *testing.T) json.RawMessage {
	t.Helper()
	if n := len(strings.Fields(tooLongBullet)); n <= commitmsg.MaxBulletWords {
		t.Fatalf("fixture bullet has %d words; it must exceed the limit", n)
	}
	raw, err := json.Marshal(map[string]any{
		"action":  "add",
		"subject": "group generated files in review summaries",
		"body":    []string{tooLongBullet, "Keep hand-written files in the per-file ledger"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A bullet over the word limit goes back to the model once. The model can
// shorten its own words without losing the point; a cut by code cannot.
func TestCommitOverlongBullet_AsksTheModelToShortenItOnce(t *testing.T) {
	shortened, _ := json.Marshal(map[string]any{
		"action":  "add",
		"subject": "group generated files in review summaries",
		"body":    []string{"Collapse generated files into one summary line per group", "Keep hand-written files in the per-file ledger"},
	})
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{overlongBulletReply(t), shortened}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 2})
	if err != nil || result == nil || result.Value == nil {
		t.Fatalf("run failed: result=%+v err=%v calls=%d", result, err, len(invoker.prompts))
	}
	if len(invoker.prompts) != 2 {
		t.Fatalf("model called %d times, want 2", len(invoker.prompts))
	}
	if !strings.Contains(invoker.prompts[1], "bullet 1 has") || !strings.Contains(invoker.prompts[1], "at most 20 words") {
		t.Fatalf("retry prompt does not name the bullet and the limit:\n%s", invoker.prompts[1])
	}
	if got := result.Value.(*CommitResult).Body[0]; got != "Collapse generated files into one summary line per group" {
		t.Fatalf("body[0] = %q, want the model's own shortened bullet", got)
	}
	if len(result.Repairs) != 0 {
		t.Fatalf("repairs = %q, want none: code cut nothing", result.Repairs)
	}
}

// When the second answer is still too long, code cuts the bullet and the
// result says so. The model is not asked a third time.
func TestCommitOverlongBullet_StillTooLongIsCutAndReported(t *testing.T) {
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{overlongBulletReply(t), overlongBulletReply(t)}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err != nil || result == nil || result.Value == nil {
		t.Fatalf("run failed: result=%+v err=%v calls=%d", result, err, len(invoker.prompts))
	}
	if len(invoker.prompts) != 2 {
		t.Fatalf("model called %d times, want 2", len(invoker.prompts))
	}
	cr := result.Value.(*CommitResult)
	if n := len(strings.Fields(cr.Body[0])); n > commitmsg.MaxBulletWords {
		t.Fatalf("repaired bullet has %d words: %q", n, cr.Body[0])
	}
	if !strings.HasPrefix(tooLongBullet, cr.Body[0]) {
		t.Fatalf("repair changed the wording instead of shortening it: %q", cr.Body[0])
	}
	if len(result.Repairs) != 1 || !strings.Contains(result.Repairs[0], "cut bullet 1") {
		t.Fatalf("repairs = %q, want one line about bullet 1", result.Repairs)
	}
}

// With a single attempt there is no retry to spend: code cuts at once.
func TestCommitOverlongBullet_NoRetryLeftIsCutAtOnce(t *testing.T) {
	invoker := &commitMarkupInvoker{replies: []json.RawMessage{overlongBulletReply(t)}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 1})
	if err != nil || len(invoker.prompts) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(invoker.prompts))
	}
	if len(result.Repairs) != 1 {
		t.Fatalf("repairs = %q", result.Repairs)
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
