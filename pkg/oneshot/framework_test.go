package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type retryErrorDefinition struct {
	validateErr  error
	unmarshalErr error
}

func (retryErrorDefinition) Name() string                    { return "retry-test" }
func (retryErrorDefinition) ContextSources() []ContextSource { return nil }
func (retryErrorDefinition) SystemPrompt() string            { return "system" }
func (retryErrorDefinition) BuildPrompt(*Context) string     { return "prompt" }
func (retryErrorDefinition) Tool() tools.Definition {
	return tools.Definition{Name: "generate_test"}
}
func (d retryErrorDefinition) Validate(json.RawMessage) error { return d.validateErr }
func (d retryErrorDefinition) Unmarshal(json.RawMessage) (any, error) {
	if d.unmarshalErr != nil {
		return nil, d.unmarshalErr
	}
	return "ok", nil
}

type retryErrorInvoker struct {
	result *Result
}

func (i retryErrorInvoker) Invoke(context.Context, string, string, tools.Definition, *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	return i.result, &transparency.Trace{}, nil
}

func TestFrameworkRetryExhaustionSurfacesUnderlyingReason(t *testing.T) {
	tests := []struct {
		name       string
		def        retryErrorDefinition
		result     *Result
		wantReason string
	}{
		{
			name:       "missing tool call",
			result:     &Result{TextContent: "plain text"},
			wantReason: `model did not call required tool "generate_test"`,
		},
		{
			name:       "validation failure",
			def:        retryErrorDefinition{validateErr: errors.New("title is required")},
			result:     &Result{ToolCall: &tools.ToolCall{Arguments: json.RawMessage(`{}`)}},
			wantReason: "invalid generate_test tool arguments: title is required",
		},
		{
			name:       "decode failure",
			def:        retryErrorDefinition{unmarshalErr: errors.New("unexpected end of JSON input")},
			result:     &Result{ToolCall: &tools.ToolCall{Arguments: json.RawMessage(`{`)}},
			wantReason: "decode generate_test tool arguments: unexpected end of JSON input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			framework := NewFramework(retryErrorInvoker{result: tt.result}, nil)
			_, err := framework.Run(context.Background(), tt.def, RunOpts{MaxRetries: 2})
			if err == nil {
				t.Fatal("expected retry exhaustion")
			}
			if !strings.Contains(err.Error(), "failed after 2 attempts") || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("error = %q, want retry count and %q", err, tt.wantReason)
			}
		})
	}
}
