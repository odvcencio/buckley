package commitmsg

import (
	"reflect"
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
		{"repeated prefix", "project", "fix(project): keep the text", "project", "keep the text"},
		{"prefix before length", "project", "fix(project): keep " + strings.Repeat("x", 53), "project", "keep " + strings.Repeat("x", 53)},
		{"colon in prose", "", "http: keep parsing", "", "http: keep parsing"},
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

func TestRepairHeader_RepeatedPrefixes(t *testing.T) {
	scope, subject, repairs := RepairHeader("fix", "api", strings.Repeat("fix(api): ", 4096)+"keep the text", HeaderLimit)
	if scope != "api" || subject != "keep the text" || len(repairs) != 1 {
		t.Fatalf("scope=%q subject=%q repairs=%v", scope, subject, repairs)
	}
}

func TestRepairHeader_ConflictingRecoveredScope(t *testing.T) {
	original := "fix(api): fix(other): keep the text"
	scope, subject, repairs := RepairHeader("fix", "", original, HeaderLimit)
	if scope != "" || subject != original || len(repairs) != 0 {
		t.Fatalf("scope=%q subject=%q repairs=%v", scope, subject, repairs)
	}
}

func TestRepairBody(t *testing.T) {
	long := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }
	tests := []struct {
		name    string
		body    []string
		want    []string
		repairs int
	}{
		{"within limits", []string{"Short bullet", "Another"}, []string{"Short bullet", "Another"}, 0},
		{"cuts at a sentence end", []string{"Group generated files by directory. Then skip them in the ledger because every reviewer already sees the summary line for each group"},
			[]string{"Group generated files by directory"}, 1},
		{"cuts at a clause break", []string{"Detect generated files from headers and build output siblings, then collapse them into grouped summaries so reviewers see one line per group"},
			[]string{"Detect generated files from headers and build output siblings"}, 1},
		{"cuts at the limit and drops a dangling word", []string{long(19) + " and then more words follow here"}, []string{long(19)}, 1},
		{"drops extra bullets", []string{"a", "b", "c", "d", "e", "f"}, []string{"a", "b", "c", "d", "e"}, 1},
		{"keeps blank bullets out of the count", []string{"a", "", "b", "c", "d", "e"}, []string{"a", "", "b", "c", "d", "e"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, repairs := RepairBody(tc.body)
			if !reflect.DeepEqual(got, tc.want) || len(repairs) != tc.repairs {
				t.Fatalf("RepairBody() = %q, %v; want %q with %d repairs", got, repairs, tc.want, tc.repairs)
			}
			for _, b := range got {
				if n := len(strings.Fields(b)); n > MaxBulletWords {
					t.Fatalf("bullet %q has %d words", b, n)
				}
			}
		})
	}
}
