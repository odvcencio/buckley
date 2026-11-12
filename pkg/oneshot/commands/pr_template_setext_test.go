package commands

import (
	"reflect"
	"strings"
	"testing"
)

// Review of #478, round seven, finding 2: Markdown++ 0.5.0 misreads a
// multi-line setext heading after a leading HTML block or thematic break, so a
// body can render a heading that the reader does not list. The guard does not
// depend on the reader's reading of that heading: a line with the form of a
// setext underline, directly below a paragraph line, that the reader does not
// show as part of a heading is a hazard.
func TestSetextUnderlineTheReaderMissesIsBlocked(t *testing.T) {
	tmpl := ParsePRTemplate("<!-- c -->\n\n## Summary\n<!-- describe -->\n")
	for name, body := range map[string]string{
		"after a leading comment":        "<!-- c -->\n\nT\nU\n=\n\n## Summary\n\nText",
		"after a leading thematic break": "***\n\nT\nU\n=\n\n## Summary\n\nText",
		"a dash underline":               "<!-- c -->\n\nT\nU\n---\n\n## Summary\n\nText",
	} {
		// CommonMark (an independent reader) shows an extra heading.
		if got := goldmarkHeadings(body); len(got) < 2 {
			t.Fatalf("%s: goldmark headings = %v, the case has no hidden heading", name, got)
		}
		ws := tmpl.finalBodyWarnings(body, nil)
		if blockingWarning(ws) == "" && tmpl.shapeWarning(body) == "" {
			t.Errorf("%s: nothing blocks the body\n%s", name, body)
		}
	}
}

// The guard names the underline when the reader shows a paragraph there.
func TestSetextUnderlineAfterALeadingCommentIsReported(t *testing.T) {
	d := readMarkdown("<!-- c -->\n\nT\nU\n=\n\n## Summary\n\nText")
	if !reflect.DeepEqual(d.Underlines, []int{4}) {
		t.Errorf("Underlines = %v, headings %v", d.Underlines, d.Headings)
	}
}

// No false alarms: a real thematic break after a blank line, a heading the
// reader shows, a fenced block, a list item, a quote, a table, and the real
// template.
func TestSetextGuardHasNoFalseAlarm(t *testing.T) {
	for name, src := range map[string]string{
		"thematic break after a blank line": "A\n\n---\n\nB",
		"thematic break in an HTML block":   "<div>x</div>\n---\n\nB",
		"setext heading":                    "T\nU\n=\n\nText",
		"one-line setext heading":           "T\n---\n\nText",
		"fenced block":                      "```\nT\nU\n=\n```\n",
		"fenced block with dashes":          "Text\n\n```\nT\n---\n```\n",
		"list item":                         "- T\n  U\n  =\n",
		"list then thematic break":          "- a\n---\n",
		"quote then thematic break":         "> a\n---\n",
		"table":                             "| a | b |\n|---|---|\n| 1 | 2 |\n",
		"table without outer pipes":         "a | b\n--- | ---\n1 | 2\n",
		"indented code":                     "    T\n    ---\n",
		"four spaces before the line":       "Text\n\n    ===\n",
	} {
		if d := readMarkdown(src); len(d.Underlines) != 0 {
			t.Errorf("%s: Underlines = %v for %q", name, d.Underlines, src)
		}
		// In model text such an input is left exactly as it is.
		if out, constructs := neutralizeHTML(src); out != src || len(constructs) != 0 {
			t.Errorf("%s: neutralizeHTML changed %q to %q (%v)", name, src, out, constructs)
		}
	}
	raw := readGotreesitterTemplate(t)
	tmpl := ParsePRTemplate(raw)
	if why := tmpl.Unreadable(); why != "" {
		t.Errorf("the real template is refused: %s", why)
	}
	if ws := tmpl.finalBodyWarnings(raw, nil); len(ws) != 0 {
		t.Errorf("the real template as a body warns: %v", ws)
	}
}

// In model text the line is made text: a backslash before its first character.
// CommonMark (goldmark) then reads a paragraph and no heading, and the reader
// agrees with it.
func TestModelTextUnderlineBecomesText(t *testing.T) {
	for name, in := range map[string]string{
		"leading thematic break": "***\n\nT\nU\n=",
		"leading html block":     "<div>x</div>\n\nT\nU\n=",
		"dash underline":         "***\n\nT\nU\n---",
	} {
		before := readMarkdown(in)
		out, constructs := neutralizeHTML(in)
		if len(before.Underlines) == 0 {
			// The reader reads this one correctly; it is a heading, and demoteHeadings
			// handles it. The inventories agree.
			if got, want := len(readMarkdown(in).topHeadings()), len(goldmarkHeadings(in)); got != want {
				t.Errorf("%s: reader %d headings, goldmark %d, and nothing is flagged", name, got, want)
			}
			continue
		}
		if len(constructs) == 0 || !strings.Contains(strings.Join(constructs, ";"), "setext underline") {
			t.Errorf("%s: constructs = %v", name, constructs)
		}
		if got := goldmarkHeadings(out); len(got) != 0 {
			t.Errorf("%s: goldmark still reads headings %v in %q", name, got, out)
		}
		if d := readMarkdown(out); len(d.Underlines) != 0 || len(d.topHeadings()) != 0 {
			t.Errorf("%s: the reader still finds a hazard in %q", name, out)
		}
		if !strings.Contains(out, `\=`) && !strings.Contains(out, `\-`) {
			t.Errorf("%s: no backslash in %q", name, out)
		}
	}
}

// The model's text is warned about, once per field.
func TestModelTextUnderlineIsWarned(t *testing.T) {
	_, ws := PRResult{Summary: "***\n\nT\nU\n="}.withoutStructuralHTML()
	got := false
	for _, w := range ws {
		got = got || strings.Contains(w.Text, "setext underline")
	}
	if readMarkdown("***\n\nT\nU\n=").Underlines != nil && !got {
		t.Errorf("warnings = %v", ws)
	}
}

// Author text and template text are reported, not rewritten.
func TestAuthorAndTemplateUnderlinesAreBlocking(t *testing.T) {
	bad := "***\n\nT\nU\n="
	if readMarkdown(bad).Underlines == nil {
		t.Fatalf("the reader no longer misses %q", bad)
	}
	tmpl := ParsePRTemplate("## Summary\n<!-- describe -->\n")
	note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: "## Evidence\n\n" + bad})
	if err != nil || !ok {
		t.Fatal(err)
	}
	found := false
	for _, s := range TemplateAuthorSections([]PRNote{note}, tmpl) {
		found = true
		if ws := tmpl.authorHTMLWarnings(s); !hasWarning(ws, "setext underline", true) {
			t.Errorf("author warnings = %v", ws)
		}
	}
	if !found {
		t.Error("no author section")
	}
	if why := ParsePRTemplate(bad + "\n\n## Summary\n").Unreadable(); !strings.Contains(why, "setext underline") {
		t.Errorf("template Unreadable() = %q", why)
	}
}

// Finding 3: a setext heading whose text begins with "#" is a valid heading.
func TestSetextHeadingThatBeginsWithAHash(t *testing.T) {
	for name, src := range map[string]string{
		"one line":  "## Summary\n<!-- describe -->\n\n#tag\n---\n",
		"two lines": "## Summary\n<!-- describe -->\n\n#tag\nmore\n---\n",
		"equals":    "## Summary\n<!-- describe -->\n\n#tag\n===\n",
	} {
		d := readMarkdown(src)
		if len(d.Problems) != 0 {
			t.Errorf("%s: problems %v", name, d.Problems)
		}
		if got := goldmarkHeadings(src); len(got) != 2 {
			t.Fatalf("%s: goldmark headings %v", name, got)
		}
		tops := d.topHeadings()
		if len(tops) != 2 {
			t.Errorf("%s: headings %v", name, d.Headings)
		}
		if tmpl := ParsePRTemplate(src); tmpl.Unreadable() != "" {
			t.Errorf("%s: template refused: %s", name, tmpl.Unreadable())
		}
	}
	// A heading that is really an ATX opener with a long range stays refused.
	if !atxOpener("# x") || !atxOpener("#") || atxOpener("#tag") || atxOpener("####### x") || atxOpener("    # x") {
		t.Error("atxOpener")
	}
}
