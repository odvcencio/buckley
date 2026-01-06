package rlm

import (
	"context"
	"errors"
	"testing"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
	"github.com/odvcencio/buckley/pkg/transparency"
)

type fakeInvoker struct {
	results []*oneshot.Result
	traces  []*transparency.Trace
	errs    []error
	calls   int
}

func (f *fakeInvoker) Invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	idx := f.calls
	f.calls++
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	var trace *transparency.Trace
	if idx < len(f.traces) {
		trace = f.traces[idx]
	}
	var err error
	if idx < len(f.errs) {
		err = f.errs[idx]
	}
	return f.results[idx], trace, err
}

func TestInvokeToolLoopReturnsToolCall(t *testing.T) {
	invoker := &fakeInvoker{
		results: []*oneshot.Result{
			{TextContent: "no tool"},
			{ToolCall: &tools.ToolCall{Name: "do_thing"}},
		},
		traces: []*transparency.Trace{{}, {}},
	}

	tool := tools.Definition{Name: "do_thing"}
	result, _, err := InvokeToolLoop(context.Background(), invoker, "system", "user", tool, nil, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.ToolCall == nil {
		t.Fatalf("expected tool call result")
	}
	if invoker.calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", invoker.calls)
	}
}

func TestInvokeToolLoopReturnsTextWhenNoToolCall(t *testing.T) {
	invoker := &fakeInvoker{
		results: []*oneshot.Result{
			{TextContent: "first"},
			{TextContent: "second"},
		},
		traces: []*transparency.Trace{{}, {}},
	}

	tool := tools.Definition{Name: "do_thing"}
	result, _, err := InvokeToolLoop(context.Background(), invoker, "system", "user", tool, nil, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.ToolCall != nil {
		t.Fatalf("expected text-only result")
	}
	if result.TextContent != "second" {
		t.Fatalf("expected last text content, got %q", result.TextContent)
	}
}

func TestInvokeToolLoopReturnsError(t *testing.T) {
	invoker := &fakeInvoker{
		results: []*oneshot.Result{{TextContent: ""}},
		errs:    []error{errors.New("boom")},
	}

	tool := tools.Definition{Name: "do_thing"}
	_, _, err := InvokeToolLoop(context.Background(), invoker, "system", "user", tool, nil, 2)
	if err == nil {
		t.Fatalf("expected error")
	}
}
