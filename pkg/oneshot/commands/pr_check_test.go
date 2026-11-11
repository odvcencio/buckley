package commands

import (
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

func TestNewPRSources_SeparatesTheRecordFromTheRest(t *testing.T) {
	src := NewPRSources(
		[]PRNote{{Path: "n.md", text: "note text"}},
		&oneshot.Context{Sources: map[string]string{
			"git_log:refs/remotes/origin/main":   "abc123 commit subject",
			"git_diff:refs/remotes/origin/main":  "+diff line",
			"git_files:refs/remotes/origin/main": "M file.go",
			"agents_md":                          "guideline text",
		}},
	)
	for _, want := range []string{"note text", "commit subject"} {
		if !strings.Contains(src.Record, want) {
			t.Errorf("Record lacks %q: %q", want, src.Record)
		}
	}
	for _, unwanted := range []string{"diff line", "file.go", "guideline text"} {
		if strings.Contains(src.Record, unwanted) {
			t.Errorf("Record has %q, which records no decision: %q", unwanted, src.Record)
		}
	}
	for _, want := range []string{"note text", "commit subject", "diff line", "file.go", "guideline text"} {
		if !strings.Contains(src.All, want) {
			t.Errorf("All lacks %q", want)
		}
	}
	if empty := NewPRSources(nil, nil); empty.Record != "" || empty.All != "" {
		t.Errorf("NewPRSources(nil, nil) = %+v, want empty", empty)
	}
}

// The three dropped steps are the ones a small model wrote on 2026-11-09: a
// test that does not exist, a script path with half its directory name
// missing, and a flag fragment.
func TestUnsourcedCommands(t *testing.T) {
	src := PRSources{All: strings.Join([]string{
		"Ran go test ./internal/ccore -run TestGolden -count=1 on Linux.",
		"Use -count=1 for every run.",
		"- `bash cgo_harness/docker/run_single_grammar_parity.sh <grammar>`",
		"make test-scripts",
		"+++ b/scripts/browser-lib.mjs",
	}, "\n")}
	pr := PRResult{Testing: StringList{
		"`go test ./internal/benchfixtures -run Ledger`",
		"bash cgo_h/docker/run_single_grammar_parity.sh <grammar>",
		"-count-1 (use -count=1)",
		"`go  test ./internal/ccore -run TestGolden -count=1` passes",
		"- bash cgo_harness/docker/run_single_grammar_parity.sh <grammar>",
		"make test-scripts (81 tests pass)",
		"Read `scripts/browser-lib.mjs` first",
		"Open the settings page and check that the toggle stays on",
		"Check `Parser.Reset` by hand",
		"-count=1 keeps the test cache out of the run",
	}}

	before := append(StringList(nil), pr.Testing...)
	warnings := pr.UnsourcedCommands(src)
	wantWarned := []string{
		"`go test ./internal/benchfixtures -run Ledger`",
		"bash cgo_h/docker/run_single_grammar_parity.sh <grammar>",
		"-count-1 (use -count=1)",
	}
	if len(warnings) != len(wantWarned) {
		t.Fatalf("warnings = %q, want one for each of %q", warnings, wantWarned)
	}
	for i, step := range wantWarned {
		if !strings.Contains(warnings[i], step) || !strings.Contains(warnings[i], "stays in the text") {
			t.Errorf("warning %d = %q, want it to name %q and say the step stays", i, warnings[i], step)
		}
	}
	// The check is advisory: no step is dropped or rewritten.
	if !reflect.DeepEqual(pr.Testing, before) {
		t.Errorf("the testing steps changed: %q", pr.Testing)
	}
}

func TestUnsourcedCommands_NothingToDrop(t *testing.T) {
	pr := PRResult{}
	if warnings := pr.UnsourcedCommands(PRSources{}); len(warnings) != 0 || pr.Testing != nil {
		t.Fatalf("warnings = %q, testing = %q", warnings, pr.Testing)
	}
}

func TestUnbackedClaims(t *testing.T) {
	notes := "Alternatives A and B were not taken here; the owner decides.\n412 of 412 match. lex steps 1,204 before and after."
	pr := PRResult{
		Title:   "port the lex automaton",
		Summary: "Alternatives A and B were rejected. All 412 cases match, with 1204 lex steps.",
		Changes: StringList{"Update 37 call sites", "Keep 2 heuristics"},
		Testing: StringList{"81 tests pass"},
	}

	claims := pr.UnbackedClaims(PRSources{Record: notes, All: notes + "\n+ the parser rejects any byte no leaf covers"})
	want := []string{
		`the text says "rejected", but the author's notes and the commits record no such decision`,
		"the text has the number 37, which is not in the notes, the commits or the diff",
		"the text has the number 81, which is not in the notes, the commits or the diff",
	}
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("claims =\n%q\nwant\n%q", claims, want)
	}

	// A decision that the record states has a source, whatever its form.
	recorded := notes + "\nWe reject alternative A: it doubles the table size."
	for _, claim := range pr.UnbackedClaims(PRSources{Record: recorded, All: recorded}) {
		if strings.Contains(claim, "rejected") {
			t.Errorf("claim %q although the notes record the decision", claim)
		}
	}

	if claims := (PRResult{Summary: "Plain text with 1 digit."}).UnbackedClaims(PRSources{}); len(claims) != 0 {
		t.Errorf("claims = %q, want none for single digits", claims)
	}
}

// Review of #468, finding 3: a backtick span that is not the command must not
// hide the command before it.
func TestStepCommands_UnrelatedBacktickSpanDoesNotHideTheCommand(t *testing.T) {
	pr := PRResult{Testing: StringList{"go test ./missing -run `TestFoo`"}}
	warnings := pr.UnsourcedCommands(PRSources{All: "nothing relevant"})
	if len(warnings) != 1 || len(pr.Testing) != 1 {
		t.Fatalf("warnings = %q, kept = %q; the unsourced command warns and the step stays", warnings, pr.Testing)
	}
	// A sourced command with a code-formatted argument stays.
	pr = PRResult{Testing: StringList{"go test ./pkg -run `TestFoo`"}}
	if warnings := pr.UnsourcedCommands(PRSources{All: "go test ./pkg -run `TestFoo`"}); len(warnings) != 0 {
		t.Fatalf("warned about a sourced step: %q", warnings)
	}
}

// Finding 4: only Testing sections lose unsourced steps; a Summary bullet that
// names a command is a description of the change.
func TestUnsourcedCommands_LeavesOtherSectionsAlone(t *testing.T) {
	pr := PRResult{Sections: map[string]string{
		"summary": "- Expose `buckley inspect` for diagnostics",
		"testing": "- `go test ./missing`",
	}}
	warnings := pr.UnsourcedCommands(PRSources{All: "x"})
	// The Testing step and the command in the summary both warn; neither is changed.
	if len(warnings) != 2 || !strings.Contains(warnings[0], "go test ./missing") || !strings.Contains(warnings[1], "buckley inspect") {
		t.Fatalf("warnings = %q", warnings)
	}
	if pr.Sections["summary"] != "- Expose `buckley inspect` for diagnostics" {
		t.Errorf("summary = %q", pr.Sections["summary"])
	}
}

// Review of #468, second round, finding 4: the whole command is compared, with
// inline code marks normalised on both sides.
func TestUnsourcedCommands_ArgumentInCodeFormatIsPartOfTheCommand(t *testing.T) {
	src := PRSources{All: "go test ./pkg -run TestFoo"}
	pr := PRResult{Testing: StringList{"go test ./pkg -run `TestMissing`"}}
	if warnings := pr.UnsourcedCommands(src); len(warnings) != 1 || len(pr.Testing) != 1 {
		t.Fatalf("warnings = %q, kept = %q: the unsupported argument passed", warnings, pr.Testing)
	}
	// The same command with a supported argument stays, whichever side has the marks.
	for _, all := range []string{"go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo`"} {
		pr = PRResult{Testing: StringList{"go test ./pkg -run `TestFoo`", "go test ./pkg -run TestFoo"}}
		if warnings := pr.UnsourcedCommands(PRSources{All: all}); len(warnings) != 0 {
			t.Errorf("source %q: warned %q", all, warnings)
		}
	}
	// Flags between two spans stay part of the command; prose after it does not.
	pr = PRResult{Testing: StringList{"go test ./pkg -run `TestA` -count=1 `TestB` passed locally"}}
	if warnings := pr.UnsourcedCommands(PRSources{All: "go test ./pkg -run TestA -count=1 TestB"}); len(warnings) != 0 {
		t.Errorf("warned %q", warnings)
	}
}

// Review of #468, third round: flags after the last code span are part of the
// command.
// The rule for a command written with code spans: the command is the step up
// to the first token that cannot belong to a command line (a lone "-" or "--",
// a dash, a colon before a space, a parenthesis, a plain word that is no flag
// value); it is compared as a whole with the sources. Reviews of #468 to #470,
// rounds two to four.
func TestUnsourcedCommands_CommandEndsAtTheFirstTokenThatIsNoCommand(t *testing.T) {
	for _, tc := range []struct {
		name, source, step string
		kept               bool
	}{
		{"unsourced flag after the last span", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` -race", false},
		{"sourced flag after the last span, then prose", "go test ./pkg -run TestFoo -race", "go test ./pkg -run `TestFoo` -race passed locally", true},
		{"prose after the span", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` passed locally", true},
		{"lone hyphen is prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` - passed locally", true},
		{"lone double hyphen is prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` -- passed locally", true},
		{"em dash is prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` \u2014 passed locally", true},
		{"colon is prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo`: passed locally", true},
		{"parenthesis is prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` (passed locally)", true},
		{"flag after the boundary is prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` - passed with -race", true},
		{"separate flag value kept, sourced", "go test ./pkg -run TestFoo -count 1", "go test ./pkg -run `TestFoo` -count 1", true},
		{"flag after a separate flag value", "go test ./pkg -run TestFoo -count 1", "go test ./pkg -run `TestFoo` -count 1 -race", false},
		{"flag after a flag with an equals sign", "go test ./pkg -run TestFoo -count=1", "go test ./pkg -run `TestFoo` -count=1 -race", false},
		{"flag after a word value and prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` passed locally with -race", false},
		// Fifth review: a flag in a code span after prose is checked on its own.
		{"code-formatted flag after prose", "go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo` passed locally with `-race`", false},
		// Fifth review: sentence punctuation alone is no flag value.
		{"sentence period after a flag", "go test ./pkg -run TestFoo -race", "go test ./pkg -run `TestFoo` -race passed.", true},
		{"flag after a second span", "go test ./pkg -run TestFoo -tags integration", "go test ./pkg -run `TestFoo` -tags `integration` -race", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := PRResult{Testing: StringList{tc.step}}
			warnings := pr.UnsourcedCommands(PRSources{All: tc.source})
			if len(pr.Testing) != 1 {
				t.Errorf("the step %q was changed or dropped: %q", tc.step, pr.Testing)
			}
			if tc.kept && len(warnings) != 0 {
				t.Errorf("warned about a sourced step %q: %q", tc.step, warnings)
			}
			if !tc.kept && len(warnings) != 1 {
				t.Errorf("no warning for an unsourced step %q: %q", tc.step, warnings)
			}
		})
	}
}

func TestUnsourcedCommands_FlagAfterTheLastSpanIsPartOfTheCommand(t *testing.T) {
	src := PRSources{All: "go test ./pkg -run TestFoo"}
	pr := PRResult{Testing: StringList{"go test ./pkg -run `TestFoo` -race"}}
	if warnings := pr.UnsourcedCommands(src); len(warnings) != 1 || len(pr.Testing) != 1 {
		t.Fatalf("warnings = %q, kept = %q: the unsupported -race passed", warnings, pr.Testing)
	}
	// Prose after the flags is not part of the command.
	pr = PRResult{Testing: StringList{"go test ./pkg -run `TestFoo` -race passed locally"}}
	if dropped := pr.UnsourcedCommands(PRSources{All: "go test ./pkg -run TestFoo -race"}); len(dropped) != 0 {
		t.Fatalf("warned about a sourced step: %q", dropped)
	}
	pr = PRResult{Testing: StringList{"go test ./pkg -run `TestFoo` passed locally"}}
	if dropped := pr.UnsourcedCommands(src); len(dropped) != 0 {
		t.Fatalf("prose was read as part of the command: %q", dropped)
	}
}
