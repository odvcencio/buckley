package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"m31labs.dev/buckley/pkg/jsonrepair"
)

// ---------------------------------------------------------------------------
// Fallback text tool-call parsing.
//
// Reasoning models (GLM-5.x, Qwen agentic checkpoints, etc.) routed through
// OpenRouter/vLLM don't always populate the OpenAI-standard structured
// `tool_calls` field on Choice.Message. Instead they emit the call as text
// inside the message content, typically as a Hermes/Qwen-style
// `<tool_call>{...}</tool_call>` block, GLM's native
// `<tool_call>name\n<arg_key>k</arg_key><arg_value>v</arg_value></tool_call>`
// form, or a ```json fenced `{"name":...,"arguments":...}` object. Left
// unhandled, that raw text gets treated as the model's final answer/summary
// by callers (pkg/rlm's SubAgent.Execute, pkg/oneshot's DefaultInvoker),
// which is the exact bug behind `buckley review` leaking a raw tool-call
// payload as its verdict.
//
// ParseTextToolCalls recognizes these encodings and converts them back into
// ToolCall values so callers can dispatch them exactly like a structured
// tool call returned by the API. It also repairs the common GLM/Qwen JSON
// quirk of stray whitespace inside numeric literals (jsonrepair.Repair)
// before treating an "arguments" payload as unparsable, since that quirk is
// what produces the live `unmarshal tool call: invalid character ' ' in
// numeric literal` failure once a recovered call reaches
// tools.ToolCall.Unmarshal downstream.
// ---------------------------------------------------------------------------

var (
	textToolCallTagRe = regexp.MustCompile(`(?is)<tool_call>(.*?)</tool_call>`)
	textJSONFenceRe   = regexp.MustCompile("(?is)```json\\s*(.*?)```")
	textArgPairRe     = regexp.MustCompile(`(?is)<arg_key>(.*?)</arg_key>\s*<arg_value>(.*?)</arg_value>`)
)

// TextToolCallParse is the outcome of scanning free-form model output for a
// tool call encoded as text (see ParseTextToolCalls).
type TextToolCallParse struct {
	// Calls holds successfully parsed tool calls, if any.
	Calls []ToolCall
	// Detected is true when content contains a recognizable tool-call
	// wrapper (<tool_call> tags, or a JSON tool-call object) even if it
	// could not be fully parsed. Callers must not treat the raw text as a
	// final answer when Detected is true and Calls is empty -- that
	// combination means "this looked like a tool call but we couldn't
	// dispatch it", not "there was no tool call".
	Detected bool
	// Reason explains why parsing failed when Detected is true and Calls is
	// empty.
	Reason string
}

// ParseTextToolCalls scans content emitted in an assistant message body for
// a tool call written as text rather than delivered via the structured
// tool_calls field. It supports the encodings seen in practice from
// reasoning models on OpenRouter: Hermes/Qwen-style
// `<tool_call>{json}</tool_call>` blocks (including GLM's native
// `<tool_call>name\n<arg_key>..</arg_key><arg_value>..</arg_value></tool_call>`
// form) and ```json fenced `{"name":...,"arguments":...}` objects.
func ParseTextToolCalls(content string) TextToolCallParse {
	content = strings.TrimSpace(content)
	if content == "" {
		return TextToolCallParse{}
	}

	if tags := textToolCallTagRe.FindAllStringSubmatch(content, -1); len(tags) > 0 {
		out := TextToolCallParse{Detected: true}
		for i, m := range tags {
			call, err := parseTaggedToolCall(strings.TrimSpace(m[1]), i)
			if err != nil {
				return TextToolCallParse{Detected: true, Reason: err.Error()}
			}
			out.Calls = append(out.Calls, call)
		}
		return out
	}

	if fences := textJSONFenceRe.FindAllStringSubmatch(content, -1); len(fences) > 0 {
		out := TextToolCallParse{}
		for i, m := range fences {
			call, recognized, err := parseJSONToolCall(strings.TrimSpace(m[1]), i)
			if !recognized {
				continue
			}
			out.Detected = true
			if err != nil {
				return TextToolCallParse{Detected: true, Reason: err.Error()}
			}
			out.Calls = append(out.Calls, call)
		}
		if out.Detected {
			return out
		}
	}

	// A message that is nothing but a bare JSON tool-call object (no tags or
	// fence at all).
	if strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}") {
		call, recognized, err := parseJSONToolCall(content, 0)
		if recognized {
			if err != nil {
				return TextToolCallParse{Detected: true, Reason: err.Error()}
			}
			return TextToolCallParse{Detected: true, Calls: []ToolCall{call}}
		}
	}

	return TextToolCallParse{}
}

// parseTaggedToolCall parses the inner text of a single
// <tool_call>...</tool_call> block, which is either a JSON object or GLM's
// native "name\n<arg_key>k</arg_key><arg_value>v</arg_value>..." form.
func parseTaggedToolCall(inner string, idx int) (ToolCall, error) {
	if inner == "" {
		return ToolCall{}, fmt.Errorf("tool_call #%d: empty payload", idx+1)
	}

	if strings.HasPrefix(inner, "{") {
		call, recognized, err := parseJSONToolCall(inner, idx)
		if recognized {
			if err != nil {
				return ToolCall{}, fmt.Errorf("tool_call #%d: %w", idx+1, err)
			}
			return call, nil
		}
	}

	if pairs := textArgPairRe.FindAllStringSubmatch(inner, -1); len(pairs) > 0 {
		keyIdx := strings.Index(inner, "<arg_key>")
		name := inner
		if keyIdx >= 0 {
			name = inner[:keyIdx]
		}
		if nl := strings.IndexAny(name, "\r\n"); nl >= 0 {
			name = name[:nl]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return ToolCall{}, fmt.Errorf("tool_call #%d: missing function name before <arg_key>", idx+1)
		}
		args := make(map[string]string, len(pairs))
		for _, p := range pairs {
			key := strings.TrimSpace(p[1])
			if key == "" {
				continue
			}
			args[key] = strings.TrimSpace(p[2])
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return ToolCall{}, fmt.Errorf("tool_call #%d: %w", idx+1, err)
		}
		return ToolCall{
			ID:       fmt.Sprintf("fallback-call-%d", idx+1),
			Type:     "function",
			Function: FunctionCall{Name: name, Arguments: string(encoded)},
		}, nil
	}

	return ToolCall{}, fmt.Errorf("tool_call #%d: unrecognized payload (not JSON, no <arg_key>/<arg_value> pairs)", idx+1)
}

// parseJSONToolCall attempts to interpret s as a
// {"name":...,"arguments":...} tool-call object. recognized is false when s
// isn't shaped like a tool call at all (e.g. missing a "name" field), so
// callers can skip it without treating the surrounding text as a
// detected-but-broken tool call. recognized is true with a non-nil err when
// s looks like it was meant to be a tool call but is malformed even after
// attempting jsonrepair.Repair (see below) -- e.g. GLM's stray-whitespace
// numeric literal quirk.
func parseJSONToolCall(s string, idx int) (call ToolCall, recognized bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '{' {
		return ToolCall{}, false, nil
	}

	// Reasoning models occasionally emit near-valid JSON here (most notably
	// GLM-5.x's stray-whitespace-inside-a-numeric-literal quirk, e.g.
	// `{"name": "foo", "arguments": {"confidence": - 5}}`). Repair before
	// giving up, since json.Unmarshal below would otherwise fail with e.g.
	// `invalid character ' ' in numeric literal` and the whole payload
	// would be reported as unparsable.
	repairedS := s
	if !json.Valid([]byte(s)) {
		if repaired := jsonrepair.Repair([]byte(s)); json.Valid(repaired) {
			repairedS = string(repaired)
		}
	}

	var raw map[string]json.RawMessage
	if unmarshalErr := json.Unmarshal([]byte(repairedS), &raw); unmarshalErr != nil {
		return ToolCall{}, true, fmt.Errorf("invalid JSON tool-call payload: %w", unmarshalErr)
	}

	nameRaw, hasName := raw["name"]
	if !hasName {
		return ToolCall{}, false, nil
	}
	argsRaw, hasArgs := raw["arguments"]
	if !hasArgs {
		argsRaw, hasArgs = raw["parameters"]
	}

	var name string
	if unmarshalErr := json.Unmarshal(nameRaw, &name); unmarshalErr != nil || strings.TrimSpace(name) == "" {
		return ToolCall{}, true, fmt.Errorf("tool-call payload has a non-string or empty \"name\" field")
	}

	argsJSON := "{}"
	if hasArgs {
		trimmed := strings.TrimSpace(string(argsRaw))
		if strings.HasPrefix(trimmed, `"`) {
			// arguments encoded as a JSON string containing JSON, e.g.
			// "arguments": "{\"path\":\"x\"}"
			var inner string
			if unmarshalErr := json.Unmarshal(argsRaw, &inner); unmarshalErr != nil {
				return ToolCall{}, true, fmt.Errorf("tool call %q has an unparsable arguments string: %w", name, unmarshalErr)
			}
			inner = strings.TrimSpace(inner)
			if inner == "" {
				inner = "{}"
			}
			if !json.Valid([]byte(inner)) {
				if repaired := jsonrepair.Repair([]byte(inner)); json.Valid(repaired) {
					inner = string(repaired)
				} else {
					return ToolCall{}, true, fmt.Errorf("tool call %q arguments string is not valid JSON", name)
				}
			}
			argsJSON = inner
		} else {
			if !json.Valid(argsRaw) {
				if repaired := jsonrepair.Repair(argsRaw); json.Valid(repaired) {
					argsRaw = repaired
				} else {
					return ToolCall{}, true, fmt.Errorf("tool call %q has malformed arguments", name)
				}
			}
			argsJSON = strings.TrimSpace(string(argsRaw))
		}
	}

	return ToolCall{
		ID:       fmt.Sprintf("fallback-call-%d", idx+1),
		Type:     "function",
		Function: FunctionCall{Name: name, Arguments: argsJSON},
	}, true, nil
}
