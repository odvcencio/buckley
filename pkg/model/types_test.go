package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestAPIError_IsRateLimitError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{"rate_limit", 429, true},
		{"bad_request", 400, false},
		{"unauthorized", 401, false},
		{"internal_error", 500, false},
		{"success", 200, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &APIError{StatusCode: tt.statusCode}
			got := err.IsRateLimitError()
			if got != tt.want {
				t.Errorf("IsRateLimitError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAPIError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *APIError
		expected string
	}{
		{
			name: "with_type_and_code",
			err: &APIError{
				StatusCode: 400,
				Message:    "Invalid request",
				Type:       "validation_error",
				Code:       "invalid_param",
			},
			expected: "HTTP 400: Invalid request (type: validation_error, code: invalid_param)",
		},
		{
			name: "without_type_and_code",
			err: &APIError{
				StatusCode: 500,
				Message:    "Internal error",
			},
			expected: "HTTP 500: Internal error",
		},
		{
			name: "with_type_only",
			err: &APIError{
				StatusCode: 403,
				Message:    "Forbidden",
				Type:       "permission_error",
			},
			expected: "HTTP 403: Forbidden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.expected {
				t.Errorf("Error() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestModelPricing_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name               string
		json               string
		expectedPrompt     float64
		expectedCompletion float64
		expectError        bool
	}{
		{
			name:               "string_values",
			json:               `{"prompt": "0.001", "completion": "0.002"}`,
			expectedPrompt:     1000.0, // 0.001 * 1_000_000
			expectedCompletion: 2000.0, // 0.002 * 1_000_000
			expectError:        false,
		},
		{
			name:               "float_values",
			json:               `{"prompt": 0.0000015, "completion": 0.0000025}`,
			expectedPrompt:     1.5, // 0.0000015 * 1_000_000
			expectedCompletion: 2.5, // 0.0000025 * 1_000_000
			expectError:        false,
		},
		{
			name:               "mixed_values",
			json:               `{"prompt": "0.000001", "completion": 0.000002}`,
			expectedPrompt:     1.0, // 0.000001 * 1_000_000
			expectedCompletion: 2.0, // 0.000002 * 1_000_000
			expectError:        false,
		},
		{
			name:        "invalid_string",
			json:        `{"prompt": "invalid", "completion": "0.002"}`,
			expectError: true,
		},
		{
			name:        "invalid_json",
			json:        `{invalid json}`,
			expectError: true,
		},
		{
			name:               "zero_values",
			json:               `{"prompt": 0, "completion": 0}`,
			expectedPrompt:     0.0,
			expectedCompletion: 0.0,
			expectError:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pricing ModelPricing
			err := json.Unmarshal([]byte(tt.json), &pricing)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if pricing.Prompt != tt.expectedPrompt {
				t.Errorf("Prompt = %f, want %f", pricing.Prompt, tt.expectedPrompt)
			}
			if pricing.Completion != tt.expectedCompletion {
				t.Errorf("Completion = %f, want %f", pricing.Completion, tt.expectedCompletion)
			}
		})
	}
}

func TestMessage_UnmarshalJSONCapturesReasoning(t *testing.T) {
	var msg Message
	raw := `{"role":"assistant","content":null,"reasoning":"update(deps): refresh deps"}`
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", msg.Role)
	}
	if msg.Content != nil {
		t.Fatalf("content = %#v, want nil", msg.Content)
	}
	if msg.Reasoning != "update(deps): refresh deps" {
		t.Fatalf("reasoning = %q, want %q", msg.Reasoning, "update(deps): refresh deps")
	}
}

func TestMessage_MarshalJSONPreservesReasoning(t *testing.T) {
	msg := Message{
		Role:      "assistant",
		Content:   "add: thing",
		Reasoning: "reasoned through it",
		ReasoningDetails: []ReasoningDetail{
			{
				Type:     "reasoning.text",
				Text:     "reasoned through it",
				Format:   "anthropic-claude-v1",
				HasIndex: true,
			},
		},
	}
	blob, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(blob) {
		t.Fatalf("expected valid JSON, got %q", string(blob))
	}
	if !strings.Contains(string(blob), `"reasoning":"reasoned through it"`) {
		t.Fatalf("expected reasoning field, got: %s", string(blob))
	}
	if !strings.Contains(string(blob), `"reasoning_details"`) {
		t.Fatalf("expected reasoning_details field, got: %s", string(blob))
	}
	if !strings.Contains(string(blob), `"index":0`) {
		t.Fatalf("expected zero index to be preserved, got: %s", string(blob))
	}
}

func TestReasoningDetail_RoundTripsUnknownFields(t *testing.T) {
	raw := `{"type":"reasoning.encrypted","data":"abc","signature":null,"id":"r1","format":"anthropic-claude-v1","index":0,"provider_field":{"x":1}}`
	var detail ReasoningDetail
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !detail.HasIndex || detail.Index != 0 {
		t.Fatalf("expected index presence to be preserved")
	}
	blob, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(blob)
	for _, want := range []string{`"type":"reasoning.encrypted"`, `"data":"abc"`, `"signature":null`, `"index":0`, `"provider_field":{"x":1}`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %s in %s", want, out)
		}
	}
}

// TestReasoningDetail_RoundTripPropertyOverRecordedShapes is a property test
// for the ReasoningDetail codec: for each recorded provider JSON shape,
// decode -> encode -> decode must produce a struct deeply equal to the first
// decode (semantically equal; MarshalJSON's field order need not match the
// source document's key order).
func TestReasoningDetail_RoundTripPropertyOverRecordedShapes(t *testing.T) {
	shapes := []string{
		// Plain reasoning.text with an index.
		`{"type":"reasoning.text","id":"rt_1","index":0,"text":"because gcd(p,q)=1 forces both p and q even, a contradiction"}`,
		// reasoning.summary with no index and a format tag.
		`{"type":"reasoning.summary","summary":"short recap of the proof","format":"anthropic-claude-v1"}`,
		// reasoning.encrypted with a non-null signature.
		`{"type":"reasoning.encrypted","id":"rt_2","data":"ZW5jcnlwdGVkLXBheWxvYWQ=","signature":"sig-xyz","index":3}`,
		// Explicit null signature plus an unknown provider field (the shape the
		// pre-existing TestReasoningDetail_RoundTripsUnknownFields also covers).
		`{"type":"reasoning.encrypted","data":"abc","signature":null,"id":"r1","format":"anthropic-claude-v1","index":0,"provider_field":{"x":1}}`,
		// Text needing JSON escaping: newlines, tabs, quotes, backslashes.
		`{"type":"reasoning.text","text":"multi\nline\ttext with \"quotes\" and \\ backslash","index":5}`,
		// Unicode content plus a deeply nested unknown provider field.
		`{"type":"reasoning.text","text":"unicode café and math ≤ ≥","vendor_meta":{"tier":"high","nested":{"deep":[1,2,3]}}}`,
		// No type field at all, only unknown fields (degenerate but valid shape).
		`{"custom_marker":true,"trace_id":"abc-123"}`,
	}

	for i, shape := range shapes {
		t.Run(fmt.Sprintf("shape_%d", i), func(t *testing.T) {
			var first ReasoningDetail
			if err := json.Unmarshal([]byte(shape), &first); err != nil {
				t.Fatalf("unmarshal original: %v", err)
			}

			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !json.Valid(encoded) {
				t.Fatalf("marshal produced invalid JSON: %s", encoded)
			}

			var second ReasoningDetail
			if err := json.Unmarshal(encoded, &second); err != nil {
				t.Fatalf("unmarshal re-encoded: %v", err)
			}

			if !reflect.DeepEqual(first, second) {
				t.Fatalf("round trip changed the decoded value:\n first:  %#v\n second: %#v\n encoded: %s", first, second, encoded)
			}
		})
	}
}

func TestChatRequest_MarshalsOpenRouterFields(t *testing.T) {
	parallel := true
	seed := 7
	enabled := true
	req := ChatRequest{
		Model:               "qwen/qwen3.6-max-preview",
		Models:              []string{"qwen/qwen3.6-max-preview", "qwen/qwen3.6-flash"},
		Messages:            []Message{{Role: "user", Content: "hello"}},
		MaxCompletionTokens: 128,
		ParallelToolCalls:   &parallel,
		Reasoning:           &ReasoningConfig{Enabled: &enabled, Effort: "minimal"},
		Provider:            map[string]any{"allow_fallbacks": true, "data_collection": "deny"},
		ResponseFormat:      map[string]any{"type": "json_object"},
		Seed:                &seed,
		ServiceTier:         "auto",
		SessionID:           "session-1",
		Metadata:            map[string]string{"surface": "test"},
		Trace:               map[string]string{"trace_id": "trace-1"},
		CacheControl:        &CacheControl{Type: "ephemeral", TTL: "1h"},
	}

	blob, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(blob)
	for _, want := range []string{
		`"models":["qwen/qwen3.6-max-preview","qwen/qwen3.6-flash"]`,
		`"max_completion_tokens":128`,
		`"parallel_tool_calls":true`,
		`"provider":{"allow_fallbacks":true,"data_collection":"deny"}`,
		`"response_format":{"type":"json_object"}`,
		`"service_tier":"auto"`,
		`"session_id":"session-1"`,
		`"cache_control":{"type":"ephemeral","ttl":"1h"}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %s in %s", want, out)
		}
	}
}
