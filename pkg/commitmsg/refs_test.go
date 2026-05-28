package commitmsg

import "testing"

func TestNeutralizeCloseDirectives(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"closes", "Closes #14", "Refs #14"},
		{"lowercase closes", "closes #14", "Refs #14"},
		{"fixes", "Fixes #9", "Refs #9"},
		{"fix no s", "fix #9", "Refs #9"},
		{"fixed", "fixed #9", "Refs #9"},
		{"resolves", "Resolves #200", "Refs #200"},
		{"resolved", "resolved #200", "Refs #200"},
		{"closed", "closed #1", "Refs #1"},
		{"colon form", "Closes: #14", "Refs #14"},
		{"inside bullet", "- Closes #14 from the roadmap", "- Refs #14 from the roadmap"},
		{"mid sentence", "This closes #14 finally", "This Refs #14 finally"},
		// Prose that must NOT be rewritten (no "#number" after the keyword).
		{"prose fixes", "fixes a memory leak", "fixes a memory leak"},
		{"prose closes", "closes the connection pool", "closes the connection pool"},
		{"prose resolve", "resolve the ambiguity", "resolve the ambiguity"},
		{"bare reference untouched", "see #14 for context", "see #14 for context"},
		{"roadmap attribution untouched", "(roadmap: #14)", "(roadmap: #14)"},
		{"empty", "", ""},
		{"multiple", "Closes #1 and fixes #2", "Refs #1 and Refs #2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeutralizeCloseDirectives(tc.in); got != tc.want {
				t.Errorf("NeutralizeCloseDirectives(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNeutralizeNeverEmitsCloseKeyword(t *testing.T) {
	// Whatever the model produces, the result must never carry a live
	// GitHub close directive.
	inputs := []string{
		"Closes #14", "fixes #9", "Resolves #1", "- closed #7 in passing",
		"Fixes #1, closes #2, resolves #3",
	}
	for _, in := range inputs {
		got := NeutralizeCloseDirectives(in)
		if closeDirective.MatchString(got) {
			t.Errorf("output still contains a close directive: %q -> %q", in, got)
		}
	}
}

func TestIssueRefLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"14", "Refs #14"},
		{"#14", "Refs #14"},
		{"##14", "Refs #14"},
		{"200", "Refs #200"},
	}
	for _, tc := range cases {
		if got := IssueRefLine(tc.in); got != tc.want {
			t.Errorf("IssueRefLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
