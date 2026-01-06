package main

import "testing"

func TestShouldNudgeForTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "search intent",
			input: "I'll search the codebase for the config.",
			want:  true,
		},
		{
			name:  "check intent",
			input: "Let me check the files and see.",
			want:  true,
		},
		{
			name:  "run intent",
			input: "I will run tests to verify.",
			want:  true,
		},
		{
			name:  "plain answer",
			input: "Here is the answer to your question.",
			want:  false,
		},
		{
			name:  "intent without action",
			input: "I'll be brief and direct.",
			want:  false,
		},
		{
			name:  "no intent",
			input: "This is a fast model.",
			want:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldNudgeForTools(tc.input); got != tc.want {
				t.Fatalf("shouldNudgeForTools(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
