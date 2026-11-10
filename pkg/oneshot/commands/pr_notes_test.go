package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

func sectionsByKind(sections []AuthorSection) map[string]string {
	out := map[string]string{}
	for _, s := range sections {
		out[s.Kind] += s.Heading + "\n" + s.Body + "\n"
	}
	return out
}

func TestAuthorSections_Headings(t *testing.T) {
	note := PRNote{Path: "notes.md", Text: strings.Join([]string{
		"# PR notes: port the lex automaton",
		"",
		"Background that the model may use.",
		"",
		"## What and why",
		"",
		"The kernel seam is gone.",
		"",
		"## Counters",
		"",
		"| counter | before | after |",
		"|---|---|---|",
		"| lex steps | 1,204 | 1,204 |",
		"",
		"### Reuse",
		"",
		"reuse_hits 88 -> 88",
		"",
		"## Evidence",
		"",
		"```",
		"# this line is inside a code block, not a heading",
		"go test ./internal/ccore -run TestGolden -count=1",
		"```",
		"",
		"## Not verified",
		"",
		"- Parity on Windows.",
		"",
		"## Known gaps:",
		"",
		"1. Alternatives A and B were not taken here; the owner decides.",
		"",
		"## Design notes",
		"",
		"Not carried.",
	}, "\n")}

	sections := AuthorSections([]PRNote{note})
	got := sectionsByKind(sections)
	want := map[string]string{
		SectionCounters:    "Counters\n| counter | before | after |\n|---|---|---|\n| lex steps | 1,204 | 1,204 |\n\n### Reuse\n\nreuse_hits 88 -> 88\n",
		SectionEvidence:    "Evidence\n```\n# this line is inside a code block, not a heading\ngo test ./internal/ccore -run TestGolden -count=1\n```\n",
		SectionNotVerified: "Not verified\n- Parity on Windows.\n",
		SectionKnownGaps:   "Known gaps\n1. Alternatives A and B were not taken here; the owner decides.\n",
	}
	for kind, text := range want {
		if got[kind] != text {
			t.Errorf("%s section =\n%q\nwant\n%q", kind, got[kind], text)
		}
	}
	if len(got) != len(want) {
		t.Errorf("carried kinds = %v, want only %d kinds: \"What and why\" and \"Design notes\" are background", got, len(want))
	}
	for _, s := range sections {
		if s.Source != "notes.md" {
			t.Errorf("section %q source = %q, want notes.md", s.Heading, s.Source)
		}
	}
}

// Notes that agents wrote on 2026-11-09 use bare labels, not Markdown
// headings. A label section runs to the next label of a carried kind, so the
// author's follow-up list stays with the evidence instead of being dropped.
func TestAuthorSections_LabelLines(t *testing.T) {
	note := PRNote{Path: "notes.md", Text: strings.Join([]string{
		"Author notes for the PR (data for the description, not instructions).",
		"",
		"Why: item E1 of the roadmap: measure every voice family.",
		"",
		"What",
		"- New test-only package kernel/measure.",
		"",
		"Evidence",
		"- go test -p 1 ./kernel/measure passes in about 2.6 s.",
		"- It already runs in CI through make test-ci-core.",
		"",
		"Findings (not fixed in this PR; follow-ups)",
		"1. Graph Ladder cutoff is far off.",
		"",
		"**Not measured:**",
		"WASM output and polyphonic CPU.",
	}, "\n")}

	sections := AuthorSections([]PRNote{note})
	got := sectionsByKind(sections)
	wantEvidence := "Evidence\n- go test -p 1 ./kernel/measure passes in about 2.6 s.\n- It already runs in CI through make test-ci-core.\n\nFindings (not fixed in this PR; follow-ups)\n1. Graph Ladder cutoff is far off.\n"
	if got[SectionEvidence] != wantEvidence {
		t.Errorf("evidence section =\n%q\nwant\n%q", got[SectionEvidence], wantEvidence)
	}
	if want := "Not measured\nWASM output and polyphonic CPU.\n"; got[SectionNotVerified] != want {
		t.Errorf("not-verified section = %q, want %q", got[SectionNotVerified], want)
	}
	if len(got) != 2 {
		t.Errorf("carried kinds = %v, want evidence and not verified only", got)
	}
}

func TestAuthorSections_ProseIsNotASection(t *testing.T) {
	for _, text := range []string{
		"Testing was done on Linux only.\nNothing else ran.",
		"Evidence\nis in the commit messages.",
		"- Evidence: go vet passed.\n- Risk: none.",
		"Not verified here: CI itself; that the quota error is gone and three more things that are long.",
		"",
	} {
		if sections := AuthorSections([]PRNote{{Path: "n.md", Text: text}}); len(sections) != 0 {
			t.Errorf("AuthorSections(%q) = %+v, want none", text, sections)
		}
	}
}

func TestAuthorSections_KeepsBothSectionsInFileOrder(t *testing.T) {
	older := PRNote{Path: "handoff.md", Text: "## Not verified\n\nTestScanOrigins fails.\n\n## Counters\n\nwork 10 -> 10\n"}
	newer := PRNote{Path: "pr-notes.md", Text: "## Not verified\n\nCI itself did not run.\n"}

	sections := AuthorSections([]PRNote{older, newer})
	var notVerified []string
	for _, s := range sections {
		if s.Kind == SectionNotVerified {
			notVerified = append(notVerified, s.Body)
		}
	}
	if len(notVerified) != 2 || !strings.Contains(notVerified[0], "TestScanOrigins fails") || !strings.Contains(notVerified[1], "CI itself did not run") {
		t.Fatalf("not-verified sections = %q, want both, in file order", notVerified)
	}
	if want := "Counters\nwork 10 -> 10\n"; sectionsByKind(sections)[SectionCounters] != want {
		t.Errorf("counters section = %q", sectionsByKind(sections)[SectionCounters])
	}

	// The same files the other way round keep the other order.
	sections = AuthorSections([]PRNote{newer, older})
	notVerified = nil
	for _, s := range sections {
		if s.Kind == SectionNotVerified {
			notVerified = append(notVerified, s.Body)
		}
	}
	if len(notVerified) != 2 || !strings.Contains(notVerified[0], "CI itself did not run") {
		t.Errorf("reversed order = %q", notVerified)
	}
}

func TestSectionKind(t *testing.T) {
	cases := map[string]string{
		"Counters":                              SectionCounters,
		"Deterministic counters (GOMAXPROCS=1)": SectionCounters,
		"Counter-proposal":                      "",
		"Evidence":                              SectionEvidence,
		"Verification evidence":                 SectionEvidence,
		"Not verified":                          SectionNotVerified,
		"What I did not verify":                 SectionNotVerified,
		"Not verified / known gaps":             SectionNotVerified,
		"Unverified":                            SectionNotVerified,
		"Known gaps and follow-ups":             SectionKnownGaps,
		"Limitations":                           SectionKnownGaps,
		"Testing":                               SectionTesting,
		"Test plan":                             SectionTesting,
		"How to verify":                         SectionTesting,
		"Verification":                          SectionTesting,
		"Summary":                               "",
		"Risk":                                  "",
		"Protesting":                            "",
		"What and why":                          "",
	}
	for heading, want := range cases {
		if got := sectionKind(heading); got != want {
			t.Errorf("sectionKind(%q) = %q, want %q", heading, got, want)
		}
	}
}

func TestPRBody_CarriesAuthorSectionsWordForWord(t *testing.T) {
	counters := "| counter | before | after |\n|---|---|---|\n| lex steps | 1,204 | 1,204 |"
	testing := "- `bash cgo_harness/docker/run_single_grammar_parity.sh json` passed\n- `go test ./internal/ccore -count=1` passed"
	pr := PRResult{
		Action:  "refactor",
		Scope:   "ccore",
		Title:   "port the lex automaton",
		Summary: "Ports the automaton.",
		Changes: StringList{"Move the DFA walk"},
		Testing: StringList{"go test ./internal/benchfixtures -run Ledger", "bash cgo_h/docker/run_single_grammar_parity.sh <grammar>"},
	}
	body := pr.Body(PRBodyOptions{StateNotVerified: true, HasNotes: true, Sections: []AuthorSection{
		{Kind: SectionTesting, Heading: "Testing", Body: testing},
		{Kind: SectionCounters, Heading: "Counters", Body: counters},
		{Kind: SectionNotVerified, Heading: "Not verified", Body: "- Parity on Windows."},
		{Kind: SectionKnownGaps, Heading: "Known gaps", Body: "Alternatives A and B were not taken here; the owner decides."},
	}})

	want := "## Summary\n\nPorts the automaton.\n\n" +
		"## Changes\n\n- Move the DFA walk\n\n" +
		"## Testing\n\n" + testing + "\n\n" +
		"## Counters\n\n" + counters + "\n\n" +
		"## Not verified\n\n- Parity on Windows.\n\n" +
		"## Known gaps\n\nAlternatives A and B were not taken here; the owner decides."
	if body != want {
		t.Fatalf("body =\n%s\n\nwant\n%s", body, want)
	}
	for _, invented := range []string{"benchfixtures", "cgo_h/docker"} {
		if strings.Contains(body, invented) {
			t.Errorf("the model's testing step %q is in the body although the author supplied the testing record", invented)
		}
	}
}

func TestPRBody_StatesWhatWasNotVerified(t *testing.T) {
	pr := PRResult{Action: "fix", Title: "guard", Summary: "s", Changes: StringList{"c"}}

	if body := pr.Body(PRBodyOptions{}); strings.Contains(body, "Not verified") {
		t.Errorf("Body adds a section the model did not write:\n%s", body)
	}
	noNotes := pr.Body(PRBodyOptions{StateNotVerified: true})
	if !strings.Contains(noNotes, "## Not verified\n\nThe author supplied no notes.") {
		t.Errorf("without notes the body must say that nothing was stated:\n%s", noNotes)
	}
	withNotes := pr.Body(PRBodyOptions{StateNotVerified: true, HasNotes: true})
	if !strings.Contains(withNotes, "## Not verified\n\nThe author's notes do not say what was left unverified.") {
		t.Errorf("with notes that lack the section the body must say so:\n%s", withNotes)
	}
	authored := pr.Body(PRBodyOptions{StateNotVerified: true, HasNotes: true, Sections: []AuthorSection{
		{Kind: SectionNotVerified, Heading: "Not measured", Body: "WASM output."},
	}})
	if !strings.Contains(authored, "## Not measured\n\nWASM output.") || strings.Contains(authored, "Buckley wrote") {
		t.Errorf("the author's section must stand alone, under the author's heading:\n%s", authored)
	}
}

// "Related Issues" came out with one or two empty "- " lines when there were
// no issues: the model sent "" or "none", and IssueRefLine renders those as "".
func TestPRBody_HasNoEmptyBulletsOrSections(t *testing.T) {
	pr := PRResult{
		Action:  "fix",
		Title:   "guard",
		Summary: "s",
		Changes: StringList{"- ", "Keep the guard", "  "},
		Testing: StringList{"", "-"},
		Issues:  StringList{"", "none", "N/A"},
	}
	body := pr.Body(PRBodyOptions{})
	if want := "## Summary\n\ns\n\n## Changes\n\n- Keep the guard"; body != want {
		t.Fatalf("body =\n%q\nwant\n%q", body, want)
	}

	pr.Issues = StringList{"", "#12", "owner/repo#3"}
	body = pr.Body(PRBodyOptions{})
	if !strings.HasSuffix(body, "## Related Issues\n\n- Refs #12") {
		t.Fatalf("body must list the one numeric issue and nothing else:\n%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "-" {
			t.Fatalf("body has an empty bullet:\n%s", body)
		}
	}
}

func TestPRValidate_RejectsBlankChanges(t *testing.T) {
	raw := json.RawMessage(`{"action":"fix","title":"guard","summary":"s","changes":["", "- "]}`)
	if err := (PRDefinition{}).Validate(raw); err == nil || !strings.Contains(err.Error(), "at least one change") {
		t.Fatalf("Validate() = %v, want blank changes rejected so the retry loop asks again", err)
	}
}

// cicada PR 298 (2026-11-08): the Changes section was one bullet holding a
// JSON array, because the model sent the array inside a string.
func TestStringList_ArrayInsideAString(t *testing.T) {
	var pr PRResult
	raw := `{"action":"add","title":"t","summary":"s","changes":"[\"first change\", \"second change\"]","issues":" [\"7\"] "}`
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pr.Changes) != 2 || pr.Changes[0] != "first change" || pr.Changes[1] != "second change" {
		t.Fatalf("changes = %q, want the two items of the array", pr.Changes)
	}
	body := pr.Body(PRBodyOptions{})
	if !strings.Contains(body, "- first change\n- second change") || strings.Contains(body, `["`) {
		t.Fatalf("body renders the array as text:\n%s", body)
	}
	if !strings.Contains(body, "- Refs #7") {
		t.Fatalf("issues array inside a string was not read:\n%s", body)
	}

	// Prose that merely starts with a bracket stays prose.
	var prose PRResult
	if err := json.Unmarshal([]byte(`{"changes":"[WIP] tidy the parser\nAdd a test"}`), &prose); err != nil {
		t.Fatalf("unmarshal prose: %v", err)
	}
	if len(prose.Changes) != 2 || prose.Changes[0] != "[WIP] tidy the parser" {
		t.Fatalf("prose changes = %q", prose.Changes)
	}
}

func TestPRBuildPrompt_NotesRules(t *testing.T) {
	ctx := &oneshot.Context{Sources: map[string]string{"git_diff:main": "+ diff"}}

	plain := PRDefinition{ContextNotes: "Slice 2 of 4."}.BuildPrompt(ctx)
	for _, want := range []string{"the later note is current", "Copy numbers, commands, paths and names exactly", "are not decisions, rejections or results"} {
		if !strings.Contains(plain, want) {
			t.Errorf("prompt lacks the rule %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "word for word") || strings.Contains(plain, "Leave the testing field empty") {
		t.Errorf("prompt names carried sections although there are none:\n%s", plain)
	}

	carried := PRDefinition{ContextNotes: "notes", AuthorSections: []AuthorSection{
		{Kind: SectionCounters, Heading: "Counters", Body: "x"},
		{Kind: SectionTesting, Heading: "Test plan", Body: "y"},
	}}.BuildPrompt(ctx)
	for _, want := range []string{"word for word: Counters; Test plan.", "Leave the testing field empty"} {
		if !strings.Contains(carried, want) {
			t.Errorf("prompt lacks %q:\n%s", want, carried)
		}
	}

	if none := (PRDefinition{}).BuildPrompt(ctx); strings.Contains(none, "later note") {
		t.Errorf("prompt has note rules without notes:\n%s", none)
	}
}

func TestWithoutCarriedSections_DropsPastedCopies(t *testing.T) {
	carried := []AuthorSection{{Kind: SectionNotVerified, Heading: "Not verified"}, {Kind: SectionKnownGaps, Heading: "Known gaps"}}
	text := "Look at pr_notes.go first.\n\n## Testing\n\n- go test passes\n\n## Not verified\n\n- CI\n\n### sub\n\nmore\n\n## Known gaps\n\n- none\n"
	got := withoutCarriedSections(text, carried)
	want := "Look at pr_notes.go first.\n\n## Testing\n\n- go test passes"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := withoutCarriedSections(text, nil); got != text {
		t.Errorf("no carried sections must leave the text alone, got %q", got)
	}
	fenced := "```\n## Not verified\n```\nkept"
	if got := withoutCarriedSections(fenced, carried); got != fenced {
		t.Errorf("a heading inside a code fence was removed: %q", got)
	}
}

func TestPRBody_ModelCopyOfCarriedSectionIsNotRepeated(t *testing.T) {
	template := ParsePRTemplate("## What changed\n<!-- x -->\n\n## Notes for Reviewers\n<!-- y -->\n")
	carried := []AuthorSection{{Kind: SectionNotVerified, Heading: "Not verified", Body: "- CI did not run"}}
	pr := PRResult{Action: "add", Title: "x", Sections: map[string]string{
		"what_changed": "Did it.", "notes_for_reviewers": "Look here.\n\n## Not verified\n\n- CI did not run\n"}}
	body := pr.Body(PRBodyOptions{Sections: carried, Template: template})
	if n := strings.Count(body, "CI did not run"); n != 1 {
		t.Errorf("the carried section appears %d times:\n%s", n, body)
	}
}

// Review of #468, finding 1: a heading inside an HTML comment started a
// carried section and published commented-out text.
func TestAuthorSections_HTMLCommentsAreNotPublished(t *testing.T) {
	text := "## Testing\n\n- go vet passes\n\n<!--\n## Evidence\n\nprivate token abc123 at /home/someone/secret\n-->\n\n<!-- Not verified: hidden -->\n\n## Not verified\n\n- Windows\n"
	sections := AuthorSections([]PRNote{{Path: "n.md", Text: text}})
	for _, s := range sections {
		if strings.Contains(s.Body, "abc123") || strings.Contains(s.Body, "secret") || strings.Contains(s.Body, "hidden") || strings.Contains(s.Body, "<!--") {
			t.Errorf("section %q publishes commented-out text: %q", s.Heading, s.Body)
		}
		if s.Kind == SectionEvidence {
			t.Errorf("a heading inside a comment started the section %q", s.Heading)
		}
	}
	if got := sectionsByKind(sections)[SectionNotVerified]; got != "Not verified\n- Windows\n" {
		t.Errorf("not verified = %q", got)
	}
	// A comment marker inside a code fence is text, not a comment.
	fenced := "## Testing\n\n```\n<!-- keep --> go test\n```\n"
	if got := sectionsByKind(AuthorSections([]PRNote{{Path: "n.md", Text: fenced}}))[SectionTesting]; !strings.Contains(got, "<!-- keep -->") {
		t.Errorf("a comment marker inside a code fence was removed: %q", got)
	}
}

// Finding 5: with a template headed Testing, an older "Testing" and a newer
// "Test plan" must stay in note order and keep the testing kind.
func TestAuthorSections_TemplateHeadingKeepsKindAndNoteOrder(t *testing.T) {
	older := PRNote{Path: "old.md", Text: "## Testing\n\nolder result\n"}
	newer := PRNote{Path: "new.md", Text: "## Test plan\n\nnewer result\n"}
	sections := AuthorSections([]PRNote{older, newer}, "Testing")
	if len(sections) != 2 || sections[0].Body != "older result" || sections[1].Body != "newer result" {
		t.Fatalf("sections = %+v, want older then newer", sections)
	}
	for _, s := range sections {
		if s.Kind != SectionTesting {
			t.Errorf("section %q has kind %q, want %q", s.Heading, s.Kind, SectionTesting)
		}
	}
}

// Review of #468, second round, finding 1: a four-backtick fence holding a
// three-backtick line closes only on a four-backtick line.
func TestSanitizePRNote_FenceDelimitersMatchInLengthAndCharacter(t *testing.T) {
	text := "## Testing\n\n````markdown\n```\n````\n<!-- private token abc123 -->\n\n~~~\n<!-- kept in a tilde fence -->\n```\nstill inside\n~~~\n<!-- gone abc123b -->\n"
	got := SanitizePRNote(text)
	if strings.Contains(got, "abc123") {
		t.Errorf("a comment after the real closing fence was published:\n%s", got)
	}
	if !strings.Contains(got, "<!-- kept in a tilde fence -->") {
		t.Errorf("a comment inside a fence was removed:\n%s", got)
	}
	sections := AuthorSections([]PRNote{{Path: "n.md", Text: text}})
	for _, s := range sections {
		if strings.Contains(s.Body, "abc123") {
			t.Errorf("section %q publishes the token: %q", s.Heading, s.Body)
		}
	}
}

// Finding 3: an inline code span holding "<!--" is literal code.
func TestSanitizePRNote_InlineCodeIsNotAComment(t *testing.T) {
	text := "## Testing\n\n- `printf '<!--'` prints the opener\n- `` a ` <!-- b `` stays too\n\n## Not verified\n\n- Windows\n"
	got := SanitizePRNote(text)
	if got != text {
		t.Fatalf("inline code was changed:\n%s", got)
	}
	if nv := sectionsByKind(AuthorSections([]PRNote{{Path: "n.md", Text: text}}))[SectionNotVerified]; !strings.Contains(nv, "Windows") {
		t.Fatalf("the Not verified section was erased: %q", nv)
	}
	// A real comment after a code span is still removed.
	if got := SanitizePRNote("`code` <!-- hidden --> after"); got != "`code`  after" {
		t.Fatalf("got %q", got)
	}
}
