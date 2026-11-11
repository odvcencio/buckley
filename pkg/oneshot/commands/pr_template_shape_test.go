package commands

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// shapeFixture reads the template and the notes of the shape tests. The
// template has seven headings, 20 checkbox lines in five of them and one fixed
// paragraph; the notes tick 8 items.
func shapeFixture(t *testing.T) (*PRTemplate, string, []AuthorSection) {
	t.Helper()
	text, err := os.ReadFile("testdata/pr_template_shape.md")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := ParsePRTemplate(string(text))
	raw, err := os.ReadFile("testdata/pr_notes_shape.md")
	if err != nil {
		t.Fatal(err)
	}
	note, ok, err := NewPRNote(RawPRNote{Path: "notes.md", Text: string(raw)})
	if err != nil || !ok {
		t.Fatalf("NewPRNote: ok=%v err=%v", ok, err)
	}
	return tmpl, string(text), TemplateAuthorSections([]PRNote{note}, tmpl)
}

func checkboxLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if checkboxRe.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

// sectionOf returns the text of the top-level section with the heading.
func sectionOf(body, heading string) string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = line == "## "+heading
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// A template that gives the body its shape: the body has the template's
// headings and nothing after them, its 20 checkbox lines word for word with
// only the author's 8 ticked, and the carried sections below the headings.
func TestTemplateShape_KeepsHeadingsCheckboxesAndFixedText(t *testing.T) {
	tmpl, text, author := shapeFixture(t)
	pr := PRResult{
		Action: "fix",
		Title:  "split the token cache",
		Sections: map[string]string{
			"why_this_approach": "Readers dominate the load.\n\n- [x] The model ticks a box here",
		},
		Issues: StringList{"31"},
	}
	body := pr.Body(PRBodyOptions{Template: tmpl, Sections: author, StateNotVerified: true, HasNotes: true})

	if got, want := topLevelHeadings(body, 2), templateHeadingLines(tmpl); !reflect.DeepEqual(got, want) || len(want) != 7 {
		t.Errorf("top-level headings =\n%q\nwant the template's\n%q", got, want)
	}

	var want []string
	for _, line := range checkboxLines(text) {
		want = append(want, line)
	}
	if len(want) != 20 {
		t.Fatalf("the fixture has %d checkbox lines, want 20", len(want))
	}
	ticked := map[int]bool{0: true, 2: true, 3: true, 5: true, 6: true, 11: true, 12: true, 14: true}
	for i := range want {
		if ticked[i] {
			want[i] = strings.Replace(want[i], "[ ]", "[x]", 1)
		}
	}
	if got := checkboxLines(body); !reflect.DeepEqual(got, want) {
		t.Errorf("checkbox lines differ from the template with exactly 8 ticked:\n got: %q\nwant: %q", got, want)
	}

	// Fixed text of the template stays word for word.
	fixed := "Read the whole diff once before you ask for review. Note the spots that need a second pair of eyes."
	if !strings.Contains(body, fixed) {
		t.Errorf("the fixed paragraph of the template is missing")
	}
	// Guide comments of a section that received text are left out; a section
	// that received nothing keeps them.
	if strings.Contains(body, "Say what changes for a user") || strings.Contains(body, "Give the reason") || strings.Contains(body, "Name the exact commands") || strings.Contains(sectionOf(body, "Performance"), "Counters or timings") {
		t.Errorf("the guide comment of a filled section is still there:\n%s", body)
	}
	if !strings.Contains(sectionOf(body, "Performance"), "Counters or timings") && strings.Contains(sectionOf(body, "Performance"), "<!--") {
		t.Errorf("unexpected comment in Performance")
	}
	if !strings.Contains(sectionOf(body, "Due diligence"), "Linked the issue") {
		t.Errorf("Due diligence lost its checklist")
	}
	if !strings.Contains(sectionOf(body, "Performance"), "###") {
		t.Errorf("Performance should hold the Counters subsection:\n%s", sectionOf(body, "Performance"))
	}

	// The carried sections are subsections one level below the template's
	// headings, placed by the keyword rule: Testing in Correctness (heading),
	// Counters in Performance (heading), Not verified in Due diligence
	// (checkbox text "risks and limits"), Related Issues at the end of the last
	// section that is not a pure checklist.
	for heading, sub := range map[string][]string{
		"Correctness":           {"### Testing\n\n- `go test ./cache -run TestSplitCacheReaders -count=1` passed"},
		"Performance":           {"### Counters\n\n| step | before | after |"},
		"Due diligence":         {"### Not verified\n\n- Windows was not tried.", "### Related Issues\n\n- Refs #31"},
		"What does this PR do?": {"Splits the token cache so that readers no longer block on writers."},
		"Why this approach?":    {"Readers dominate the load."},
	} {
		got := sectionOf(body, heading)
		for _, s := range sub {
			if !strings.Contains(got, s) {
				t.Errorf("section %q lacks %q:\n%s", heading, s, got)
			}
		}
	}
	for _, other := range []string{"## Testing", "## Counters", "## Not verified", "## Related Issues"} {
		if strings.Contains(body, "\n"+other+"\n") {
			t.Errorf("%q is a top-level section", other)
		}
	}
	// The model's text never ticks, adds or removes a template checkbox.
	if strings.Contains(body, "The model ticks a box here") {
		t.Errorf("a checkbox line of the model reached the body")
	}
	// The author's line that is no item of the template is not written.
	if strings.Contains(body, "Added a changelog entry") {
		t.Errorf("an unmatched checkbox line of the notes was written")
	}
	// The author's other lines in a section follow the checklist.
	correctness := sectionOf(body, "Correctness")
	if i, j := strings.Index(correctness, "The new test is `TestSplitCacheReaders`."), strings.LastIndex(correctness, "- [ ] Output formats"); i < 0 || i < j {
		t.Errorf("the author's line does not follow the checklist:\n%s", correctness)
	}
}

// Nothing in the notes ticks a box, and no model text ticks one: the template's
// checklist is written as it is.
func TestTemplateShape_NoTicksWithoutTheAuthor(t *testing.T) {
	tmpl, text, _ := shapeFixture(t)
	pr := PRResult{Action: "fix", Title: "x", Sections: map[string]string{
		"what_does_this_pr_do": "Does it.\n\n- [x] Edge cases (empty input, very long input) were tried by hand",
	}}
	body := pr.Body(PRBodyOptions{Template: tmpl})
	if got := checkboxLines(body); !reflect.DeepEqual(got, checkboxLines(text)) {
		t.Errorf("checkbox lines changed without the author's ticks:\n%q", got)
	}
}

// templateHeadingLines returns the heading lines of the template, in order.
func templateHeadingLines(t *PRTemplate) []string {
	var out []string
	for _, s := range t.Sections {
		out = append(out, strings.TrimSpace(s.HeadingLine))
	}
	return out
}

// topLevelHeadings returns the headings of the given level, outside code
// fences and HTML comments.
func topLevelHeadings(body string, level int) []string {
	var out []string
	prefix := strings.Repeat("#", level) + " "
	var fence fenceTracker
	comment := false
	for _, line := range strings.Split(body, "\n") {
		if !comment && (fence.line(line) || fence.inFence()) {
			continue
		}
		if !comment && strings.HasPrefix(line, prefix) {
			out = append(out, strings.TrimSpace(line))
		}
		comment = commentOpenAfter(line, comment)
	}
	return out
}

// The section rule: a block that no section takes goes to the first section
// whose heading, guide or checkbox text has a keyword of its kind; else to the
// last section that is not a pure checklist.
func TestTemplateShape_PlacementRule(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\n\n<!-- what -->\n\n## Gate\n\n- [ ] Run the benchmark suite\n\n## Notes\n\nFixed words.\n\n## Boxes\n\n- [ ] one\n")
	pr := PRResult{Action: "fix", Title: "x", Breaking: true, Sections: map[string]string{"summary": "Does it."}}
	body := pr.Body(PRBodyOptions{Template: tmpl, Sections: []AuthorSection{
		{Kind: SectionCounters, Heading: "Counters", Body: "work 10 -> 10"},
		{Kind: SectionTesting, Heading: "Testing", Body: "ran it"},
		{Kind: SectionNotVerified, Heading: "Not verified", Body: "Windows"},
		{Kind: SectionKnownGaps, Heading: "Known gaps", Body: "none known"},
	}})
	if got, want := topLevelHeadings(body, 2), templateHeadingLines(tmpl); !reflect.DeepEqual(got, want) {
		t.Errorf("top-level headings = %q, want the template's %q", got, want)
	}
	for heading, subs := range map[string][]string{
		"Gate":  {"### Counters\n\nwork 10 -> 10"},                                                                                // "benchmark" in a checkbox
		"Notes": {"### Testing\n\nran it", "### Breaking Changes", "### Not verified\n\nWindows", "### Known gaps\n\nnone known"}, // no keyword: the last section that is not a checklist
	} {
		for _, sub := range subs {
			if !strings.Contains(sectionOf(body, heading), sub) {
				t.Errorf("section %q lacks %q:\n%s", heading, sub, body)
			}
		}
	}
	if strings.Contains(sectionOf(body, "Boxes"), "###") {
		t.Errorf("a pure checklist took a block:\n%s", body)
	}
}

// shapeBody renders notes into a template and returns the body and the warning
// texts. The model wrote summary text for the sections it is asked for.
func shapeBody(t *testing.T, template, notes string, pr PRResult) (string, []BodyWarning) {
	t.Helper()
	tmpl := ParsePRTemplate(template)
	var sections []AuthorSection
	if notes != "" {
		note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: notes})
		if err != nil || !ok {
			t.Fatalf("NewPRNote: %v %v", ok, err)
		}
		sections = TemplateAuthorSections([]PRNote{note}, tmpl)
	}
	return pr.BodyWithWarnings(PRBodyOptions{Template: tmpl, Sections: sections, StateNotVerified: true, HasNotes: notes != ""})
}

func hasWarning(ws []BodyWarning, part string, blocking bool) bool {
	for _, w := range ws {
		if strings.Contains(w.Text, part) && w.Blocking == blocking {
			return true
		}
	}
	return false
}

// Review of #478, finding 1: without a template a notes section headed
// Checklist is an ordinary section, so the sections nested in it stay.
func TestChecklistWithoutATemplateIsAnOrdinarySection(t *testing.T) {
	note, _, _ := NewPRNote(RawPRNote{Path: "n.md", Text: "## Checklist\n\n- [x] Ready\n\n### Testing\n\n- Manual check passed.\n\n### Not verified\n\n- Windows was not tested.\n"})
	got := sectionsByKind(AuthorSections([]PRNote{note}))
	if !strings.Contains(got[SectionTesting], "Manual check passed.") || !strings.Contains(got[SectionNotVerified], "Windows") {
		t.Fatalf("sections = %q, want Testing and Not verified", got)
	}
}

// Finding 2: text in a Checklist section that is not a checkbox is written
// after the checklist when the template has a Checklist section, else placed
// as a section of no known kind; in a checklist-only template it is named.
func TestChecklistTextIsNeverLost(t *testing.T) {
	notes := "## Checklist\n- [x] Ready\nWindows was not tested.\n"
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n## Checklist\n- [ ] Ready\n", notes, PRResult{Sections: map[string]string{"summary": "Does it."}})
	if !strings.Contains(body, "- [x] Ready\n\nWindows was not tested.") || hasWarning(ws, "", true) {
		t.Errorf("same-heading section: body =\n%s\nwarnings = %v", body, ws)
	}
	body, ws = shapeBody(t, "## Summary\n<!-- describe -->\n## Gate\n- [ ] Ready\n", notes, PRResult{Sections: map[string]string{"summary": "Does it."}})
	if !strings.Contains(body, "- [x] Ready") || !strings.Contains(body, "Windows was not tested.") {
		t.Errorf("placed section: body =\n%s", body)
	}
	if !strings.Contains(sectionOf(body, "Summary"), "### Checklist\n\nWindows was not tested.") {
		t.Errorf("the text is not placed under the last section that is not a checklist:\n%s", body)
	}
	_, ws = shapeBody(t, "## Gate\n- [ ] Ready\n", notes, PRResult{})
	if !hasWarning(ws, `"Checklist"`, true) {
		t.Errorf("a checklist-only template: warnings = %v, want a blocking one naming Checklist", ws)
	}
}

// Findings 3, 7 and 8: one inventory of items; a checkbox-shaped line in a code
// fence is fixed text, a line in the preamble is an item, and an inline comment
// does not change an item's identity.
func TestTemplateItemInventory(t *testing.T) {
	fenced := "## Summary\n<!-- describe -->\n\n## Example\n```md\n## Sample\n- [ ] Ready\n```\n"
	body, ws := shapeBody(t, fenced, "## Checklist\n- [X] Ready\n", PRResult{Sections: map[string]string{"summary": "Does it."}})
	if !strings.Contains(body, "```md\n## Sample\n- [ ] Ready\n```") || !hasWarning(ws, "- [X] Ready", true) {
		t.Errorf("fenced example: body =\n%s\nwarnings = %v", body, ws)
	}
	pre := "- [ ] License acknowledged\n\n## Summary\n<!-- describe -->\n"
	body, ws = shapeBody(t, pre, "## Checklist\n- [X] License acknowledged\n", PRResult{Sections: map[string]string{"summary": "Does it."}})
	if !strings.HasPrefix(body, "- [x] License acknowledged\n\n## Summary") || len(ws) != 0 {
		t.Errorf("preamble item: body =\n%s\nwarnings = %v", body, ws)
	}
	inline := "## Summary\n<!-- describe -->\n## Checklist\n- [ ] A <!-- guide -->\n"
	body, ws = shapeBody(t, inline, "## Checklist\n- [x] A\n", PRResult{Sections: map[string]string{"summary": "Does it."}})
	if !strings.Contains(body, "- [x] A <!-- guide -->") || len(ws) != 0 {
		t.Errorf("inline comment: body =\n%s\nwarnings = %v", body, ws)
	}
}

// Finding 4: a template with fixed text and no open section keeps exactly its
// own top-level headings; Buckley's sections are placed inside it.
func TestFixedTextTemplateKeepsItsHeadings(t *testing.T) {
	body, ws := shapeBody(t, "## Review\nRead the diff.\n- [ ] Tests ran\n", "", PRResult{Summary: "Change", Changes: StringList{"Update the guard"}})
	if got := topLevelHeadings(body, 2); !reflect.DeepEqual(got, []string{"## Review"}) {
		t.Errorf("headings = %q, body =\n%s", got, body)
	}
	if !strings.Contains(body, "### Summary\n\nChange") || !strings.Contains(body, "### Changes\n\n- Update the guard") || !strings.Contains(body, "Read the diff.") {
		t.Errorf("body =\n%s", body)
	}
	_ = ws
}

// Finding 5 and the level limit: the headings inside a placed section move below
// the template's headings; headings in code fences stay.
func TestPlacedHeadingsStayBelowTheTemplate(t *testing.T) {
	body, _ := shapeBody(t, "## Summary\n<!-- overview -->\n### Details\n<!-- testing evidence -->\n", "## Evidence\n### Measurements\nObserved output.\n```\n# keep\n```\n", PRResult{Sections: map[string]string{"summary": "Does it."}})
	if got := topLevelHeadings(body, 3); !reflect.DeepEqual(got, []string{"### Details"}) {
		t.Errorf("level-3 headings = %q, body =\n%s", got, body)
	}
	if !strings.Contains(body, "#### Evidence") || !strings.Contains(body, "##### Measurements") || !strings.Contains(body, "```\n# keep\n```") {
		t.Errorf("body =\n%s", body)
	}
	deep, _ := shapeBody(t, "###### Details\n<!-- describe -->\n", "## Evidence\n### Measurements\nSeen.\n", PRResult{Sections: map[string]string{"details": "Does it."}})
	if !strings.Contains(deep, "**Evidence**") || !strings.Contains(deep, "**Measurements**") || strings.Contains(deep, "####### ") {
		t.Errorf("level 6 template: body =\n%s", deep)
	}
}

// Finding 6: the author's own checkbox lines in a section with no checkboxes
// and in a placed section are written as written; the model's are dropped with
// one warning that counts them.
func TestAuthorAndModelCheckboxLines(t *testing.T) {
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n## Gate\n- [ ] Existing\n", "## Summary\n- [x] Mine\n\n## Evidence\n- [x] Also mine\n", PRResult{})
	if !strings.Contains(body, "- [x] Mine") || !strings.Contains(body, "- [x] Also mine") || len(ws) != 0 {
		t.Errorf("author lists: body =\n%s\nwarnings = %v", body, ws)
	}
	body, ws = shapeBody(t, "## Summary\n<!-- describe -->\n## Gate\n- [ ] Existing\n", "", PRResult{Sections: map[string]string{"summary": "Does it.\n- [x] Existing\n- [ ] Extra"}})
	if strings.Contains(body, "[x] Existing") || strings.Contains(body, "Extra") || !hasWarning(ws, "2 checkbox", false) {
		t.Errorf("model lists: body =\n%s\nwarnings = %v", body, ws)
	}
}

// Finding 9: the generated Not verified statement of a checklist-only template
// warns, and does not block.
func TestGeneratedNotVerifiedInAChecklistOnlyTemplateWarns(t *testing.T) {
	_, ws := shapeBody(t, "## Checklist\n- [ ] Ready\n", "", PRResult{})
	if !hasWarning(ws, "Not verified", false) {
		t.Errorf("warnings = %v, want a non-blocking one naming Not verified", ws)
	}
}

// The model's text reaches only the sections it was asked for: an open section
// that an author section fills by kind gets nothing from the model.
func TestModelTextOnlyFillsTheSectionsItWasAskedFor(t *testing.T) {
	body, _ := shapeBody(t, "## Testing\n<!-- steps -->\n## Validation\n<!-- more -->\n", "## Testing\nRan it.\n", PRResult{Sections: map[string]string{"validation": "EXTRA"}})
	if strings.Contains(body, "EXTRA") {
		t.Errorf("model text for a section it was not asked for reached the body:\n%s", body)
	}
}
