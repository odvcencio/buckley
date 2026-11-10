package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

// commitRetryDefinition skips git context so the test runs without a repository.
type commitRetryDefinition struct{ CommitDefinition }

func (commitRetryDefinition) ContextSources() []oneshot.ContextSource { return nil }

// A retry prompt that names only the latest failed check lets a model fix
// that rule and break the earlier one again: verb repeated, then no body,
// then verb repeated. The third prompt must carry both rules, and when the
// run fails, the error must say what each attempt broke.
func TestCommitRetryPromptCarriesEveryFailedCheck(t *testing.T) {
	repeatsVerb := json.RawMessage(`{"action":"fix","subject":"fix the nil guard","body":["Guard the nil map"]}`)
	noBody := json.RawMessage(`{"action":"fix","subject":"guard the nil map","body":[]}`)
	valid := json.RawMessage(`{"action":"fix","subject":"guard the nil map","body":["Guard the nil map"]}`)

	invoker := &prRepairInvoker{replies: []json.RawMessage{repeatsVerb, noBody, valid}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitRetryDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err != nil || result == nil || len(invoker.prompts) != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(invoker.prompts))
	}

	second, third := invoker.prompts[1], invoker.prompts[2]
	if !strings.Contains(second, "subject repeats the action") || strings.Contains(second, "Earlier attempts") {
		t.Errorf("the second prompt must name the first failure and nothing earlier:\n%s", second)
	}
	if !strings.Contains(third, "failed validation: body requires at least one bullet") {
		t.Errorf("the third prompt does not name the latest failure:\n%s", third)
	}
	if !strings.Contains(third, "Earlier attempts failed these checks. The new arguments must pass them too:\n- subject repeats the action") {
		t.Errorf("the third prompt does not carry the check the first attempt failed:\n%s", third)
	}

	// The same failure twice is listed once.
	again := &prRepairInvoker{replies: []json.RawMessage{noBody, noBody, valid}}
	if _, err := oneshot.NewFramework(again, nil).Run(context.Background(), commitRetryDefinition{}, oneshot.RunOpts{MaxRetries: 3}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(again.prompts[2], "Earlier attempts") {
		t.Errorf("a repeated failure was listed as an earlier one:\n%s", again.prompts[2])
	}

	exhausted := &prRepairInvoker{replies: []json.RawMessage{repeatsVerb, noBody, repeatsVerb}}
	_, err = oneshot.NewFramework(exhausted, nil).Run(context.Background(), commitRetryDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err == nil {
		t.Fatal("expected the run to fail after three invalid answers")
	}
	for _, want := range []string{"failed after 3 attempts", "last attempt: validation: subject repeats the action", "earlier attempts: ", "body requires at least one bullet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
