package main

import "testing"

func TestSanitizeTerminalInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean input unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "cursor position report removed",
			input: "hello\x1b[67;3Rworld",
			want:  "helloworld",
		},
		{
			name:  "multiple CPR sequences removed",
			input: "\x1b[67;3R\x1b[67;11Rhello",
			want:  "hello",
		},
		{
			name:  "color codes removed",
			input: "\x1b[31mred text\x1b[0m",
			want:  "red text",
		},
		{
			name:  "mixed escape sequences",
			input: "start\x1b[67;3R middle\x1b[0m end",
			want:  "start middle end",
		},
		{
			name:  "empty after sanitization",
			input: "\x1b[67;3R\x1b[67;11R",
			want:  "",
		},
		{
			name:  "preserves newlines",
			input: "line1\nline2\x1b[0m\nline3",
			want:  "line1\nline2\nline3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTerminalInput(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeTerminalInput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
