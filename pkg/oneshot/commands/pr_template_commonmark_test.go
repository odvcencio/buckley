package commands

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// shapeNames lists "level:key" for the top-level headings of text that are at or
// above level deepest, as the reader finds them.
func shapeNames(text string, deepest int) []string {
	var out []string
	for _, h := range readMarkdown(text).topHeadings() {
		if h.Level <= deepest {
			out = append(out, fmt.Sprintf("%d:%s", h.Level, headingKey(h.Text)))
		}
	}
	return out
}

// templateNames lists the same for a parsed template.
func templateNames(t *PRTemplate) []string {
	var out []string
	for _, s := range t.Sections {
		out = append(out, fmt.Sprintf("%d:%s", s.Level, headingKey(s.Heading)))
	}
	return out
}

// Review of #478, round four, finding 1: a model field with CRLF line endings
// is demoted like any other text, and the body has no carriage return.
func TestModelTextWithCRLFIsDemoted(t *testing.T) {
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n", "", PRResult{Sections: map[string]string{"summary": "Intro\r\nMeasurements\r\n---\r\nObserved."}})
	if got := shapeNames(body, 2); !reflect.DeepEqual(got, []string{"2:summary"}) {
		t.Errorf("headings at the template's level = %q, body =\n%q", got, body)
	}
	if strings.Contains(body, "\r") || !strings.Contains(body, "### Intro Measurements\nObserved.") {
		t.Errorf("body = %q", body)
	}
	for _, w := range ws {
		if w.Blocking {
			t.Errorf("blocking warning: %q", w.Text)
		}
	}
	// A lone carriage return is a line ending too, in a result field and in a list.
	body, _ = shapeBody(t, "## Summary\n<!-- describe -->\n", "", PRResult{Sections: map[string]string{"summary": "Intro\rMeasurements\r---\rObserved."}})
	if got := shapeNames(body, 2); !reflect.DeepEqual(got, []string{"2:summary"}) {
		t.Errorf("lone CR: headings = %q, body =\n%q", got, body)
	}
}

// Finding 2: a list that interrupts a paragraph is not part of a setext heading,
// so its pre-ticked item is in the inventory and follows the notes.
func TestListInterruptingAParagraphKeepsItsItemInTheInventory(t *testing.T) {
	template := "## Summary\n<!-- describe -->\nIntro\n- [x] A\n---\n"
	for name, tc := range map[string]struct {
		notes, want string
		cleared     bool
	}{
		"no notes":      {"", "Intro\n- [ ] A\n---", true},
		"unchecked":     {"## Checklist\n- [ ] A\n", "Intro\n- [ ] A\n---", true},
		"checked":       {"## Checklist\n- [x] A\n", "Intro\n- [x] A\n---", false},
		"alien checked": {"## Checklist\n- [x] B\n", "Intro\n- [ ] A\n---", true},
	} {
		t.Run(name, func(t *testing.T) {
			body, ws := shapeBody(t, template, tc.notes, PRResult{})
			if !strings.Contains(body, tc.want) {
				t.Errorf("body =\n%s\nwant it to contain %q", body, tc.want)
			}
			if got := hasWarning(ws, `"A" is ticked in the template, cleared`, false); got != tc.cleared {
				t.Errorf("clearing warning = %v, want %v; warnings %v", got, tc.cleared, ws)
			}
			if hasWarning(ws, "out of the template's shape", true) {
				t.Errorf("the body is in shape but warns: %v", ws)
			}
		})
	}
}

// Finding 3: raw HTML that the parser leaves open swallows the next template
// heading, and the check names the missing heading. A closed block that holds
// a heading-shaped line is not a heading, so it is not a false alarm.
func TestRawHTMLInPlacedTextIsStructureTheParserSees(t *testing.T) {
	_, ws := shapeBody(t, "## Summary\n<!-- describe -->\n## Checks\n- [ ] A\n", "## Summary\n<pre>\nlog\n", PRResult{})
	if !hasWarning(ws, `lacks the template heading "checks"`, true) {
		t.Errorf("an unclosed <pre> that swallows a template heading: warnings = %v", ws)
	}
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n", "## Summary\n<pre>\n## Extra\n</pre>\n", PRResult{})
	if hasWarning(ws, "out of the template's shape", true) || len(shapeNames(body, 2)) != 1 {
		t.Errorf("a closed <pre> block is not a heading: warnings = %v, body =\n%s", ws, body)
	}
	// The template is read the same way: a heading that its own unclosed HTML
	// block swallows is not a template heading, so the body agrees with it.
	tmpl := ParsePRTemplate("## Summary\n<!-- describe -->\n<div>\ntext\n\n## Checks\n- [ ] A\n")
	if got := templateNames(tmpl); !reflect.DeepEqual(got, []string{"2:summary", "2:checks"}) {
		t.Errorf("a blank line ends a <div> block: template headings = %q", got)
	}
	tmpl = ParsePRTemplate("## Summary\n<!-- describe -->\n<pre>\ntext\n\n## Checks\n- [ ] A\n")
	if got := templateNames(tmpl); !reflect.DeepEqual(got, []string{"2:summary"}) {
		t.Errorf("an unclosed <pre> swallows the rest: template headings = %q", got)
	}
}

// Finding 4: a fence marker inside a comment opens no fence, so the template's
// own setext heading after the comment is a heading in the template and in the
// body alike.
func TestFenceMarkerInACommentOpensNoFence(t *testing.T) {
	template := "## Summary\n<!--\n```\n-->\nTitle\n---\nfixed\n"
	tmpl := ParsePRTemplate(template)
	if got := templateNames(tmpl); !reflect.DeepEqual(got, []string{"2:summary", "2:title"}) {
		t.Fatalf("template headings = %q", got)
	}
	body, ws := shapeBody(t, template, "## Evidence\nObserved.\n", PRResult{})
	if hasWarning(ws, "out of the template's shape", true) {
		t.Errorf("warnings = %v, body =\n%s", ws, body)
	}
	if got := shapeNames(body, 2); !reflect.DeepEqual(got, []string{"2:summary", "2:title"}) {
		t.Errorf("headings = %q, body =\n%s", got, body)
	}
}

// The notes name a few templates' worth of structure; the property test below
// writes random notes and model text and checks the shape of the body.
type fragmentWriter struct{ r *rand.Rand }

func (w fragmentWriter) pick(options ...string) string { return options[w.r.Intn(len(options))] }

var words = []string{"alpha", "beta", "gamma", "delta", "cache", "reader", "writer", "token"}

func (w fragmentWriter) phrase() string {
	n := 1 + w.r.Intn(3)
	out := make([]string, n)
	for i := range out {
		out[i] = words[w.r.Intn(len(words))]
	}
	return strings.Join(out, " ")
}

// fragment returns a block of Markdown that is balanced: every fence and
// comment it opens, it closes. (An unclosed one is the job of the shape check,
// and has its own tests.)
func (w fragmentWriter) fragment() string {
	switch w.r.Intn(12) {
	case 0, 1:
		hashes := strings.Repeat("#", 1+w.r.Intn(6))
		closing := w.pick("", "", " ##", "  ")
		return strings.Repeat(" ", w.r.Intn(4)) + hashes + " " + w.phrase() + closing
	case 2, 3:
		under := w.pick("=", "===", "-", "---", "-----", "  ---  ")
		lines := w.phrase()
		if w.r.Intn(3) == 0 {
			lines += "\n" + w.phrase()
		}
		return lines + "\n" + under
	case 4:
		return w.phrase() + ". " + w.phrase()
	case 5:
		return "- " + w.phrase() + "\n- " + w.phrase() + "\n  - " + w.phrase()
	case 6:
		return "1. " + w.phrase() + "\n2. " + w.phrase()
	case 7:
		char := w.pick("`", "~")
		open := strings.Repeat(char, 3+w.r.Intn(3))
		other := map[string]string{"`": "~~~", "~": "```"}[char] // the other character never closes this fence
		inner := w.pick("# heading", "---", "- [ ] box", "text\n===", other, "## "+w.phrase())
		closeFence := open
		if w.r.Intn(2) == 0 {
			closeFence += char
		}
		return open + w.pick("", "go", " md") + "\n" + inner + "\n" + closeFence
	case 8:
		return "<!-- " + w.phrase() + "\n## hidden\n-->"
	case 9:
		return "> " + w.phrase() + "\n> " + w.pick("## quoted", "---", w.phrase())
	case 10:
		return w.pick("***", "___", "- - -")
	default:
		return "`" + w.phrase() + "` and **" + w.phrase() + "**"
	}
}

func (w fragmentWriter) text(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = w.fragment()
		// mdpp v0.5.0 does not read a multi-line setext heading in a document
		// that begins with a thematic break or an HTML block (known difference 1,
		// a defect in mdpp that is reported: see mdpp-differences.md in the
		// lane's notes), so no generated text starts with one.
		if i == 0 && (isBreak(parts[i]) || strings.HasPrefix(parts[i], "<!--")) {
			parts[i] = w.phrase()
		}
	}
	return strings.Join(parts, w.pick("\n\n", "\n\n", "\n"))
}

// For random notes and model text, the body has exactly the template's
// top-level headings, and every checkbox line of the template appears once, in
// order, byte for byte except the check mark. 2,400 cases from a fixed seed.
func TestPropertyBodyKeepsTheTemplateShape(t *testing.T) {
	templates := []string{
		"Thanks for the change.\n- [ ] I read the contributing guide\n\n## Summary\n<!-- describe -->\n### Details\n<!-- more -->\n## Testing\n<!-- how you tested -->\n## Checklist\n- [ ] Tests added\n- [ ] Docs updated <!-- if needed -->\n  - [ ] Nested item\n## Notes\nFixed words stay.\n",
		"What\n====\n\n<!-- say what -->\n\nWhy\n---\n\n<!-- say why -->\n\n## Checks\n\n- [ ] Ran it\n* [ ] Read it\n\n```md\n## Example\n- [ ] not an item\n```\n",
	}
	itemsOf := func(tmpl string) []string {
		d := readMarkdown(tmpl)
		var out []string
		for _, task := range d.Tasks {
			out = append(out, d.Lines[task.Line])
		}
		return out
	}
	const cases = 1200
	for ti, template := range templates {
		tmpl := ParsePRTemplate(template)
		wantHeadings := templateNames(tmpl)
		deepest := tmpl.deepestLevel()
		items := itemsOf(template)
		rnd := rand.New(rand.NewSource(478 + int64(ti)))
		w := fragmentWriter{rnd}
		for n := 0; n < cases; n++ {
			// Notes: some sections the template takes or places, a Checklist section
			// that ticks some of the template's items, and free text.
			var notes []string
			for _, head := range []string{"Testing", "Evidence", "Counters", "Not verified", "Known gaps", "Summary", "Why"} {
				if rnd.Intn(2) == 0 {
					notes = append(notes, "## "+head+"\n\n"+w.text(1+rnd.Intn(3)))
				}
			}
			var ticked []string
			if rnd.Intn(2) == 0 {
				var boxes []string
				for _, item := range items {
					if rnd.Intn(2) == 0 {
						line := strings.Replace(item, "[ ]", w.pick("[x]", "[X]"), 1)
						boxes = append(boxes, strings.TrimSpace(line))
						ticked = append(ticked, item)
					}
				}
				boxes = append(boxes, "- [x] Not an item of the template")
				notes = append(notes, "## Checklist\n\n"+strings.Join(boxes, "\n"))
			}
			text := strings.Join(notes, "\n\n")
			if rnd.Intn(3) == 0 {
				text = strings.ReplaceAll(text, "\n", "\r\n")
			}
			model := map[string]string{}
			for _, s := range tmpl.Sections {
				if s.Open && rnd.Intn(3) > 0 {
					model[s.Key] = w.text(1 + rnd.Intn(3))
					if rnd.Intn(3) == 0 {
						model[s.Key] = strings.ReplaceAll(model[s.Key], "\n", "\r\n")
					}
				}
			}
			var sections []AuthorSection
			if strings.TrimSpace(text) != "" {
				note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: text})
				if err != nil {
					t.Fatalf("case %d/%d: NewPRNote: %v\nnotes:\n%q", ti, n, err, text)
				}
				if ok {
					sections = TemplateAuthorSections([]PRNote{note}, tmpl)
				}
			}
			pr := PRResult{Action: "fix", Title: "x", Summary: w.phrase(), Changes: StringList{w.phrase()}, Sections: model}
			body, ws := pr.BodyWithWarnings(PRBodyOptions{Template: tmpl, Sections: sections, StateNotVerified: rnd.Intn(2) == 0, HasNotes: len(sections) > 0})
			fail := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("case %d/%d: "+format+"\nnotes:\n%q\nmodel: %q\nbody:\n%s", append(append([]any{ti, n}, args...), text, model, body)...)
			}
			if strings.Contains(body, "\r") {
				fail("the body has a carriage return")
			}
			if got := shapeNames(body, deepest); !reflect.DeepEqual(got, wantHeadings) {
				fail("headings = %q, want %q", got, wantHeadings)
			}
			for _, w := range ws {
				if w.Blocking && strings.Contains(w.Text, "out of the template's shape") {
					fail("the shape check warns: %s", w.Text)
				}
			}
			got := checkboxLines(body)
			if len(got) != len(items) {
				fail("%d checkbox lines, want the template's %d: %q", len(got), len(items), got)
			}
			for i := range got {
				if mask(got[i]) != mask(items[i]) {
					fail("checkbox line %d = %q, want %q except its check mark", i, got[i], items[i])
				}
				if strings.ContainsAny(strings.TrimSpace(got[i])[3:4], "xX") && !containsLine(ticked, items[i]) {
					fail("checkbox line %d is ticked but the notes did not tick it: %q", i, got[i])
				}
			}
		}
	}
}

// mask blanks the check mark of a task line.
func mask(line string) string {
	d := readMarkdown(line)
	if len(d.Tasks) != 1 {
		return line
	}
	b := []byte(d.text)
	b[d.Tasks[0].MarkAt] = '_'
	return string(b)
}

func containsLine(lines []string, line string) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

func isBreak(s string) bool { return s == "***" || s == "___" || s == "- - -" }
