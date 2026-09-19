package model

import "testing"

func TestIsTruncatedFinishReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   bool
	}{
		{name: "length", reason: "length", want: true},
		{name: "max tokens", reason: "max_tokens", want: true},
		{name: "max output tokens", reason: "max_output_tokens", want: true},
		{name: "trimmed lowercase", reason: "  MAX_TOKENS\t", want: true},
		{name: "stop", reason: "stop", want: false},
		{name: "tool calls", reason: "tool_calls", want: false},
		{name: "empty", reason: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTruncatedFinishReason(tt.reason); got != tt.want {
				t.Fatalf("IsTruncatedFinishReason(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}
