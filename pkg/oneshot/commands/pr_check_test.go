package commands

import (
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

func TestNewPRSources_SeparatesTheRecordFromTheRest(t *testing.T) {
	src := NewPRSources(
		[]PRNote{{Path: "n.md", Text: "note text"}},
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
func TestDropUnbackedTesting(t *testing.T) {
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

	dropped := pr.DropUnbackedTesting(src)
	wantDropped := []string{
		"`go test ./internal/benchfixtures -run Ledger`",
		"bash cgo_h/docker/run_single_grammar_parity.sh <grammar>",
		"-count-1 (use -count=1)",
	}
	if !reflect.DeepEqual(dropped, wantDropped) {
		t.Errorf("dropped = %q\nwant      %q", dropped, wantDropped)
	}
	if len(pr.Testing) != 7 {
		t.Errorf("kept %d steps, want 7: %q", len(pr.Testing), pr.Testing)
	}
	for _, step := range pr.Testing {
		for _, gone := range wantDropped {
			if step == gone {
				t.Errorf("step %q is still in the list", step)
			}
		}
	}
}

func TestDropUnbackedTesting_NothingToDrop(t *testing.T) {
	pr := PRResult{}
	if dropped := pr.DropUnbackedTesting(PRSources{}); len(dropped) != 0 || pr.Testing != nil {
		t.Fatalf("dropped = %q, testing = %q", dropped, pr.Testing)
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
	dropped := pr.DropUnbackedTesting(PRSources{All: "nothing relevant"})
	if len(dropped) != 1 || len(pr.Testing) != 0 {
		t.Fatalf("dropped = %q, kept = %q; the unsourced command must go", dropped, pr.Testing)
	}
	// A sourced command with a code-formatted argument stays.
	pr = PRResult{Testing: StringList{"go test ./pkg -run `TestFoo`"}}
	if dropped := pr.DropUnbackedTesting(PRSources{All: "go test ./pkg -run `TestFoo`"}); len(dropped) != 0 {
		t.Fatalf("dropped a sourced step: %q", dropped)
	}
}

// Finding 4: only Testing sections lose unsourced steps; a Summary bullet that
// names a command is a description of the change.
func TestDropUnbackedTesting_LeavesOtherSectionsAlone(t *testing.T) {
	pr := PRResult{Sections: map[string]string{
		"summary": "- Expose `buckley inspect` for diagnostics",
		"testing": "- `go test ./missing`",
	}}
	dropped := pr.DropUnbackedTesting(PRSources{All: "x"})
	if len(dropped) != 1 || !strings.Contains(dropped[0], "go test ./missing") {
		t.Fatalf("dropped = %q", dropped)
	}
	if pr.Sections["summary"] != "- Expose `buckley inspect` for diagnostics" {
		t.Errorf("summary = %q", pr.Sections["summary"])
	}
}

// Review of #468, second round, finding 4: the whole command is compared, with
// inline code marks normalised on both sides.
func TestDropUnbackedTesting_ArgumentInCodeFormatIsPartOfTheCommand(t *testing.T) {
	src := PRSources{All: "go test ./pkg -run TestFoo"}
	pr := PRResult{Testing: StringList{"go test ./pkg -run `TestMissing`"}}
	if dropped := pr.DropUnbackedTesting(src); len(dropped) != 1 || len(pr.Testing) != 0 {
		t.Fatalf("dropped = %q, kept = %q: the unsupported argument passed", dropped, pr.Testing)
	}
	// The same command with a supported argument stays, whichever side has the marks.
	for _, all := range []string{"go test ./pkg -run TestFoo", "go test ./pkg -run `TestFoo`"} {
		pr = PRResult{Testing: StringList{"go test ./pkg -run `TestFoo`", "go test ./pkg -run TestFoo"}}
		if dropped := pr.DropUnbackedTesting(PRSources{All: all}); len(dropped) != 0 {
			t.Errorf("source %q: dropped %q", all, dropped)
		}
	}
	// Flags between two spans stay part of the command; prose after it does not.
	pr = PRResult{Testing: StringList{"go test ./pkg -run `TestA` -count=1 `TestB` passed locally"}}
	if dropped := pr.DropUnbackedTesting(PRSources{All: "go test ./pkg -run TestA -count=1 TestB"}); len(dropped) != 0 {
		t.Errorf("dropped %q", dropped)
	}
}
