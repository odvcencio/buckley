package model

import (
	"fmt"
	"testing"
)

func TestIsContextLengthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "openai code", err: &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "request failed"}, want: true},
		{name: "openrouter wording", err: fmt.Errorf("provider error: maximum context length is 131072 tokens"), want: true},
		{name: "anthropic wording", err: fmt.Errorf("prompt is too long: 201000 tokens"), want: true},
		{name: "unrelated bad request", err: &APIError{StatusCode: 400, Code: "invalid_request", Message: "missing model"}, want: false},
		{name: "rate limit", err: fmt.Errorf("HTTP 429: too many requests"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContextLengthError(tt.err); got != tt.want {
				t.Fatalf("IsContextLengthError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
