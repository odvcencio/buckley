package commitmsg

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRepairHeader_Format(t *testing.T) {
	tests := []struct{ name, scope, subject, wantScope, wantSubject string }{
		{"incident", "project", "keep " + strings.Repeat("x", 54), "", "keep " + strings.Repeat("x", 54)},
		{"scope only", strings.Repeat("scope", 20), "keep evidence", "", "keep evidence"},
		{"unicode fits", "", strings.Repeat("界", 67), "", strings.Repeat("界", 67)},
		{"unicode cut", "", strings.Repeat("界", 80), "", strings.Repeat("界", 67)},
		{"word boundary", "", strings.Repeat("word ", 16) + "end", "", strings.TrimSpace(strings.Repeat("word ", 13))},
		{"articles first", "", "keep the " + strings.Repeat("x", 60), "", "keep " + strings.Repeat("x", 60)},
		{"trailing qualifier", "", "keep " + strings.Repeat("x", 60) + " for now", "", "keep " + strings.Repeat("x", 60)},
		{"conjunction", "", "keep " + strings.Repeat("x", 54) + " and another component", "", "keep " + strings.Repeat("x", 54)},
		{"spaced period", "", "Keep the text .", "", "keep the text"},
		{"format", " project\t", "  Keep\t the   text. ", "project", "keep the text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "incident" && utf8.RuneCountInString(composeHeader("fix", tt.scope, tt.subject)) != 73 {
				t.Fatal("incident fixture must be 73 runes")
			}
			scope, subject, _ := RepairHeader("fix", tt.scope, tt.subject, HeaderLimit)
			if scope != tt.wantScope || subject != tt.wantSubject {
				t.Fatalf("got (%q, %q), want (%q, %q)", scope, subject, tt.wantScope, tt.wantSubject)
			}
			if err := ValidateCommitFields("fix", scope, subject, []string{"Keep details"}, nil); err != nil {
				t.Fatal(err)
			}
			if !utf8.ValidString(subject) {
				t.Fatal("invalid UTF-8")
			}
		})
	}
}

func TestRepairHeader_NonMechanical(t *testing.T) {
	for _, tt := range []struct{ name, action, subject string }{
		{"type only", "fix", ""},
		{"control character", "fix", strings.Repeat("word ", 30) + "\x00"},
		{"unknown type", "ship", strings.Repeat("word ", 30)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scope, subject, repairs := RepairHeader(tt.action, "", tt.subject, HeaderLimit)
			if subject != tt.subject || len(repairs) != 0 {
				t.Fatal("invented semantic content")
			}
			if ValidateCommitFields(tt.action, scope, subject, []string{"Details"}, nil) == nil {
				t.Fatal("accepted missing or invalid semantic content")
			}
		})
	}
}
