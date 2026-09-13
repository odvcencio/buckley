package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsToolUnsupportedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unsupported tool lower", err: errors.New("provider does not allow unsupported tool"), want: true},
		{name: "not support tool lower", err: errors.New("tool not support"), want: true},
		{name: "unsupported tool upper", err: errors.New("UNSUPPORTED TOOL"), want: true},
		{name: "not support tool upper", err: errors.New("TOOL NOT SUPPORT"), want: true},
		{name: "tool calling not supported", err: errors.New("this model does not support tool calling"), want: true},
		{name: "tool response not supported", err: errors.New("tool response not supported by model"), want: true},
		{name: "unsupported reasoning", err: errors.New("unsupported reasoning effort for this model"), want: false},
		{name: "tool quota", err: errors.New("tool quota exceeded"), want: false},
		{name: "wrapped unsupported tool", err: fmt.Errorf("request failed: %w", errors.New("unsupported tool requested")), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsToolUnsupportedError(tt.err); got != tt.want {
				t.Errorf("IsToolUnsupportedError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}
