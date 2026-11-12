package commands

import (
	"os"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// goldmarkHeadings lists the headings that are children of the document, as
// "h<level>", the way CommonMark (goldmark) reads them. It is an independent
// reader: nothing here calls readMarkdown.
func goldmarkHeadings(src string) []string {
	var out []string
	b := []byte(src)
	root := goldmark.New().Parser().Parse(text.NewReader(b))
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok {
			out = append(out, "h"+string(rune('0'+h.Level)))
		}
	}
	return out
}

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
