package model

import (
	"encoding/json"
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

func TestMessage_MarshalJSONOmitsReasoning(t *testing.T) {
	msg := Message{
		Role:      "assistant",
		Content:   "add: thing",
		Reasoning: "should not be sent",
	}
	blob, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(blob) {
		t.Fatalf("expected valid JSON, got %q", string(blob))
	}
	if strings.Contains(string(blob), "reasoning") {
		t.Fatalf("unexpected reasoning field present: %s", string(blob))
	}
	if string(blob) != `{"role":"assistant","content":"add: thing"}` {
		t.Fatalf("json = %q, want %q", string(blob), `{"role":"assistant","content":"add: thing"}`)
	}
}
