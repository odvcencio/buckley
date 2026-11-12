package commands

import (
	"os"
	"strings"
	"testing"
)

func readGotreesitterTemplate(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/gotreesitter_pr_template.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func blockingWarning(ws []BodyWarning) string {
	for _, w := range ws {
		if w.Blocking {
			return w.Text
		}
	}
	return ""
}

// Review of #478, round seven, finding 1: the final check compares the
// template's checkbox lines with the body's, not only the headings. The template
// is a real one (seven headings, twenty checkbox lines).
func TestFinalCheckComparesTheCheckboxInventory(t *testing.T) {
	raw := readGotreesitterTemplate(t)
	tmpl := ParsePRTemplate(raw)
	if why := tmpl.Unreadable(); why != "" {
		t.Fatalf("the template is unreadable: %s", why)
	}
	if got := len(tmpl.boxRecords()); got != 20 {
		t.Fatalf("the template has %d checkbox records, want 20", got)
	}
	first := "- [ ] Focused package or unit tests ran for the touched behavior"
	second := "- [ ] Affected parity validation ran in Docker, one language or grammar at a time"
	if !strings.Contains(raw, first+"\n"+second+"\n") {
		t.Fatal("the fixture changed")
	}
	// A real body ticks some boxes and fills the two open sections.
	ok := strings.Replace(raw, "<!-- One or two sentences. State the outcome, not a commit diary. -->", "Reads share a lock.", 1)
	ok = strings.Replace(ok, first, strings.Replace(first, "[ ]", "[x]", 1), 1)
	cases := []struct {
		name, body, want string // want: part of the blocking warning, or "" for none
	}{
		{"a correct body", ok, ""},
		{"the first checkbox reworded", strings.Replace(ok, "Focused package or unit tests ran", "Changed checkbox wording", 1), `Changed checkbox wording`},
		{"a checkbox dropped", strings.Replace(ok, second+"\n", "", 1), "lacks the template checkbox line"},
		{"a checkbox duplicated", strings.Replace(ok, second+"\n", second+"\n"+second+"\n", 1), "where the template has"},
		{"two checkboxes swapped", strings.Replace(ok, strings.Replace(first, "[ ]", "[x]", 1)+"\n"+second, second+"\n"+strings.Replace(first, "[ ]", "[x]", 1), 1), "lacks the template checkbox line"},
		{"a checkbox moved under another heading", strings.Replace(strings.Replace(ok, second+"\n", "", 1), "## Performance\n\n", "## Performance\n\n"+second+"\n", 1), "under"},
	}
	for _, tc := range cases {
		ws := tmpl.finalBodyWarnings(tc.body, nil)
		got := blockingWarning(ws)
		switch {
		case tc.want == "" && (len(ws) != 0):
			t.Errorf("%s: warnings %v, want none", tc.name, ws)
		case tc.want != "" && !strings.Contains(got, tc.want):
			t.Errorf("%s: blocking warning %q, want it to contain %q (all: %v)", tc.name, got, tc.want, ws)
		}
	}
}

// The checkbox lines that the author's own notes carry may stand in the body;
// a line that nobody owns may not.
func TestAuthorCheckboxLinesStayAllowed(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\n<!-- describe -->\n## Checks\n- [ ] Ran it\n")
	body := "## Summary\n\nText\n\n### Testing\n\n- [x] ran the suite\n\n## Checks\n\n- [ ] Ran it"
	note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: "## Testing\n- [x] ran the suite\n"})
	if err != nil || !ok {
		t.Fatal(err)
	}
	sections := TemplateAuthorSections([]PRNote{note}, tmpl)
	if ws := tmpl.finalBodyWarnings(body, sections); len(ws) != 0 {
		t.Errorf("the author's list is refused: %v", ws)
	}
	if got := blockingWarning(tmpl.finalBodyWarnings(body, nil)); !strings.Contains(got, "ran the suite") {
		t.Errorf("a line that nobody owns is accepted: %q", got)
	}
}

// The review's comment-exposure case. The template hides "- [ ] A" behind a
// comment on the same line, so its last line is an HTML block and the template
// has no task item. Stripping the guide comment from the section that receives
// text exposes the line as a task item. The final check blocks it: the body
// has a checkbox that the template does not have. (The alternative, keeping
// the comment, would also keep the guidance text in the body.)
func TestCommentExposedCheckboxIsBlocked(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\nFixed.\n<!-- guide -->- [ ] A\n")
	if n := len(tmpl.boxRecords()); n != 0 {
		t.Fatalf("the template has %d checkbox records, want 0", n)
	}
	body, ws := PRResult{Summary: "Change"}.BodyWithWarnings(PRBodyOptions{Template: tmpl})
	if !strings.Contains(body, "- [ ] A") {
		t.Fatalf("the case no longer exposes the line; body:\n%s", body)
	}
	if got := blockingWarning(ws); !strings.Contains(got, `the checkbox line "- [ ] A"`) || !strings.Contains(got, "does not have") {
		t.Errorf("warnings %v, want a blocking one about the exposed checkbox", ws)
	}
}

// The real template, through the whole path: a body made from it has no
// warning; the same path with a reworded template line in the notes is refused
// by the existing item rule.
func TestGotreesitterTemplateBodyHasNoWarning(t *testing.T) {
	tmpl := ParsePRTemplate(readGotreesitterTemplate(t))
	pr := PRResult{Sections: map[string]string{"what_does_this_pr_do": "Reads share a lock.", "why_this_approach": "A read-write lock keeps the API."}}
	body, ws := pr.BodyWithWarnings(PRBodyOptions{Template: tmpl, StateNotVerified: true})
	for _, w := range ws {
		if w.Blocking {
			t.Errorf("blocking warning %q", w.Text)
		}
	}
	if got := len(readMarkdown(body).Tasks); got != 20 {
		t.Errorf("the body has %d checkbox lines, want 20", got)
	}
}

// Review of #478, round eight, finding 2. The comparison is exact: after the one
// character inside the brackets is masked (space, x or X), the two lines must be
// byte for byte equal, with the list marker, the indentation and the trailing
// spaces. Two trailing spaces are a hard break, so they are content. The one
// exception is the edge of the whole text, which the body trims.
func TestCheckboxComparisonIsExact(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\n<!-- describe -->\n\n## Checks\n- [ ] A  \n  continued\n- [ ] B\n- [X] C\n")
	body := func(checks string) string { return "## Summary\n\nText\n\n## Checks\n" + checks }
	const same = "- [ ] A  \n  continued\n- [ ] B\n- [X] C"
	for name, tc := range map[string]struct {
		checks string
		ok     bool
	}{
		"identical":                       {same, true},
		"x for a space, x for X":          {"- [x] A  \n  continued\n- [X] B\n- [x] C", true},
		"trailing spaces dropped":         {"- [ ] A\n  continued\n- [ ] B\n- [X] C", false},
		"trailing space added":            {"- [ ] A  \n  continued\n- [ ] B \n- [X] C", false},
		"one trailing space":              {"- [ ] A \n  continued\n- [ ] B\n- [X] C", false},
		"a different list marker":         {"- [ ] A  \n  continued\n* [ ] B\n- [X] C", false},
		"a different indentation":         {"- [ ] A  \n  continued\n  - [ ] B\n- [X] C", false},
		"a word added after the text":     {"- [ ] A  \n  continued\n- [ ] B now\n- [X] C", false},
		"a changed character in the text": {"- [ ] A  \n  continued\n- [ ] D\n- [X] C", false},
		"a tab for the space after":       {"- [ ] A  \n  continued\n-\t[ ] B\n- [X] C", false},
	} {
		ws := tmpl.finalBodyWarnings(body(tc.checks), nil)
		if got := blockingWarning(ws); tc.ok && got != "" {
			t.Errorf("%s: blocked: %q", name, got)
		} else if !tc.ok && !strings.Contains(got, "checkbox line") {
			t.Errorf("%s: not blocked by a checkbox warning (warnings %v)", name, ws)
		}
	}
	// A template that ends with an item with trailing spaces: the body is trimmed
	// at its end, which is the only place where trailing spaces may go.
	edge := ParsePRTemplate("## Checks\n- [ ] A\n- [ ] Z  \n")
	if got := blockingWarning(edge.finalBodyWarnings("## Checks\n- [ ] A\n- [x] Z", nil)); got != "" {
		t.Errorf("edge of the text: %q", got)
	}
	if got := blockingWarning(edge.finalBodyWarnings("## Checks\n- [ ] A \n- [x] Z", nil)); !strings.Contains(got, "checkbox line") {
		t.Errorf("trailing space inside the text is accepted: %q", got)
	}
}

// Review of #478, round nine, finding 1. The template's last line is a checkbox
// with two trailing spaces (a hard break). The generated Summary follows it, so
// the line is no longer at the body's edge and keeps its spaces. Trimming is
// decided by the line's place in the final body, not by its place in the
// template.
func TestCheckboxAtTheTemplateEdgeIsComparedByItsPlaceInTheBody(t *testing.T) {
	const template = "## Summary\nFixed.\n- [ ] A  \n"
	tmpl := ParsePRTemplate(template)

	// Generation: the box keeps its spaces, and nothing blocks.
	body, ws := PRResult{Summary: "Change"}.BodyWithWarnings(PRBodyOptions{Template: tmpl})
	if want := "## Summary\nFixed.\n- [ ] A  \n\n### Summary\n\nChange"; body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if got := blockingWarning(ws); got != "" {
		t.Errorf("a correct body is refused: %q", got)
	}

	// Final comparison, the correct body: accepted. The same body with the two
	// spaces removed: refused, because the line is no longer at the body's edge.
	if got := blockingWarning(tmpl.finalBodyWarnings(body, nil)); got != "" {
		t.Errorf("final check refuses the correct body: %q", got)
	}
	changed := strings.Replace(body, "- [ ] A  \n", "- [ ] A\n", 1)
	if got := blockingWarning(tmpl.finalBodyWarnings(changed, nil)); !strings.Contains(got, "checkbox line") {
		t.Errorf("final check accepts a body whose box lost its trailing spaces: %q", got)
	}

	// At the body's own edge the trimming is allowed, and only there.
	if got := blockingWarning(tmpl.finalBodyWarnings("## Summary\nFixed.\n- [ ] A", nil)); got != "" {
		t.Errorf("edge of the body: %q", got)
	}
}

// Review of #478, rounds ten to thirteen. A template whose first non-blank line
// is indented is refused when it is read: trimming the start of a body (a saved
// file read back by --body-file) would remove that indentation and could change
// how the first block is read. The refusal comes before any model call. A
// template whose first non-blank line is at column 0 is fine, also after blank
// lines.
func TestTemplateWithAnIndentedFirstLineIsRefused(t *testing.T) {
	for name, template := range map[string]string{
		"indented checkbox":           "  - [ ] A  \n",
		"leading indented code block": "\n    ## Example\n\n## Summary\n<!-- Describe the change. -->\n",
		"indented checkbox first":     "  - [ ] P  \n\n## Checks\n- [ ] B\n",
		"indented paragraph":          "  Fixed paragraph.\n\n## Summary\n<!-- describe -->\n",
		"tab":                         "\t- [ ] A\n",
	} {
		tmpl := ParsePRTemplate(template)
		why := tmpl.Unreadable()
		if !strings.Contains(why, "the template's first line is indented") || !strings.Contains(why, "column 0") {
			t.Errorf("%s: Unreadable() = %q", name, why)
		}
		_, ws := PRResult{Sections: map[string]string{"summary": "Change"}}.BodyWithWarnings(PRBodyOptions{Template: tmpl})
		if !hasWarning(ws, "the pull request template cannot be used", true) {
			t.Errorf("%s: warnings = %v", name, ws)
		}
	}
	for name, template := range map[string]string{
		"blank lines, then a paragraph at column 0": "\n\nFixed paragraph.\n\n## Summary\n<!-- describe -->\n",
		"spaces-only line, then a heading":          " \t \n## Summary\n<!-- describe -->\n",
		"indentation later is fine":                 "## Summary\nFixed.\n  - [ ] A  \n",
	} {
		if why := ParsePRTemplate(template).Unreadable(); why != "" {
			t.Errorf("%s: refused: %s", name, why)
		}
	}
}

// An indented checkbox that is not the first line stays as the template has it,
// also when it is the last line and generated text follows it.
func TestIndentedCheckboxAtTheTemplateEdgeKeepsItsWhiteSpaceInTheBody(t *testing.T) {
	tmpl := ParsePRTemplate("## Summary\nFixed.\n  - [ ] A  \n")
	body, ws := PRResult{Summary: "Change"}.BodyWithWarnings(PRBodyOptions{Template: tmpl})
	if got := blockingWarning(ws); got != "" {
		t.Errorf("Buckley's own output is refused: %q\nbody %q", got, body)
	}
	if !strings.Contains(body, "  - [ ] A  \n") {
		t.Errorf("body = %q", body)
	}
	stripped := strings.Replace(body, "  - [ ]", "- [ ]", 1)
	if got := blockingWarning(tmpl.finalBodyWarnings(stripped, nil)); !strings.Contains(got, "checkbox line") {
		t.Errorf("a body that lost the indentation is accepted: %q", stripped)
	}
}

// The final body is read again and its headings are compared with the template's
// whatever happened to the text on the way. Here a code block that looks like a
// heading is put at the start of a body by hand.
func TestFinalBodyHeadingsAreComparedAfterATransformation(t *testing.T) {
	tmpl := ParsePRTemplate("\n\nFixed paragraph.\n\n## Summary\n<!-- Describe the change. -->\n")
	body, ws := PRResult{Sections: map[string]string{"summary": "Change"}}.BodyWithWarnings(PRBodyOptions{Template: tmpl})
	if got := blockingWarning(ws); got != "" || !strings.HasPrefix(body, "\n\nFixed paragraph.") && !strings.HasPrefix(body, "Fixed paragraph.") {
		t.Fatalf("a template that starts with blank lines is refused: %q\n%q", got, body)
	}
	bad := "## Example\n\n" + body
	if got := blockingWarning(tmpl.finalBodyWarnings(bad, nil)); !strings.Contains(got, "example") {
		t.Errorf("an extra heading is accepted: %q", got)
	}
}
