package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type retentionFrameworkInvokerCall struct {
	result *Result
	trace  *transparency.Trace
	err    error
}

type retentionFrameworkInvoker struct {
	calls        []retentionFrameworkInvokerCall
	validateSeen int
}

func (i *retentionFrameworkInvoker) Invoke(context.Context, string, string, tools.Definition, *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	if i.validateSeen >= len(i.calls) {
		return nil, nil, errors.New("unexpected invoke")
	}
	next := i.calls[i.validateSeen]
	i.validateSeen++
	return next.result, next.trace, next.err
}

type retentionDefinition struct {
	validateCalls int
}

func (d *retentionDefinition) Name() string { return "retention" }

func (d *retentionDefinition) Tool() tools.Definition { return retentionToolDef() }

func (d *retentionDefinition) ContextSources() []ContextSource { return nil }

func (d *retentionDefinition) SystemPrompt() string { return "system" }

func (d *retentionDefinition) BuildPrompt(*Context) string { return "user" }

func (d *retentionDefinition) Validate(raw json.RawMessage) error {
	d.validateCalls++
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if !payload.OK {
		return fmt.Errorf("ok must be true")
	}
	return nil
}

func (d *retentionDefinition) Unmarshal(raw json.RawMessage) (any, error) {
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload.OK, nil
}

type retentionUnmarshalDefinition struct {
	retentionDefinition
	unmarshalCalls int
}

func (d *retentionUnmarshalDefinition) Unmarshal(json.RawMessage) (any, error) {
	d.unmarshalCalls++
	return nil, errors.New("forced unmarshal failure")
}

func TestFrameworkRunAggregatesDefinitionRetryTraceOnSuccess(t *testing.T) {
	firstTrace := retentionFrameworkTrace("definition-1", "resp-invalid", 10, 1, "")
	secondTrace := retentionFrameworkTrace("definition-2", "resp-valid", 20, 2, "")
	invoker := &retentionFrameworkInvoker{calls: []retentionFrameworkInvokerCall{
		{result: retentionFrameworkToolResult(`{"ok":false}`, firstTrace), trace: firstTrace},
		{result: retentionFrameworkToolResult(`{"ok":true}`, secondTrace), trace: secondTrace},
	}}
	def := &retentionDefinition{}
	framework := NewFramework(invoker, nil)

	result, err := framework.Run(context.Background(), def, RunOpts{MaxRetries: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Value != true {
		t.Fatalf("result = %#v, want valid value", result)
	}
	if result.Attempts != 2 || invoker.validateSeen != 2 || def.validateCalls != 2 {
		t.Fatalf("attempts/invokes/validates = %d/%d/%d, want 2/2/2", result.Attempts, invoker.validateSeen, def.validateCalls)
	}
	if result.Trace == nil || len(result.Trace.Attempts) != 2 {
		t.Fatalf("trace = %#v, want aggregate retry trace", result.Trace)
	}
	if got := result.Trace.Attempts[0].ValidationError; !strings.Contains(got, "ok must be true") {
		t.Fatalf("first validation error = %q, want invalid attempt annotation", got)
	}
	if result.Trace.Tokens.Input != 30 || result.Trace.Tokens.Output != 3 {
		t.Fatalf("tokens = %+v, want both attempts exactly once", result.Trace.Tokens)
	}
	if got := result.Trace.ModelExecutions; len(got) != 2 || got[0].ResponseID != "resp-invalid" || got[1].ResponseID != "resp-valid" {
		t.Fatalf("model executions = %+v, want both attempts", got)
	}
	firstTrace.ModelExecutions[0].ResponseID = "mutated"
	if result.Trace.Attempts[0].Trace.ModelExecutions[0].ResponseID != "resp-invalid" || result.Trace.ModelExecutions[0].ResponseID != "resp-invalid" {
		t.Fatalf("aggregate trace aliased source attempt identities: attempts=%+v aggregate=%+v", result.Trace.Attempts[0].Trace.ModelExecutions, result.Trace.ModelExecutions)
	}
}

func TestFrameworkRunExhaustedRetriesRetainTraceErrorFailClosed(t *testing.T) {
	tests := []struct {
		name           string
		makeDefinition func() Definition
		makeResult     func(args string, trace *transparency.Trace) *Result
		args           string
		wantError      string
		wantAnnotation string
		checkCounts    func(*testing.T, Definition)
	}{
		{
			name:           "no tool",
			makeDefinition: func() Definition { return &retentionDefinition{} },
			makeResult: func(_ string, trace *transparency.Trace) *Result {
				return &Result{TextContent: "plain text", Trace: trace}
			},
			wantError:      "model did not call",
			wantAnnotation: "model did not call",
			checkCounts: func(t *testing.T, def Definition) {
				t.Helper()
				if got := def.(*retentionDefinition).validateCalls; got != 0 {
					t.Fatalf("Validate calls = %d, want no validation without tool calls", got)
				}
			},
		},
		{
			name:           "validation",
			makeDefinition: func() Definition { return &retentionDefinition{} },
			makeResult:     retentionFrameworkToolResult,
			args:           `{"ok":false}`,
			wantError:      "validation",
			wantAnnotation: "ok must be true",
			checkCounts: func(t *testing.T, def Definition) {
				t.Helper()
				if got := def.(*retentionDefinition).validateCalls; got != 2 {
					t.Fatalf("Validate calls = %d, want both attempts validated", got)
				}
			},
		},
		{
			name:           "unmarshal",
			makeDefinition: func() Definition { return &retentionUnmarshalDefinition{} },
			makeResult:     retentionFrameworkToolResult,
			args:           `{"ok":true}`,
			wantError:      "unmarshal",
			wantAnnotation: "forced unmarshal failure",
			checkCounts: func(t *testing.T, def Definition) {
				t.Helper()
				got := def.(*retentionUnmarshalDefinition)
				if got.validateCalls != 2 || got.unmarshalCalls != 2 {
					t.Fatalf("Validate/Unmarshal calls = %d/%d, want 2/2", got.validateCalls, got.unmarshalCalls)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			firstTrace := retentionFrameworkTrace(tt.name+"-1", "resp-"+tt.name+"-1", 10, 1, "")
			secondTrace := retentionFrameworkTrace(tt.name+"-2", "resp-"+tt.name+"-2", 20, 2, "")
			invoker := &retentionFrameworkInvoker{calls: []retentionFrameworkInvokerCall{
				{result: tt.makeResult(tt.args, firstTrace), trace: firstTrace},
				{result: tt.makeResult(tt.args, secondTrace), trace: secondTrace},
			}}
			def := tt.makeDefinition()
			framework := NewFramework(invoker, nil)

			result, err := framework.Run(context.Background(), def, RunOpts{MaxRetries: 2})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Run error = %v, want %q", err, tt.wantError)
			}
			if result == nil || !result.Incomplete || result.Value != nil {
				t.Fatalf("result = %#v, want incomplete fail-closed nil value", result)
			}
			if result.Attempts != 2 || result.PrimaryAttempts != 2 {
				t.Fatalf("attempt counts = %d/%d, want exhausted two attempts", result.Attempts, result.PrimaryAttempts)
			}
			if result.Trace == nil {
				t.Fatal("result trace = nil, want retained aggregate trace")
			}
			if result.Trace.Error == "" || !strings.Contains(result.Trace.Error, tt.wantError) {
				t.Fatalf("trace error = %q, want terminal %q", result.Trace.Error, tt.wantError)
			}
			if firstTrace.Error != "" || secondTrace.Error != "" {
				t.Fatalf("caller-owned source trace errors mutated: first=%q second=%q", firstTrace.Error, secondTrace.Error)
			}
			if len(result.Trace.Attempts) != 2 {
				t.Fatalf("trace attempts = %d, want both exhausted attempts", len(result.Trace.Attempts))
			}
			if got := result.Trace.Attempts[1].ValidationError; !strings.Contains(got, tt.wantAnnotation) {
				t.Fatalf("last attempt annotation = %q, want %q", got, tt.wantAnnotation)
			}
			if result.Trace.Tokens.Input != 30 || result.Trace.Tokens.Output != 3 {
				t.Fatalf("tokens = %+v, want both attempts exactly once", result.Trace.Tokens)
			}
			if got := result.Trace.ModelExecutions; len(got) != 2 || got[0].ResponseID != "resp-"+tt.name+"-1" || got[1].ResponseID != "resp-"+tt.name+"-2" {
				t.Fatalf("model executions = %+v, want ordered exhausted attempts", got)
			}
			tt.checkCounts(t, def)
		})
	}
}

func TestFrameworkRunInvokeErrorReturnsIncompleteTraceWithoutValidatingPartial(t *testing.T) {
	firstTrace := retentionFrameworkTrace("definition-1", "resp-invalid", 10, 1, "")
	secondTrace := retentionFrameworkTrace("definition-2", "resp-partial", 7, 3, "transport failed")
	invoker := &retentionFrameworkInvoker{calls: []retentionFrameworkInvokerCall{
		{result: retentionFrameworkToolResult(`{"ok":false}`, firstTrace), trace: firstTrace},
		{result: retentionFrameworkToolResult(`{"ok":true}`, secondTrace), trace: secondTrace, err: errors.New("transport failed after body")},
	}}
	def := &retentionDefinition{}
	framework := NewFramework(invoker, nil)

	result, err := framework.Run(context.Background(), def, RunOpts{MaxRetries: 2})
	if err == nil || !strings.Contains(err.Error(), "invoke failed") {
		t.Fatalf("Run error = %v, want invoke failure", err)
	}
	if result == nil || !result.Incomplete || result.Value != nil {
		t.Fatalf("result = %#v, want incomplete with nil value", result)
	}
	if def.validateCalls != 1 {
		t.Fatalf("Validate calls = %d, want errored second invocation not validated", def.validateCalls)
	}
	if result.Attempts != 2 || result.PrimaryAttempts != 2 {
		t.Fatalf("attempt counts = %d/%d, want two attempted calls", result.Attempts, result.PrimaryAttempts)
	}
	if result.Trace == nil || len(result.Trace.Attempts) != 2 {
		t.Fatalf("trace = %#v, want aggregate failed trace", result.Trace)
	}
	if result.Trace.Error == "" || !strings.Contains(result.Trace.Error, "transport failed") {
		t.Fatalf("trace error = %q, want latest invoke error", result.Trace.Error)
	}
	if result.Trace.Tokens.Input != 17 || result.Trace.Tokens.Output != 4 {
		t.Fatalf("tokens = %+v, want first+failed attempts exactly once", result.Trace.Tokens)
	}
	if got := result.Trace.ModelExecutions; len(got) != 2 || got[0].ResponseID != "resp-invalid" || got[1].ResponseID != "resp-partial" {
		t.Fatalf("model executions = %+v, want failed second retained", got)
	}
}

func TestFrameworkRunInvokeErrorCopiesBlankTraceError(t *testing.T) {
	firstTrace := retentionFrameworkTrace("definition-1", "resp-invalid", 10, 1, "")
	secondTrace := retentionFrameworkTrace("definition-2", "resp-error", 7, 3, "")
	invoker := &retentionFrameworkInvoker{calls: []retentionFrameworkInvokerCall{
		{result: retentionFrameworkToolResult(`{"ok":false}`, firstTrace), trace: firstTrace},
		{result: retentionFrameworkToolResult(`{"ok":true}`, secondTrace), trace: secondTrace, err: errors.New("transport failed after body")},
	}}
	def := &retentionDefinition{}
	framework := NewFramework(invoker, nil)

	result, err := framework.Run(context.Background(), def, RunOpts{MaxRetries: 2})
	if err == nil {
		t.Fatal("Run error = nil, want invoke failure")
	}
	if secondTrace.Error != "" {
		t.Fatalf("caller-owned trace mutated error = %q, want blank", secondTrace.Error)
	}
	if result == nil || result.Trace == nil || result.Trace.Error == "" || !strings.Contains(result.Trace.Error, "transport failed") {
		t.Fatalf("result trace error = %#v, want copied terminal error", result)
	}
	if got := result.Trace.Attempts[1].Trace.Error; got == "" || !strings.Contains(got, "transport failed") {
		t.Fatalf("second attempt trace error = %q, want copied terminal error", got)
	}
}

func TestFrameworkRunNilTraceInvokeErrorKeepsPriorTraceMarkedIncomplete(t *testing.T) {
	firstTrace := retentionFrameworkTrace("definition-1", "resp-invalid", 10, 1, "")
	invoker := &retentionFrameworkInvoker{calls: []retentionFrameworkInvokerCall{
		{result: retentionFrameworkToolResult(`{"ok":false}`, firstTrace), trace: firstTrace},
		{result: retentionFrameworkToolResult(`{"ok":true}`, nil), trace: nil, err: errors.New("lost trace")},
	}}
	def := &retentionDefinition{}
	framework := NewFramework(invoker, nil)

	result, err := framework.Run(context.Background(), def, RunOpts{MaxRetries: 2})
	if err == nil {
		t.Fatal("Run error = nil, want invoke failure")
	}
	if result == nil || !result.Incomplete || result.Value != nil {
		t.Fatalf("result = %#v, want fail-closed incomplete", result)
	}
	if result.Trace == nil || result.Trace.ID != "definition-1" {
		t.Fatalf("trace = %#v, want prior trace retained", result.Trace)
	}
	if !strings.Contains(result.Trace.Error, "lost trace") {
		t.Fatalf("trace error = %q, want terminal nil-trace failure projected", result.Trace.Error)
	}
	if firstTrace.Error != "" {
		t.Fatalf("caller-owned prior trace mutated error = %q, want blank", firstTrace.Error)
	}
}

func TestFrameworkRunSuccessfulNilTraceRetryKeepsPriorTrace(t *testing.T) {
	firstTrace := retentionFrameworkTrace("definition-1", "resp-invalid", 10, 1, "")
	invoker := &retentionFrameworkInvoker{calls: []retentionFrameworkInvokerCall{
		{result: retentionFrameworkToolResult(`{"ok":false}`, firstTrace), trace: firstTrace},
		{result: retentionFrameworkToolResult(`{"ok":true}`, nil), trace: nil},
	}}
	def := &retentionDefinition{}
	framework := NewFramework(invoker, nil)

	result, err := framework.Run(context.Background(), def, RunOpts{MaxRetries: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Value != true || result.Incomplete {
		t.Fatalf("result = %#v, want successful value with retained prior trace", result)
	}
	if result.Attempts != 2 || result.PrimaryAttempts != 2 {
		t.Fatalf("attempt counts = %d/%d, want truthful two-call success", result.Attempts, result.PrimaryAttempts)
	}
	if result.Trace == nil || result.Trace.ID != "definition-1" {
		t.Fatalf("trace = %#v, want prior nonnil trace retained", result.Trace)
	}
}

func retentionFrameworkToolResult(args string, trace *transparency.Trace) *Result {
	return &Result{
		ToolCall: &tools.ToolCall{ID: "call", Name: "retain_tool", Arguments: json.RawMessage(args)},
		Trace:    trace,
	}
}

func retentionFrameworkTrace(id, responseID string, input, output int, traceErr string) *transparency.Trace {
	trace := transparency.NewTraceBuilder(id, "gpt-request", "openai").
		WithContent("public output").
		WithModelExecutions([]transparency.ExecutionIdentityTrace{{
			RequestedModel: "gpt-request",
			SelectedModel:  "gpt-selected",
			ProviderID:     "openai",
			ResponseModel:  "wire",
			ResponseID:     responseID,
		}})
	if traceErr != "" {
		trace.WithError(errors.New(traceErr))
	}
	return trace.Complete(transparency.TokenUsage{Input: input, Output: output}, 0)
}
