package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

// prRepairDefinition skips real git context gathering so the test can run
// without a repository, matching commitMarkupDefinition's pattern.
type prRepairDefinition struct{ PRDefinition }

func (prRepairDefinition) ContextSources() []oneshot.ContextSource { return nil }

type prRepairInvoker struct {
	replies []json.RawMessage
	prompts []string
}

func (i *prRepairInvoker) Invoke(_ context.Context, _, prompt string, _ tools.Definition, _ *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	index := len(i.prompts)
	i.prompts = append(i.prompts, prompt)
	if index >= len(i.replies) {
		return nil, nil, fmt.Errorf("unexpected invocation %d", index+1)
	}
	return &oneshot.Result{ToolCall: &tools.ToolCall{Name: "generate_pull_request", Arguments: i.replies[index]}}, nil, nil
}

// TestPRValidationRepairEchoesPreviousArgumentsAndMissingField covers a live
// failure: a model omitted the required "action" field on every one of 3
// attempts against `buckley pr`, so PR generation failed after exhausting
// retries. A repair prompt that only restates the English validation error
// is a blind retry; echoing the model's own previous tool call arguments
// alongside the error gives it a concrete, targeted correction to make
// instead of guessing what changed between attempts.
func TestPRValidationRepairEchoesPreviousArgumentsAndMissingField(t *testing.T) {
	isolatePRPrompt(t)

	missingAction := json.RawMessage(`{"title":"add(api): widen retry budget","summary":"Retries transient errors.","changes":["Widen retry budget"]}`)
	valid := json.RawMessage(`{"action":"add","title":"add(api): widen retry budget","summary":"Retries transient errors.","changes":["Widen retry budget"]}`)

	invoker := &prRepairInvoker{replies: []json.RawMessage{missingAction, valid}}
	result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), prRepairDefinition{}, oneshot.RunOpts{MaxRetries: 2})
	if err != nil || result == nil || len(invoker.prompts) != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(invoker.prompts))
	}

	repairPrompt := invoker.prompts[1]
	if !strings.Contains(repairPrompt, "action is required") {
		t.Fatalf("repair prompt missing the validation error: %s", repairPrompt)
	}
	if !strings.Contains(repairPrompt, string(missingAction)) {
		t.Fatalf("repair prompt does not echo the previous tool call arguments: %s", repairPrompt)
	}
}

// TestPRValidationRepairExhaustedNamesMissingFieldInFinalError covers the
// exact exhausted-retry failure text from the live incident: after 3
// attempts, the surfaced error must still name the missing field, not just
// "validation failed", so an operator scanning logs knows what to fix.
func TestPRValidationRepairExhaustedNamesMissingFieldInFinalError(t *testing.T) {
	isolatePRPrompt(t)

	missingAction := json.RawMessage(`{"title":"add(api): widen retry budget","summary":"Retries transient errors.","changes":["Widen retry budget"]}`)

	invoker := &prRepairInvoker{replies: []json.RawMessage{missingAction, missingAction, missingAction}}
	_, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), prRepairDefinition{}, oneshot.RunOpts{MaxRetries: 3})
	if err == nil {
		t.Fatal("expected exhausted validation retries to fail")
	}
	if !strings.Contains(err.Error(), "action is required") {
		t.Fatalf("exhausted error does not name the missing field: %v", err)
	}
	if len(invoker.prompts) != 3 {
		t.Fatalf("calls = %d, want 3 (one per attempt)", len(invoker.prompts))
	}
}
