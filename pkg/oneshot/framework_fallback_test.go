package oneshot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type fallbackTestInvoker struct {
	name      string
	order     *[]string
	valid     bool
	invokeErr error
}

func (i fallbackTestInvoker) Invoke(context.Context, string, string, tools.Definition, *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	*i.order = append(*i.order, i.name)
	raw := `{"ok":false}`
	if i.valid {
		raw = `{"ok":true}`
	}
	trace := retentionFrameworkTrace(i.name, i.name, 10, 1, "")
	return retentionFrameworkToolResult(raw, trace), trace, i.invokeErr
}

func TestFrameworkRun_ValidationFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name                          string
		fallbacks, valid, invokeError bool
		want                          []string
		wantError                     bool
	}{
		{name: "order", fallbacks: true, valid: true, want: []string{"primary", "primary", "second", "second", "third"}},
		{name: "no fallback", want: []string{"primary", "primary", "primary"}, wantError: true},
		{name: "exhausted", fallbacks: true, want: []string{"primary", "primary", "second", "second", "third", "third", "third"}, wantError: true},
		{name: "provider error", fallbacks: true, invokeError: true, want: []string{"primary"}, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var order []string
			primary := fallbackTestInvoker{name: "primary", order: &order}
			if tt.invokeError {
				primary.invokeErr = errors.New("provider failed")
			}
			framework := NewFramework(primary, nil)
			if tt.fallbacks {
				framework = framework.WithValidationFallbacks(
					func() (ToolInvoker, error) { return fallbackTestInvoker{name: "second", order: &order}, nil },
					func() (ToolInvoker, error) {
						return fallbackTestInvoker{name: "third", order: &order, valid: tt.valid}, nil
					},
				)
			}
			result, err := framework.Run(context.Background(), &retentionDefinition{}, RunOpts{MaxRetries: 3})
			if (err != nil) != tt.wantError {
				t.Fatalf("error=%v", err)
			}
			if !reflect.DeepEqual(order, tt.want) {
				t.Fatalf("order=%v want=%v", order, tt.want)
			}
			if result.Attempts != len(order) {
				t.Fatalf("attempts=%d order=%v", result.Attempts, order)
			}
			if len(order) > 1 && len(result.Trace.Attempts) != len(order) {
				t.Fatalf("lost traces: %+v", result.Trace)
			}
		})
	}
}
