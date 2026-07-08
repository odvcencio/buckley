package rlm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// These fixtures mirror real payloads observed from reasoning models
// (GLM-5.2, Qwen agentic checkpoints) routed through OpenRouter/vLLM, where
// the tool call arrives as text inside the assistant message content
// instead of the OpenAI-standard structured `tool_calls` field. Without
// resolveSubAgentToolCalls, subagent.go's Execute loop treats this raw text
// as the final answer (see the `len(choice.Message.ToolCalls) == 0` branch
// prior to this fix) -- so a reasoning model's tool call becomes the
// sub-agent's "summary" verbatim, which is the exact bug behind `buckley
// review` leaking a raw tool-call payload as its verdict.
const (
	rlmQwenHermesToolCallPayload = "<tool_call>\n" +
		`{"name": "get_weather", "arguments": {"location": "San Francisco", "unit": "celsius"}}` +
		"\n</tool_call>"

	rlmGLMNativeArgTagPayload = "<tool_call>read_file\n" +
		"<arg_key>path</arg_key>\n" +
		"<arg_value>pkg/rlm/subagent.go</arg_value>\n" +
		"</tool_call>"

	rlmGLMJSONFencePayload = "I'll write the file now.\n\n" +
		"```json\n" +
		`{"name": "write_file", "arguments": {"path": "notes.md", "content": "hello world"}}` +
		"\n```\n"

	// rlmGLMNumericQuirkPayload reproduces the exact live bug: GLM-5.2
	// tool-call argument JSON with a stray space injected right after a
	// leading '-' in a numeric literal -- the one encoding/json scanner
	// state that emits the literal reported error text "invalid character
	// ' ' in numeric literal" (see pkg/jsonrepair for the full analysis).
	rlmGLMNumericQuirkPayload = "<tool_call>\n" +
		`{"name": "generate_review", "arguments": {"verdict": "approve", "confidence": - 5}}` +
		"\n</tool_call>"
)

func TestResolveSubAgentToolCalls_StructuredPassthrough(t *testing.T) {
	msg := model.Message{
		Role: "assistant",
		ToolCalls: []model.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
		}},
	}

	calls, unparsable, reason := resolveSubAgentToolCalls(msg)
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 || calls[0].Function.Name != "read_file" {
		t.Fatalf("expected structured call passed through unchanged, got %+v", calls)
	}
}

func TestResolveSubAgentToolCalls_HermesTag(t *testing.T) {
	msg := model.Message{Role: "assistant", Content: rlmQwenHermesToolCallPayload}

	calls, unparsable, reason := resolveSubAgentToolCalls(msg)
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 recovered call, got %d", len(calls))
	}
	if calls[0].Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q, want %q", calls[0].Function.Name, "get_weather")
	}
}

func TestResolveSubAgentToolCalls_GLMNativeTag(t *testing.T) {
	msg := model.Message{Role: "assistant", Content: rlmGLMNativeArgTagPayload}

	calls, unparsable, reason := resolveSubAgentToolCalls(msg)
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 || calls[0].Function.Name != "read_file" {
		t.Fatalf("expected 1 recovered read_file call, got %+v", calls)
	}
}

func TestResolveSubAgentToolCalls_JSONFence(t *testing.T) {
	msg := model.Message{Role: "assistant", Content: rlmGLMJSONFencePayload}

	calls, unparsable, reason := resolveSubAgentToolCalls(msg)
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 || calls[0].Function.Name != "write_file" {
		t.Fatalf("expected 1 recovered write_file call, got %+v", calls)
	}
}

// TestResolveSubAgentToolCalls_NumericLiteralQuirk proves the sub-agent
// decision function recovers a dispatchable call with valid JSON arguments
// even when GLM's tool-call text has the stray-whitespace-in-numeric-literal
// quirk that otherwise fails downstream with "invalid character ' ' in
// numeric literal".
func TestResolveSubAgentToolCalls_NumericLiteralQuirk(t *testing.T) {
	msg := model.Message{Role: "assistant", Content: rlmGLMNumericQuirkPayload}

	calls, unparsable, reason := resolveSubAgentToolCalls(msg)
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 recovered call, got %d", len(calls))
	}

	var args struct {
		Verdict    string  `json:"verdict"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("recovered arguments are not valid JSON after repair: %v (arguments=%q)", err, calls[0].Function.Arguments)
	}
	if args.Confidence != -5 {
		t.Errorf("Confidence = %v, want -5", args.Confidence)
	}
}

func TestResolveSubAgentToolCalls_DetectedButUnparsable(t *testing.T) {
	msg := model.Message{
		Role:    "assistant",
		Content: `<tool_call>{"name": "foo", "arguments": {not valid json}}</tool_call>`,
	}

	calls, unparsable, reason := resolveSubAgentToolCalls(msg)
	if !unparsable {
		t.Fatalf("expected unparsable=true for a tagged-but-malformed payload")
	}
	if len(calls) != 0 {
		t.Fatalf("expected no calls, got %d", len(calls))
	}
	if reason == "" {
		t.Errorf("expected a non-empty reason")
	}
}

func TestResolveSubAgentToolCalls_NoFalsePositiveOnPlainText(t *testing.T) {
	msg := model.Message{Role: "assistant", Content: "I read the file and everything looks correct."}

	calls, unparsable, reason := resolveSubAgentToolCalls(msg)
	if unparsable {
		t.Errorf("expected unparsable=false for plain text, reason=%q", reason)
	}
	if len(calls) != 0 {
		t.Errorf("expected no calls for plain text, got %d", len(calls))
	}
}

// TestResolveSubAgentToolCalls_RegressionWithoutParser documents the exact
// bug this fix addresses and proves the fallback recovers what the "old"
// code path would otherwise have surfaced verbatim as result.Summary.
func TestResolveSubAgentToolCalls_RegressionWithoutParser(t *testing.T) {
	oldPathSummary, err := model.ExtractTextContent(rlmQwenHermesToolCallPayload)
	if err != nil {
		t.Fatalf("unexpected error extracting raw text: %v", err)
	}
	if !strings.Contains(oldPathSummary, "<tool_call>") {
		t.Fatalf("sanity check failed: expected the old code path to still contain the raw tag, got %q", oldPathSummary)
	}

	calls, unparsable, reason := resolveSubAgentToolCalls(model.Message{Role: "assistant", Content: rlmQwenHermesToolCallPayload})
	if unparsable || len(calls) != 1 {
		t.Fatalf("parser failed to recover the tool call that would otherwise leak into result.Summary: unparsable=%v calls=%d reason=%q", unparsable, len(calls), reason)
	}
}

// echoTool is a minimal tool.Tool implementation that reports back whatever
// arguments it was called with, so tests can assert executeTools correctly
// unmarshaled (and, where relevant, repaired) the arguments JSON before
// dispatch.
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echoes back its arguments" }
func (echoTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (echoTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true, Data: params}, nil
}

// TestExecuteTools_RepairsStructuredNumericLiteralQuirk proves executeTools
// (the dispatch path used for BOTH structured API tool calls and
// text-fallback-recovered calls) tolerates GLM's stray-whitespace numeric
// literal quirk arriving directly in a *structured* model.ToolCall -- i.e.
// a call that never went through resolveSubAgentToolCalls' text-parsing
// fallback at all, only the jsonrepair.TryUnmarshal defense-in-depth inside
// executeTools itself.
func TestExecuteTools_RepairsStructuredNumericLiteralQuirk(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(echoTool{})

	agent := &SubAgent{id: "test-agent"}
	result := &SubAgentResult{}

	calls := []model.ToolCall{{
		ID:       "call_1",
		Type:     "function",
		Function: model.FunctionCall{Name: "echo", Arguments: `{"confidence": - 5}`},
	}}

	toolResults, err := agent.executeTools(context.Background(), calls, registry, map[string]struct{}{"echo": {}}, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toolResults) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(toolResults))
	}
	if !toolResults[0].Success {
		t.Fatalf("expected successful dispatch despite malformed numeric literal, got: %s", toolResults[0].Result)
	}
	if !strings.Contains(toolResults[0].Result, "-5") {
		t.Errorf("expected repaired confidence value -5 to appear in dispatched result, got: %s", toolResults[0].Result)
	}
}

// TestExecuteTools_TrulyInvalidArgumentsStillFail proves the
// jsonrepair.TryUnmarshal defense-in-depth doesn't mask genuinely broken
// argument JSON -- it must still be reported as a failed tool call, not
// silently dropped or panic.
func TestExecuteTools_TrulyInvalidArgumentsStillFail(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(echoTool{})

	agent := &SubAgent{id: "test-agent"}
	result := &SubAgentResult{}

	calls := []model.ToolCall{{
		ID:       "call_1",
		Type:     "function",
		Function: model.FunctionCall{Name: "echo", Arguments: `{not json at all`},
	}}

	toolResults, err := agent.executeTools(context.Background(), calls, registry, map[string]struct{}{"echo": {}}, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toolResults) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(toolResults))
	}
	if toolResults[0].Success {
		t.Fatalf("expected failed dispatch for genuinely malformed JSON, got success")
	}
	if !strings.Contains(toolResults[0].Result, "invalid arguments") {
		t.Errorf("expected an 'invalid arguments' failure message, got: %s", toolResults[0].Result)
	}
}
