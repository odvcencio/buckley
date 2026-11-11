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
// fences and HTML comments, with a reference reading that is written for these
// tests and shares nothing with the production recognizer: it cuts the text into
// blocks (blank lines, fences, ATX headings and comment lines end a block) and
// reads a block as a setext heading when its last line is an underline and it
// has text above it that is no list item, quote or indented code.
func topLevelHeadings(body string, level int) []string {
	var out []string
	var block []string
	fenced := ""
	flush := func() {
		defer func() { block = nil }()
		if len(block) < 2 {
			return
		}
		under := strings.TrimRight(block[len(block)-1], " \t")
		trimmed := strings.TrimLeft(under, " ")
		if len(under)-len(trimmed) > 3 || trimmed == "" {
			return
		}
		want := 0
		switch {
		case strings.Trim(trimmed, "=") == "":
			want = 1
		case strings.Trim(trimmed, "-") == "":
			want = 2
		}
		first := block[0]
		if want == level && !strings.HasPrefix(first, "    ") && !strings.HasPrefix(strings.TrimSpace(first), "> ") && !strings.HasPrefix(strings.TrimSpace(first), "- ") && !strings.HasPrefix(strings.TrimSpace(first), "* ") {
			out = append(out, strings.TrimSpace(strings.Join(block[:len(block)-1], " ")))
		}
	}
	inComment := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case fenced != "":
			if strings.HasPrefix(t, fenced) && strings.Trim(t, fenced[:1]) == "" {
				fenced = ""
			}
		case inComment:
			inComment = !strings.Contains(line, "-->")
		case strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~"):
			flush()
			fenced = strings.Repeat(t[:1], len(t)-len(strings.TrimLeft(t, t[:1])))
		case strings.HasPrefix(t, "<!--"):
			flush()
			inComment = !strings.Contains(line, "-->")
		case t == "":
			flush()
		case strings.HasPrefix(line, strings.Repeat("#", level)+" "):
			flush()
			out = append(out, strings.TrimSpace(line))
		case strings.HasPrefix(t, "#"):
			flush()
		default:
			block = append(block, line)
			if len(block) > 1 && strings.Trim(t, "=") == "" || len(block) > 1 && strings.Trim(t, "-") == "" {
				flush() // an underline ends its paragraph
			}
		}
	}
	flush()
	return out
}

// The section rule: a block that no section takes goes to the first section
// whose heading, guide or checkbox text has a keyword of its kind; else to the
// last section that is not a pure checklist.
func TestTemplateShape_PlacementRule(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\n\n<!-- what -->\n\n## Gate\n\n- [ ] Run the benchmark suite\n\n## Notes\n\nFixed words.\n\n## Boxes\n\n- [ ] one\n")
	pr := PRResult{Action: "fix", Title: "x", Breaking: true, Sections: map[string]string{"summary": "Does it."}}
	body := pr.Body(PRBodyOptions{Template: tmpl, Sections: []AuthorSection{
		{kind: SectionCounters, heading: "Counters", body: "work 10 -> 10"},
		{kind: SectionTesting, heading: "Testing", body: "ran it"},
		{kind: SectionNotVerified, heading: "Not verified", body: "Windows"},
		{kind: SectionKnownGaps, heading: "Known gaps", body: "none known"},
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

// Review of #478, round two, finding 1: a setext heading in a placed section is
// converted and demoted; the body has no heading at the template's level that
// the template lacks.
func TestPlacedSetextHeadingsAreDemoted(t *testing.T) {
	body, _ := shapeBody(t, "## Summary\n<!-- describe -->\n", "## Evidence\nMeasurements\n------------\nObserved output.\n\nTitle\n=====\nMore.\n\n```\nKeep\n----\n```\n", PRResult{Sections: map[string]string{"summary": "Change"}})
	if got := topLevelHeadings(body, 2); !reflect.DeepEqual(got, []string{"## Summary"}) {
		t.Errorf("level-2 headings = %q, body =\n%s", got, body)
	}
	if got := topLevelHeadings(body, 1); len(got) != 0 {
		t.Errorf("level-1 headings = %q, body =\n%s", got, body)
	}
	if !strings.Contains(body, "### Evidence\n\n##### Measurements\n") || !strings.Contains(body, "\n#### Title\n") || strings.Contains(body, "------------") || strings.Contains(body, "=====") || !strings.Contains(body, "```\nKeep\n----\n```") {
		t.Errorf("setext headings not converted, or the fence changed:\n%s", body)
	}
}

// Setext headings are read in the other places that read headings of a
// template. (A notes section is started by ATX headings only: a setext heading
// inside a carried section stays in it.)
func TestSetextHeadingsInATemplate(t *testing.T) {
	tmpl := ParsePRTemplate("What\n====\n\n<!-- say what -->\n\nChecks\n------\n\n- [ ] Ran it\n")
	if len(tmpl.Sections) != 2 || tmpl.Sections[0].Heading != "What" || tmpl.Sections[0].Level != 1 || !tmpl.Sections[0].Open || tmpl.Sections[1].Level != 2 || tmpl.Sections[1].HeadingLine != "Checks\n------" {
		t.Fatalf("setext template parsed as %+v", tmpl.Sections)
	}
	body, _ := shapeBody(t, "What\n====\n\n<!-- say what -->\n\nChecks\n------\n\n- [ ] Ran it\n", "", PRResult{Sections: map[string]string{"what": "Does it."}})
	if !strings.Contains(body, "What\n====\n\nDoes it.") || !strings.Contains(body, "Checks\n------\n\n- [ ] Ran it") {
		t.Errorf("body =\n%s", body)
	}
}

// Finding 2: a shorter or different delimiter inside a longer fence does not
// close it, so a checkbox example in it stays fixed text.
func TestTemplateFenceClosesOnlyWithTheSameDelimiter(t *testing.T) {
	template := "## Summary\n<!-- describe -->\n## Example\n````md\n```\n## Sample\n- [ ] Ready\n````\n"
	tmpl := ParsePRTemplate(template)
	if len(tmpl.Sections) != 2 {
		t.Fatalf("sections = %+v, want Summary and Example only", tmpl.Sections)
	}
	body, ws := shapeBody(t, template, "## Checklist\n- [X] Ready\n", PRResult{Sections: map[string]string{"summary": "Does it."}})
	if !strings.Contains(body, "```\n## Sample\n- [ ] Ready\n````") || !hasWarning(ws, "- [X] Ready", true) {
		t.Errorf("body =\n%s\nwarnings = %v", body, ws)
	}
}

// Finding 3: a checklist section that also receives prose keeps its checkbox
// lines byte for byte except the check mark; its other guide comments go.
func TestCheckboxLineKeepsItsCommentWhenProseArrives(t *testing.T) {
	body, _ := shapeBody(t, "## Summary\n<!-- describe -->\n## Checklist\n<!-- tick what you did -->\n- [ ] A <!-- guide -->\n- [ ] B\n", "## Checklist\n- [x] A\nWindows was not tested.\n", PRResult{Sections: map[string]string{"summary": "Does it."}})
	if !strings.Contains(body, "- [x] A <!-- guide -->\n- [ ] B\n\nWindows was not tested.") || strings.Contains(body, "tick what you did") {
		t.Errorf("body =\n%s", body)
	}
}

// The setext cases follow the setext section of the CommonMark specification.
// Each expected reading is written out here, apart from the production code.
func TestSetextRecognizerFollowsCommonMark(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"one-line level 2", "Title\n---\nText", "### Title\nText"},
		{"one-line level 1", "Title\n===\nText", "### Title\nText"},
		{"multi-line paragraph", "Intro\nMeasurements\n---\nObserved", "### Intro Measurements\nObserved"},
		{"multi-line paragraph over =", "Intro\nMeasurements\n=\nObserved", "### Intro Measurements\nObserved"},
		{"one-character underline", "Title\n-\nText", "### Title\nText"},
		{"underline indented three spaces", "Title\n   ---\nText", "### Title\nText"},
		{"underline indented four spaces is text", "Title\n    ---\nText", "Title\n    ---\nText"},
		{"title indented four spaces is code", "    Title\n---\nText", "    Title\n---\nText"},
		{"after a closing fence", "```\ncode\n```\nTitle\n---\nText", "```\ncode\n```\n### Title\nText"},
		{"title starting with inline code", "`Measurements`\n---\nText", "### `Measurements`\nText"},
		{"after an ATX heading", "## Top\nTitle\n---\nText", "### Top\n### Title\nText"},
		{"after a comment line", "<!-- note -->\nTitle\n---\nText", "<!-- note -->\n### Title\nText"},
		{"list item is no heading", "- item\n---\nText", "- item\n---\nText"},
		{"list item continuation is no heading", "- item\n  more\n---\nText", "- item\n  more\n---\nText"},
		{"block quote is no heading", "> quote\n---\nText", "> quote\n---\nText"},
		{"dashes after a blank line are a break", "Text\n\n---\nAfter", "Text\n\n---\nAfter"},
		{"fenced setext stays", "```\nKeep\n----\n```", "```\nKeep\n----\n```"},
		{"two levels keep their order", "A\n=\n\nB\n-", "### A\n\n#### B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := demoteHeadings(tc.in, 3); got != tc.want {
				t.Errorf("demoteHeadings(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	// The reference reader used by the tests agrees on the headings it counts.
	if got := topLevelHeadings("Intro\nMeasurements\n---\nText", 2); !reflect.DeepEqual(got, []string{"Intro Measurements"}) {
		t.Errorf("reference reading = %q", got)
	}
}

// Template parsing reads setext headings with the same recognizer: the comment
// line between them ends the first paragraph.
func TestTemplateSetextAfterACommentLine(t *testing.T) {
	tmpl := ParsePRTemplate("What\n=\n<!-- guide -->\nChecks\n-\n- [ ] Ran it\n")
	if len(tmpl.Sections) != 2 || tmpl.Sections[0].Heading != "What" || tmpl.Sections[1].Heading != "Checks" || tmpl.Sections[1].Level != 2 {
		t.Fatalf("sections = %+v", tmpl.Sections)
	}
}

// The three-line paragraph of the review: one demoted heading.
func TestPlacedMultiLineSetextParagraph(t *testing.T) {
	body, _ := shapeBody(t, "## Summary\n<!-- describe -->\n", "## Evidence\nIntro\nMeasurements\n---\nObserved output.\n", PRResult{Sections: map[string]string{"summary": "Change"}})
	if got := topLevelHeadings(body, 2); !reflect.DeepEqual(got, []string{"## Summary"}) {
		t.Errorf("level-2 headings = %q\n%s", got, body)
	}
	if !strings.Contains(body, "### Evidence\n\n#### Intro Measurements\nObserved output.") {
		t.Errorf("body =\n%s", body)
	}
}

// The last check of the shape names a heading that is extra or missing.
func TestShapeWarningNamesTheOutOfShapeHeading(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\n<!-- a -->\n## Checks\n- [ ] Ran it\n")
	ok := "## Summary\n\nText\n\n## Checks\n\n- [ ] Ran it\n\n### Evidence\n\nx"
	if w := tmpl.shapeWarning(ok); w != "" {
		t.Errorf("a body in shape warns: %q", w)
	}
	for body, want := range map[string]string{
		"## Summary\n\nText\n\n## Extra\n\n## Checks\n":   `"extra"`,
		"## Summary\n\nText\n":                            `lacks the template heading "checks"`,
		"## Summary\n\nText\n\n## Checks\n\nMore\n----\n": `"more"`, // a setext heading at level 2
	} {
		if w := tmpl.shapeWarning(body); !strings.Contains(w, want) {
			t.Errorf("shapeWarning(%q) = %q, want it to name %s", body, w, want)
		}
	}
}

// A template item that is ticked in the template is written ticked only when the
// notes tick it; otherwise it is cleared, with one warning that names it.
func TestPreTickedTemplateItemsFollowTheAuthor(t *testing.T) {
	template := "- [X] Pre <!-- why -->\n\n## Summary\n<!-- describe -->\n## Checklist\n- [x] A\n- [ ] B\n"
	model := PRResult{Sections: map[string]string{"summary": "Change"}}
	for name, tc := range map[string]struct {
		notes      string
		wantA, pre string
		warned     []string
	}{
		"no notes":            {"", "- [ ] A", "- [ ] Pre <!-- why -->", []string{"A", "Pre"}},
		"unticked author":     {"## Checklist\n- [ ] A\n- [ ] Pre\n", "- [ ] A", "- [ ] Pre <!-- why -->", []string{"A", "Pre"}},
		"ticked author":       {"## Checklist\n- [x] A\n- [x] Pre\n", "- [x] A", "- [X] Pre <!-- why -->", nil},
		"uppercase tick kept": {"## Checklist\n- [X] A\n- [X] Pre\n", "- [x] A", "- [X] Pre <!-- why -->", nil},
	} {
		t.Run(name, func(t *testing.T) {
			body, ws := shapeBody(t, template, tc.notes, model)
			if !strings.Contains(body, tc.wantA+"\n- [ ] B") || !strings.HasPrefix(body, tc.pre+"\n") {
				t.Errorf("body =\n%s", body)
			}
			var got []string
			for _, w := range ws {
				if strings.Contains(w.Text, "cleared because the notes do not tick it") {
					got = append(got, strings.Fields(strings.SplitN(w.Text, `"`, 3)[1])[0])
				}
			}
			if !reflect.DeepEqual(got, func() []string {
				if tc.warned == nil {
					return nil
				}
				return []string{tc.warned[1], tc.warned[0]}
			}()) {
				t.Errorf("cleared warnings = %q, want %q", got, tc.warned)
			}
		})
	}
}

// A checkbox comment that spans lines stays with its line when prose arrives;
// an independent guide comment goes.
func TestCheckboxCommentSpanningLinesStaysAttached(t *testing.T) {
	body, _ := shapeBody(t, "## Summary\n<!-- describe -->\n## Checklist\n- [ ] A <!-- first\nsecond -->\n- [ ] B\n<!-- guide\nmore -->\n", "## Checklist\n- [x] A\nWindows was not tested.\n", PRResult{Sections: map[string]string{"summary": "Change"}})
	if !strings.Contains(body, "- [x] A <!-- first\nsecond -->\n- [ ] B\n\nWindows was not tested.") || strings.Contains(body, "guide") {
		t.Errorf("body =\n%s", body)
	}
}
