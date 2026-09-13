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

type commitMarkupDefinition struct{ CommitDefinition }

func (commitMarkupDefinition) ContextSources() []oneshot.ContextSource { return nil }

type commitMarkupInvoker struct {
	replies []json.RawMessage
	prompts []string
}

func (i *commitMarkupInvoker) Invoke(_ context.Context, _, prompt string, _ tools.Definition, _ *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	index := len(i.prompts)
	i.prompts = append(i.prompts, prompt)
	if index >= len(i.replies) {
		return nil, nil, fmt.Errorf("unexpected invocation %d", index+1)
	}
	return &oneshot.Result{ToolCall: &tools.ToolCall{Name: "generate_commit", Arguments: i.replies[index]}}, nil, nil
}

func TestCommitMarkupValidationRepair(t *testing.T) {
	valid := json.RawMessage(`{"action":"fix","subject":"preserve message text","body":["Run jest with --json"]}`)
	for _, body := range []string{`["<arg_value>- Run jest with --json"]`, `"<arg_value>- Run jest with --json"`} {
		t.Run(body, func(t *testing.T) {
			bad := json.RawMessage(`{"action":"fix","subject":"preserve message text","body":` + body + `}`)
			for _, exhausted := range []bool{false, true} {
				t.Run(fmt.Sprint("exhausted=", exhausted), func(t *testing.T) {
					second := valid
					if exhausted {
						second = bad
					}
					invoker := &commitMarkupInvoker{replies: []json.RawMessage{bad, second}}
					result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 2})
					if len(invoker.prompts) != 2 || result == nil {
						t.Fatalf("result=%+v err=%v calls=%d", result, err, len(invoker.prompts))
					}
					if !strings.Contains(invoker.prompts[1], "body starts with tool markup; quote literal tags with backticks") {
						t.Fatalf("missing repair guidance: %s", invoker.prompts[1])
					}
					if exhausted {
						if err == nil || result.Value != nil || !strings.Contains(err.Error(), "tool markup") {
							t.Fatalf("exhausted repair accepted: result=%+v err=%v", result, err)
						}
						return
					}
					if err != nil {
						t.Fatalf("repair failed: result=%+v err=%v", result, err)
					}
					message := result.Value.(*CommitResult).Format()
					if message != "fix: preserve message text\n\n- Run jest with --json\n" {
						t.Fatalf("message=%q", message)
					}
				})
			}
		})
	}
}

func TestCommitMarkupPreservesLiteralTags(t *testing.T) {
	for _, bullet := range []string{"`<arg_value>` is a literal tag", "Preserve <arg_value> in source examples", "<div> remains valid HTML"} {
		t.Run(bullet, func(t *testing.T) {
			for _, body := range []any{[]string{bullet}, bullet} {
				raw, err := json.Marshal(map[string]any{"action": "fix", "subject": "preserve message text", "body": body})
				if err != nil {
					t.Fatal(err)
				}
				invoker := &commitMarkupInvoker{replies: []json.RawMessage{raw}}
				result, err := oneshot.NewFramework(invoker, nil).Run(context.Background(), commitMarkupDefinition{}, oneshot.RunOpts{MaxRetries: 2})
				if err != nil || result == nil || len(invoker.prompts) != 1 {
					t.Fatalf("result=%+v err=%v calls=%d", result, err, len(invoker.prompts))
				}
				if got := result.Value.(*CommitResult).Format(); got != "fix: preserve message text\n\n- "+bullet+"\n" {
					t.Fatalf("literal text changed: %q", got)
				}
			}
		})
	}
}
