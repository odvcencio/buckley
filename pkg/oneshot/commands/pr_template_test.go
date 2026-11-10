package commands

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// checklistTemplate is modeled on the gotreesitter template: two sections
// that ask for text and three checklists, one with a comment that spans lines.
func checklistTemplate(t *testing.T) (string, *PRTemplate) {
	t.Helper()
	data, err := os.ReadFile("testdata/pull_request_template.md")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := ParsePRTemplate(string(data))
	if tmpl == nil {
		t.Fatal("ParsePRTemplate returned nil for the fixture")
	}
	return string(data), tmpl
}

// reviewTemplate is Buckley's own template: a checklist under the first
// heading and three sub-headings that ask for text.
const reviewTemplate = `## Summary
- [ ] Includes tests or rationale for why tests are unnecessary
- [ ] References relevant issues/links

### What changed
<!-- Briefly describe the changes. -->

### Testing
<!-- List commands run, e.g., ./scripts/test.sh -->

### Notes for Reviewers
<!-- Call out anything that needs special attention. -->
`

func TestParsePRTemplate(t *testing.T) {
	_, tmpl := checklistTemplate(t)

	type shape struct {
		Heading string
		Key     string
		Open    bool
	}
	var got []shape
	for _, s := range tmpl.Sections {
		got = append(got, shape{s.Heading, s.Key, s.Open})
	}
	want := []shape{
		{"What does this PR do?", "what_does_this_pr_do", true},
		{"Why this approach?", "why_this_approach", true},
		{"Correctness", "correctness", false},
		{"Performance", "performance", false},
		{"Self-review", "self_review", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sections = %+v\nwant       %+v", got, want)
	}
	if guide := tmpl.Sections[0].Guide; guide != "One or two sentences. State the outcome, not a commit diary." {
		t.Errorf("guide of the first section = %q", guide)
	}
	if tmpl.Preamble != "" {
		t.Errorf("preamble = %q, want none", tmpl.Preamble)
	}

	if ParsePRTemplate("  \n\n") != nil {
		t.Error("a blank template must parse to nil")
	}

	free := ParsePRTemplate("Thanks for the PR!\n\n- [ ] I read CONTRIBUTING.md\n")
	if free == nil || len(free.Sections) != 0 || free.Preamble != "Thanks for the PR!\n\n- [ ] I read CONTRIBUTING.md" {
		t.Errorf("a template without headings = %+v, want all of it as preamble", free)
	}

	// A comment that starts before the first heading can hold a line that
	// looks like one.
	early := ParsePRTemplate("<!-- Fill in every section.\n## not a heading\n-->\n\n## Real ##\n\n<!-- say what changed -->\n")
	if len(early.Sections) != 1 || early.Sections[0].Heading != "Real" || !early.Sections[0].Open || !strings.Contains(early.Preamble, "## not a heading") {
		t.Errorf("a heading inside a comment in the preamble was read as a section: %+v", early)
	}
	if got, _ := (PRResult{}).templateBody(early, nil, ""); !strings.Contains(got, "\n## Real ##\n") {
		t.Errorf("the heading line is not written as the template has it:\n%s", got)
	}

	twice := ParsePRTemplate("## Notes\n\n## Notes\n")
	if twice.Sections[0].Key != "notes" || twice.Sections[1].Key != "notes_2" {
		t.Errorf("keys of repeated headings = %q, %q", twice.Sections[0].Key, twice.Sections[1].Key)
	}
}

// With nothing to add, the body is the template itself: every heading,
// checkbox, comment and blank line as the repository wrote it.
func TestPRBody_UnfilledTemplateIsTheTemplate(t *testing.T) {
	for name, text := range map[string]string{"checklists": "", "sub-headings": reviewTemplate} {
		tmpl := ParsePRTemplate(text)
		if name == "checklists" {
			text, tmpl = checklistTemplate(t)
		}
		got, rest := PRResult{}.templateBody(tmpl, nil, "")
		if got != strings.TrimSpace(text) {
			t.Errorf("%s: rendered template differs from the file:\n--- got\n%s\n--- want\n%s", name, got, strings.TrimSpace(text))
		}
		if len(rest) != 0 {
			t.Errorf("%s: rest = %+v", name, rest)
		}
	}
}

func TestPRBody_FollowsTheTemplate(t *testing.T) {
	text, tmpl := checklistTemplate(t)
	pr := PRResult{
		Action: "refactor",
		Title:  "port the lex automaton",
		Sections: map[string]string{
			"what_does_this_pr_do": "Ports the lex automaton into the core package. Closes #14.",
			"why_this_approach":    "",
		},
		Issues: StringList{"12"},
	}
	body := pr.Body(PRBodyOptions{Template: tmpl, StateNotVerified: true, HasNotes: true, Sections: []AuthorSection{
		{Kind: SectionCounters, Heading: "Counters", Body: "| lex steps | 1,204 | 1,204 |"},
	}})

	wantStart := "## What does this PR do?\n\nPorts the lex automaton into the core package. Refs #14.\n\n" +
		"## Why this approach?\n\n<!-- Brief rationale. Mention the main tradeoff or rejected alternative only if it matters. -->\n\n" +
		"## Correctness\n\n- [ ] Focused package or unit tests ran for the touched behavior\n"
	if !strings.HasPrefix(body, wantStart) {
		t.Fatalf("body does not start with the filled template:\n%s", body)
	}
	wantEnd := "- [ ] Called out anything I'm unsure about or want a second opinion on\n\n" +
		"## Counters\n\n| lex steps | 1,204 | 1,204 |\n\n" +
		"## Not verified\n\nThe author's notes do not say what was left unverified. Buckley wrote this text from the notes, the commits and the diff, and ran nothing.\n\n" +
		"## Related Issues\n\n- Refs #12"
	if !strings.HasSuffix(body, wantEnd) {
		t.Fatalf("body does not end with the carried sections:\n%s", body)
	}
	if got, want := strings.Count(body, "- [ ]"), strings.Count(text, "- [ ]"); got != want {
		t.Errorf("body has %d unticked checkboxes, the template has %d", got, want)
	}
	if strings.Contains(body, "[x]") {
		t.Error("the body ticks a checkbox; only the author can attest to a check")
	}
	for _, unwanted := range []string{"## Summary", "## Changes", "State the outcome, not a commit diary"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body has %q", unwanted)
		}
	}
	for _, kept := range []string{"## this heading is inside a comment", "Review your own diff before requesting review."} {
		if !strings.Contains(body, kept) {
			t.Errorf("body lost the template's own text %q", kept)
		}
	}
}

func TestPRBody_TemplateTakesTheAuthorsSections(t *testing.T) {
	tmpl := ParsePRTemplate(reviewTemplate)
	pr := PRResult{Action: "fix", Title: "guard", Sections: map[string]string{
		"what_changed":        "model text that the author's section replaces",
		"notes_for_reviewers": "Look at the guard first.",
	}}
	author := []AuthorSection{
		{Kind: SectionTesting, Heading: "Test plan", Body: "- `./scripts/test.sh` passed"},
		{Kind: headingKindPrefix + "what changed", Heading: "What changed", Body: "The guard now covers the retry path."},
		{Kind: SectionKnownGaps, Heading: "Known gaps", Body: "Windows is not covered."},
	}

	body := pr.Body(PRBodyOptions{Template: tmpl, Sections: author})
	want := "## Summary\n- [ ] Includes tests or rationale for why tests are unnecessary\n- [ ] References relevant issues/links\n\n" +
		"### What changed\n\nThe guard now covers the retry path.\n\n" +
		"### Testing\n\n- `./scripts/test.sh` passed\n\n" +
		"### Notes for Reviewers\n\nLook at the guard first.\n\n" +
		"## Known gaps\n\nWindows is not covered."
	if body != want {
		t.Fatalf("body =\n%s\n\nwant\n%s", body, want)
	}

	// Text for a checklist section goes under the checklist. A checklist that
	// the author restated replaces the template's.
	under := pr.Body(PRBodyOptions{Template: tmpl, Sections: []AuthorSection{
		{Kind: headingKindPrefix + "summary", Heading: "Summary", Body: "Tests are in guard_test.go."},
	}})
	if !strings.HasPrefix(under, "## Summary\n- [ ] Includes tests or rationale for why tests are unnecessary\n- [ ] References relevant issues/links\n\nTests are in guard_test.go.\n\n### What changed") {
		t.Errorf("author text is not under the checklist:\n%s", under)
	}
	ticked := pr.Body(PRBodyOptions{Template: tmpl, Sections: []AuthorSection{
		{Kind: headingKindPrefix + "summary", Heading: "Summary", Body: "- [x] Includes tests\n- [x] References #12"},
	}})
	if !strings.HasPrefix(ticked, "## Summary\n\n- [x] Includes tests\n- [x] References #12\n\n### What changed") || strings.Contains(ticked, "- [ ]") {
		t.Errorf("the author's checklist does not replace the template's:\n%s", ticked)
	}
}

func TestPRBody_ChecklistOnlyTemplateFollowsBuckleysSections(t *testing.T) {
	tmpl := ParsePRTemplate("## Checklist\n\n- [ ] Tests ran\n- [ ] Docs updated\n")
	pr := PRResult{Action: "fix", Title: "guard", Summary: "Fixes the guard.", Changes: StringList{"Cover the retry path"}, ReviewersHint: "Start at guard.go."}
	body := pr.Body(PRBodyOptions{Template: tmpl})
	want := "## Summary\n\nFixes the guard.\n\n## Changes\n\n- Cover the retry path\n\n## Review Focus\n\nStart at guard.go.\n\n## Checklist\n\n- [ ] Tests ran\n- [ ] Docs updated"
	if body != want {
		t.Fatalf("body =\n%s\n\nwant\n%s", body, want)
	}
}

// A template with its own "Not verified" heading keeps that as the one place
// for the statement.
func TestPRBody_TemplateWithItsOwnNotVerifiedSection(t *testing.T) {
	tmpl := ParsePRTemplate("## What changed\n\n<!-- Describe the change. -->\n\n## Not verified\n\n<!-- Say what you did not check. -->\n")
	pr := PRResult{Action: "fix", Title: "guard", Sections: map[string]string{"what_changed": "Fixes the guard.", "not_verified": "Windows."}}

	body := pr.Body(PRBodyOptions{Template: tmpl, StateNotVerified: true})
	if want := "## What changed\n\nFixes the guard.\n\n## Not verified\n\nWindows."; body != want {
		t.Fatalf("body =\n%s\n\nwant\n%s", body, want)
	}

	// Left empty by the model, the template's own slot gets the statement
	// that the author supplied no verification gaps (review of #468, finding 6).
	pr.Sections = map[string]string{"what_changed": "Fixes the guard."}
	body = pr.Body(PRBodyOptions{Template: tmpl, StateNotVerified: true, HasNotes: true})
	if strings.Count(body, "## Not verified") != 1 || !strings.Contains(body, "The author's notes do not say what was left unverified") {
		t.Fatalf("body =\n%s", body)
	}
	// Without the option the slot stays as the template has it.
	body = pr.Body(PRBodyOptions{Template: tmpl})
	if !strings.Contains(body, "<!-- Say what you did not check. -->") || strings.Contains(body, "Buckley wrote") {
		t.Fatalf("body =\n%s", body)
	}
}

func TestPRTool_AsksForTheOpenTemplateSections(t *testing.T) {
	_, tmpl := checklistTemplate(t)
	def := PRDefinition{Template: tmpl}
	params := def.Tool().Parameters

	for _, name := range []string{"section_what_does_this_pr_do", "section_why_this_approach"} {
		prop, ok := params.Properties[name]
		if !ok {
			t.Fatalf("the tool lacks %s: %v", name, params.Properties)
		}
		if !strings.Contains(prop.Description, "no heading and no checkboxes") {
			t.Errorf("%s description = %q", name, prop.Description)
		}
	}
	if desc := params.Properties["section_what_does_this_pr_do"].Description; !strings.Contains(desc, `"What does this PR do?"`) || !strings.Contains(desc, "State the outcome, not a commit diary.") {
		t.Errorf("the description lacks the heading or the template's guidance: %q", desc)
	}
	for _, name := range []string{"summary", "changes", "testing", "reviewers_hint", "section_correctness", "section_self_review"} {
		if _, ok := params.Properties[name]; ok {
			t.Errorf("the tool asks for %s although the template sets the body", name)
		}
	}
	if want := []string{"action", "title", "section_what_does_this_pr_do", "section_why_this_approach"}; !reflect.DeepEqual(params.Required, want) {
		t.Errorf("required = %v, want %v", params.Required, want)
	}
	if !strings.Contains(def.SystemPrompt(), "do not tick a checkbox") {
		t.Error("the system prompt does not tell the model to leave the checkboxes alone")
	}

	// A section the author wrote is not asked of the model.
	def.AuthorSections = []AuthorSection{{Kind: headingKindPrefix + "why this approach", Heading: "Why this approach?", Body: "x"}}
	if _, ok := def.Tool().Parameters.Properties["section_why_this_approach"]; ok {
		t.Error("the tool asks the model for a section the author wrote")
	}

	// Without a template, and with a checklist-only template, the contract is unchanged.
	for _, plain := range []PRDefinition{{}, {Template: ParsePRTemplate("## Checklist\n- [ ] Tests ran\n")}} {
		params := plain.Tool().Parameters
		if want := []string{"action", "title", "summary", "changes"}; !reflect.DeepEqual(params.Required, want) {
			t.Errorf("required = %v, want %v", params.Required, want)
		}
		if strings.Contains(plain.SystemPrompt(), "do not tick a checkbox") {
			t.Error("the template prompt is used without an open template section")
		}
	}
}

func TestPRValidateAndUnmarshal_Template(t *testing.T) {
	_, tmpl := checklistTemplate(t)
	def := PRDefinition{Template: tmpl}

	missing := json.RawMessage(`{"action":"fix","title":"guard","section_why_this_approach":"Because."}`)
	if err := def.Validate(missing); err == nil || !strings.Contains(err.Error(), "section_what_does_this_pr_do is required") {
		t.Fatalf("Validate() = %v, want the first open section required", err)
	}

	ok := json.RawMessage(`{"action":"fix","title":"guard","section_what_does_this_pr_do":"Fixes the guard.","section_why_this_approach":["Small","Safe"],"section_unknown":3}`)
	if err := def.Validate(ok); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	value, err := def.Unmarshal(ok)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := map[string]string{"what_does_this_pr_do": "Fixes the guard.", "why_this_approach": "- Small\n- Safe\n"}
	if got := value.(*PRResult).Sections; !reflect.DeepEqual(got, want) {
		t.Errorf("sections = %q, want %q", got, want)
	}

	// The header rules apply in both modes.
	if err := def.Validate(json.RawMessage(`{"action":"feat","title":"guard","section_what_does_this_pr_do":"x"}`)); err == nil {
		t.Error("a verb outside the vocabulary passed validation")
	}
	// A summary is not required when the template sets the body.
	if err := (PRDefinition{}).Validate(ok); err == nil {
		t.Error("without a template the summary must still be required")
	}
}

func TestDropUnbackedTesting_TemplateSections(t *testing.T) {
	src := PRSources{All: "Ran `./scripts/test.sh` on Linux."}
	pr := PRResult{Sections: map[string]string{
		"testing": "Checked on Linux:\n- `./scripts/test.sh` passed\n- `go test ./internal/benchfixtures -run Ledger`\n\nAlso ran `make lint` by hand.",
	}}

	dropped := pr.DropUnbackedTesting(src)
	if want := []string{"- `go test ./internal/benchfixtures -run Ledger`"}; !reflect.DeepEqual(dropped, want) {
		t.Fatalf("dropped = %q, want %q", dropped, want)
	}
	if want := "Checked on Linux:\n- `./scripts/test.sh` passed\n\nAlso ran `make lint` by hand."; pr.Sections["testing"] != want {
		t.Fatalf("section = %q, want %q", pr.Sections["testing"], want)
	}
	claims := pr.UnbackedClaims(src)
	if len(claims) != 1 || !strings.Contains(claims[0], "`make lint`") {
		t.Fatalf("claims = %q, want one for the command in running text", claims)
	}
}
