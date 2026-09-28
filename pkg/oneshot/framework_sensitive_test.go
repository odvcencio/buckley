package oneshot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type sensitiveErr struct{}

func (sensitiveErr) Error() string             { return "message failed the safety check" }
func (sensitiveErr) SensitiveValidation() bool { return true }

type sensitiveDefinition struct {
	retentionDefinition
	calls int
}

func (d *sensitiveDefinition) ValidateWithContext(*Context, json.RawMessage) error {
	d.calls++
	return sensitiveErr{}
}

type promptCapture struct{ prompts []string }

func (p *promptCapture) Invoke(_ context.Context, _, user string, _ tools.Definition, _ *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	p.prompts = append(p.prompts, user)
	trace := retentionFrameworkTrace("m", "m", 10, 1, "")
	return retentionFrameworkToolResult(`{"ok":true,"note":"zorblax-secret"}`, trace), trace, nil
}

func TestContextValidatorFailureDoesNotEchoArguments(t *testing.T) {
	cap := &promptCapture{}
	def := &sensitiveDefinition{}
	_, err := NewFramework(cap, nil).Run(context.Background(), def, RunOpts{MaxRetries: 3})
	if err == nil {
		t.Fatal("expected the run to fail after retries")
	}
	if def.calls != 3 || len(cap.prompts) != 3 {
		t.Fatalf("calls=%d prompts=%d, want 3 each", def.calls, len(cap.prompts))
	}
	for _, p := range cap.prompts[1:] {
		if strings.Contains(p, "zorblax") {
			t.Fatalf("repair prompt echoed rejected arguments: %q", p)
		}
		if !strings.Contains(p, "safety check") {
			t.Fatalf("repair prompt lacks the failure reason: %q", p)
		}
	}
}
