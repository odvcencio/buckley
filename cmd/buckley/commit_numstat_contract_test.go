package main

import (
	"strings"
	"testing"
)

func TestParseCommitNumstatContract(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantN  int
		wantOK bool
	}{
		{name: "zero", input: "0", wantN: 0, wantOK: true},
		{name: "positive integer", input: "42", wantN: 42, wantOK: true},
		{name: "surrounding whitespace", input: "  7 \t", wantN: 7, wantOK: true},
		{name: "empty input", input: "", wantN: 0, wantOK: false},
		{name: "binary marker", input: "-", wantN: 0, wantOK: false},
		{name: "negative input", input: "-3", wantN: 0, wantOK: false},
		{name: "fractional input", input: "3.5", wantN: 0, wantOK: false},
		{name: "nonnumeric input", input: "abc", wantN: 0, wantOK: false},
		{name: "whitespace-only input", input: " \t", wantN: 0, wantOK: false},
		{
			name:  "100-digit overflow",
			input: strings.Repeat("9", 100),
			wantN: 0, wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := parseCommitNumstat(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if n != tt.wantN {
				t.Fatalf("n = %d, want %d", n, tt.wantN)
			}
		})
	}
}
