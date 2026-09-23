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

func TestAPIError_ErrorExplainsOpenRouterPolicyFiltering(t *testing.T) {
	err := (&APIError{
		StatusCode: 404,
		Message:    "No endpoints available matching your guardrail restrictions and data policy",
	}).Error()
	if !strings.Contains(err, "OpenRouter filtered every eligible endpoint") {
		t.Fatalf("policy-filtered error omitted remediation: %q", err)
	}
	if !strings.Contains(err, "Settings > Privacy and Guardrails") {
		t.Fatalf("policy-filtered error omitted account guidance: %q", err)
	}
}

func TestAPIError_ErrorDoesNotMisclassifyOrdinaryNotFound(t *testing.T) {
	err := (&APIError{StatusCode: 404, Message: "model not found"}).Error()
	if strings.Contains(err, "Settings > Privacy and Guardrails") {
		t.Fatalf("ordinary not-found error received policy guidance: %q", err)
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

func TestModelInfoUnmarshalJSONPromotesOpenRouterCompletionLimit(t *testing.T) {
	var info ModelInfo
	if err := json.Unmarshal([]byte(`{
		"id":"qwen/qwen3.8-max",
		"context_length":1000000,
		"top_provider":{"max_completion_tokens":131072}
	}`), &info); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if info.ID != "qwen/qwen3.8-max" || info.ContextLength != 1000000 {
		t.Fatalf("model info = %#v", info)
	}
	if info.MaxCompletionTokens != 131072 {
		t.Fatalf("MaxCompletionTokens = %d, want 131072", info.MaxCompletionTokens)
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
	if msg.ReasoningContent {
		t.Fatal("ReasoningContent = true, want false for canonical reasoning")
	}
}

func TestMessage_UnmarshalJSONCapturesReasoningContentAlias(t *testing.T) {
	var msg Message
	raw := `{"role":"assistant","reasoning_content":"private chain","content":"done"}`
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Reasoning != "private chain" || !msg.ReasoningContent {
		t.Fatalf("message reasoning = %q native=%v, want native alias", msg.Reasoning, msg.ReasoningContent)
	}
}

func TestMessageDelta_UnmarshalJSONCapturesReasoningContentAlias(t *testing.T) {
	var chunk StreamChunk
	raw := `{"id":"chatcmpl-1","model":"glm","choices":[{"index":0,"delta":{"reasoning_content":"private chain","content":"final"},"finish_reason":null}]}`
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(chunk.Choices))
	}
	delta := chunk.Choices[0].Delta
	if delta.Reasoning != "private chain" {
		t.Fatalf("reasoning = %q, want alias content", delta.Reasoning)
	}
	if !delta.ReasoningContent {
		t.Fatal("ReasoningContent = false, want true for reasoning_content alias")
	}
	if delta.Content != "final" {
		t.Fatalf("content = %q, want final", delta.Content)
	}
}

func TestMessageDelta_UnmarshalJSONKeepsCanonicalReasoningMarkerFalse(t *testing.T) {
	var chunk StreamChunk
	raw := `{"id":"chatcmpl-1","model":"glm","choices":[{"index":0,"delta":{"reasoning":"private chain"},"finish_reason":null}]}`
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delta := chunk.Choices[0].Delta
	if delta.Reasoning != "private chain" {
		t.Fatalf("reasoning = %q, want canonical reasoning", delta.Reasoning)
	}
	if delta.ReasoningContent {
		t.Fatal("ReasoningContent = true, want false for canonical reasoning")
	}
}

func TestMessageDelta_UnmarshalJSONReasoningFieldWinsOverAlias(t *testing.T) {
	var delta MessageDelta
	raw := `{"reasoning":"canonical","reasoning_content":"alias","content":"done"}`
	if err := json.Unmarshal([]byte(raw), &delta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if delta.Reasoning != "canonical" {
		t.Fatalf("reasoning = %q, want canonical reasoning field", delta.Reasoning)
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

// TestChoice_UnmarshalJSONCapturesNativeFinishReason covers the OpenRouter
// early-200 transport failure shell from the stealth/ox-alpha incident:
// choices[0].native_finish_reason is "network_error" beside the normalized
// finish_reason, and must survive decoding so callers can classify it.
func TestChoice_UnmarshalJSONCapturesNativeFinishReason(t *testing.T) {
	raw := `{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"stop","native_finish_reason":"network_error"}`
	var choice Choice
	if err := json.Unmarshal([]byte(raw), &choice); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if choice.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", choice.FinishReason)
	}
	if choice.NativeFinishReason != "network_error" {
		t.Fatalf("native_finish_reason = %q, want network_error", choice.NativeFinishReason)
	}
}

// TestChatResponse_UsagePresent covers the three shapes that matter for
// distinguishing an OpenRouter early-200 transport failure shell (no usage
// object at all) from an honest, literally-zero usage object, and from
// Buckley's own durable evidence envelope re-marshaling a response that
// already carries an explicit usage_present flag.
func TestChatResponse_UsagePresent(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "usage key absent -- the transport failure shell",
			raw:  `{"id":"gen-1","choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"stop","native_finish_reason":"network_error"}]}`,
			want: false,
		},
		{
			name: "usage key null",
			raw:  `{"id":"gen-1","choices":[],"usage":null}`,
			want: false,
		},
		{
			name: "usage key present with literal zero fields",
			raw:  `{"id":"gen-1","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			want: true,
		},
		{
			name: "usage key present with nonzero fields",
			raw:  `{"id":"gen-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			want: true,
		},
		{
			name: "explicit usage_present false wins over a re-marshaled usage key",
			raw:  `{"id":"gen-1","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0},"usage_present":false}`,
			want: false,
		},
		{
			name: "explicit usage_present true is trusted even with zero usage",
			raw:  `{"id":"gen-1","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0},"usage_present":true}`,
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp ChatResponse
			if err := json.Unmarshal([]byte(tt.raw), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.UsagePresent != tt.want {
				t.Fatalf("UsagePresent = %v, want %v", resp.UsagePresent, tt.want)
			}
		})
	}
}

// TestChatResponse_UsagePresentRoundTripsThroughMarshal covers the case that
// motivated the explicit usage_present-key precedence rule: Buckley's own
// durable evidence envelope always re-marshals a ChatResponse, which always
// emits a literal "usage" object (Usage has no omitempty) regardless of
// whether the original wire response ever had one. Without trusting the
// explicit key on the second decode, every replayed absent-usage response
// would silently flip to "present" on replay.
func TestChatResponse_UsagePresentRoundTripsThroughMarshal(t *testing.T) {
	original := ChatResponse{
		Choices:      []Choice{{Message: Message{Role: "assistant"}, NativeFinishReason: "network_error"}},
		UsagePresent: false,
	}
	blob, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"usage":{`) {
		t.Fatalf("expected the re-marshal to always emit a literal usage object, got: %s", blob)
	}
	var decoded ChatResponse
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.UsagePresent {
		t.Fatalf("UsagePresent = true after round-trip, want false to survive despite the re-marshaled usage object")
	}
	if decoded.Choices[0].NativeFinishReason != "network_error" {
		t.Fatalf("native_finish_reason did not round-trip: %+v", decoded.Choices[0])
	}
}

func TestChatResponse_AttemptEvidenceRoundTripsWithoutText(t *testing.T) {
	secret := "SENTINEL-SECRET-PROMPT-TEXT"
	evidence := []ModelAttemptEvidence{
		{
			Usage: Usage{
				PromptTokens:     120,
				CompletionTokens: 45,
				TotalTokens:      165,
				PromptTokensDetails: &PromptTokensDetails{
					CachedTokens: 30,
				},
				CompletionTokenDetails: &CompletionTokenDetails{
					ReasoningTokens: 12,
				},
				CacheWriteTokens: 7,
			},
			UsagePresent: true,
			FinishReason: "stop",
			Incomplete:   false,
		},
		{
			Usage: Usage{
				PromptTokens:     0,
				CompletionTokens: 0,
				TotalTokens:      0,
			},
			UsagePresent: false,
			FinishReason: "length",
			Incomplete:   true,
		},
	}
	original := ChatResponse{
		ID:              "resp-1",
		Model:           "test-model",
		AttemptEvidence: evidence,
	}
	blob, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ChatResponse
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded.AttemptEvidence, original.AttemptEvidence) {
		t.Fatalf("attempt evidence did not round-trip: got %+v, want %+v", decoded.AttemptEvidence, original.AttemptEvidence)
	}
	var raw struct {
		AttemptEvidence []map[string]json.RawMessage `json:"attempt_evidence"`
	}
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if len(raw.AttemptEvidence) != 2 {
		t.Fatalf("expected 2 attempt evidence records, got %d", len(raw.AttemptEvidence))
	}
	allowed := map[string]bool{"usage": true, "usage_present": true, "finish_reason": true, "incomplete": true}
	for i, obj := range raw.AttemptEvidence {
		for k := range obj {
			if !allowed[k] {
				t.Fatalf("attempt evidence %d has disallowed key %q", i, k)
			}
		}
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("encoded JSON contains sentinel secret text")
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

func TestChatRequest_MarshalIncludesUsageRequestOptions(t *testing.T) {
	req := ChatRequest{
		Model:    "openai/gpt-6-luna-pro",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Usage:    &UsageRequestOptions{Include: true},
	}
	blob, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"usage":{"include":true}`) {
		t.Fatalf("expected usage.include in %s", blob)
	}
}

// TestUsage_UnmarshalOpenRouterBYOKCostShape covers C7 against the exact,
// live-verified OpenRouter response shape for a BYOK
// (bring-your-own-key) call (openai/gpt-6-luna-pro via OpenRouter, the
// upstream key billed directly to OpenAI):
//
//	"usage": {
//	  "prompt_tokens": 1396, "completion_tokens": 41, "total_tokens": 1437,
//	  "cost": 0, "is_byok": true,
//	  "cost_details": {
//	    "upstream_inference_cost": 0.0001601,
//	    "upstream_inference_prompt_cost": 0.0001396,
//	    "upstream_inference_completions_cost": 2.05e-05
//	  }
//	}
func TestUsage_UnmarshalOpenRouterBYOKCostShape(t *testing.T) {
	raw := `{
		"prompt_tokens": 1396, "completion_tokens": 41, "total_tokens": 1437,
		"cost": 0, "is_byok": true,
		"prompt_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0},
		"cost_details": {
			"upstream_inference_cost": 0.0001601,
			"upstream_inference_prompt_cost": 0.0001396,
			"upstream_inference_completions_cost": 2.05e-05
		},
		"completion_tokens_details": {"reasoning_tokens": 14}
	}`
	var usage Usage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if usage.Cost == nil || *usage.Cost != 0 {
		t.Fatalf("Cost = %v, want 0", usage.Cost)
	}
	if !usage.IsBYOK {
		t.Fatal("IsBYOK = false, want true")
	}
	if usage.CostDetails == nil || usage.CostDetails.UpstreamInferenceCost != 0.0001601 {
		t.Fatalf("CostDetails = %+v, want UpstreamInferenceCost 0.0001601", usage.CostDetails)
	}

	total, known := usage.TotalCostUSD()
	if !known {
		t.Fatal("TotalCostUSD() known = false, want true")
	}
	if total != 0.0001601 {
		t.Fatalf("TotalCostUSD() = %v, want 0.0001601 (OpenRouter fee 0 + upstream 0.0001601)", total)
	}
}

func TestUsage_TotalCostUSD(t *testing.T) {
	zero := 0.0
	fee := 0.002
	tests := []struct {
		name      string
		usage     Usage
		wantTotal float64
		wantKnown bool
	}{
		{name: "no cost reported", usage: Usage{}, wantKnown: false},
		{
			name:      "non-BYOK cost is authoritative alone",
			usage:     Usage{Cost: &fee},
			wantTotal: 0.002,
			wantKnown: true,
		},
		{
			name: "BYOK adds upstream cost to the (often zero) fee",
			usage: Usage{
				Cost:        &zero,
				IsBYOK:      true,
				CostDetails: &UsageCostDetails{UpstreamInferenceCost: 0.0001601},
			},
			wantTotal: 0.0001601,
			wantKnown: true,
		},
		{
			name: "BYOK without cost_details falls back to the fee alone",
			usage: Usage{
				Cost:   &fee,
				IsBYOK: true,
			},
			wantTotal: 0.002,
			wantKnown: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, known := tt.usage.TotalCostUSD()
			if known != tt.wantKnown {
				t.Fatalf("known = %v, want %v", known, tt.wantKnown)
			}
			if known && total != tt.wantTotal {
				t.Fatalf("total = %v, want %v", total, tt.wantTotal)
			}
		})
	}
}
