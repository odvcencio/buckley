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

// A cut can remove a fact: a bullet that counted "two ..., and two more ..."
// came out as "two ...". The repair now says which bullet it cut and by how
// much, so the author can check it.
func TestRepairBody_SaysWhatItCut(t *testing.T) {
	bullet := "Match C end-of-input and ill-formed UTF-8 lookahead; document two heuristics where the blob loses source detail, and two more where the scanner state is opaque"
	out, repairs := RepairBody([]string{"Keep the guard", bullet})
	if want := "Match C end-of-input and ill-formed UTF-8 lookahead; document two heuristics where the blob loses source detail"; out[1] != want {
		t.Fatalf("cut bullet = %q, want %q", out[1], want)
	}
	if len(repairs) != 1 || repairs[0] != "cut bullet 2 from 25 to 16 words; check that it still says what changed" {
		t.Fatalf("repairs = %q", repairs)
	}

	seven := []string{"one", "two", "three", "", "four", "five", "six", "seven"}
	out, repairs = RepairBody(seven)
	if len(repairs) != 1 || repairs[0] != "dropped 2 bullet(s) beyond the limit of 5" {
		t.Fatalf("repairs = %q", repairs)
	}
	if kept := len(out); kept != 6 { // five bullets and the blank entry
		t.Fatalf("kept %d entries: %q", kept, out)
	}

	if _, repairs = RepairBody([]string{"short bullet"}); len(repairs) != 0 {
		t.Fatalf("repairs for a valid body = %q", repairs)
	}
}

func TestRepairHeader_SaysHowMuchOfTheSubjectItCut(t *testing.T) {
	subject := "teach the incremental parser to reuse subtrees across edits that touch only trailing whitespace and comments"
	_, cut, repairs := RepairHeader("update", "", subject, HeaderLimit)
	if len(cut) >= len(subject) {
		t.Fatalf("subject was not cut: %q", cut)
	}
	found := false
	for _, repair := range repairs {
		if strings.HasPrefix(repair, "cut the subject from 16 to ") && strings.HasSuffix(repair, " words") {
			found = true
		}
	}
	if !found {
		t.Fatalf("repairs = %q, want the word counts of the cut", repairs)
	}
}

func TestRepairHeader_KeepsScopeAndEndsOnAContentWord(t *testing.T) {
	// The header the installed v0.8.4 wrote: it ended on "from".
	subject := "rebuild C's invalid-lookahead and end-of-input lex steps from the generator's ADVANCE_MAP"
	scope, got, repairs := RepairHeader("fix", "ccore", subject, HeaderLimit)
	header := composeHeader("fix", scope, got)
	if n := utf8.RuneCountInString(header); n > HeaderLimit {
		t.Fatalf("header %q has %d characters", header, n)
	}
	if last := lastWord(got); trailingFunctionWords[last] {
		t.Errorf("subject %q ends on the function word %q", got, last)
	}
	if scope != "ccore" {
		t.Errorf("scope = %q, want it kept: the subject keeps %d words", scope, len(strings.Fields(got)))
	}
	if len(repairs) == 0 {
		t.Error("the cut is not reported")
	}
}

func TestRepairHeader_NeverEndsOnAFunctionWord(t *testing.T) {
	for word := range trailingFunctionWords {
		// Cut points land right after the function word.
		base := "rebuild the generated lexer tables and traces " + word
		subject := base + " extra words that push the header past the limit of seventy two"
		_, got, _ := RepairHeader("fix", "", subject, utf8.RuneCountInString(composeHeader("fix", "", base)))
		if last := lastWord(got); trailingFunctionWords[last] {
			t.Errorf("after %q the subject %q ends on %q", word, got, last)
		}
	}
}

func TestRepairHeader_DropsScopeOnlyWhenTheSubjectWouldFallBelowFourWords(t *testing.T) {
	scope, got, repairs := RepairHeader("fix", "a-very-long-scope-name-that-eats-the-header", "rebuild the lexer tables from the generated sources and traces", HeaderLimit)
	if scope != "" {
		t.Errorf("scope = %q, want it dropped: with it the subject would have fewer than 4 words (%q)", scope, got)
	}
	if !strings.Contains(strings.Join(repairs, ";"), "dropped scope") {
		t.Errorf("repairs = %q", repairs)
	}
	if n := utf8.RuneCountInString(composeHeader("fix", scope, got)); n > HeaderLimit {
		t.Errorf("header has %d characters", n)
	}
	if lastWord(got) == "from" {
		t.Errorf("subject %q ends on a function word", got)
	}
}

func TestRepairHeader_OnlyTheVerbSurvivesWithoutADanglingWord(t *testing.T) {
	_, got, _ := RepairHeader("fix", "", "rebuild from "+strings.Repeat("x", 80), 20)
	if got == "" || trailingFunctionWords[lastWord(got)] {
		t.Errorf("subject = %q", got)
	}
}

func lastWord(s string) string {
	words := strings.Fields(strings.ToLower(s))
	if len(words) == 0 {
		return ""
	}
	return words[len(words)-1]
}
