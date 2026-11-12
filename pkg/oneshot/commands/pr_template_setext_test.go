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
		// By CommonMark's definition (section 4.3, setext headings) the last line
		// is an underline of the paragraph lines above it: a heading the reader
		// does not list.
		if last := strings.Split(strings.TrimRight(body[:strings.Index(body, "\n\n## Summary")], "\n"), "\n"); !isSetextUnderline(last[len(last)-1]) {
			t.Fatalf("%s: the case has no setext underline", name)
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
// Two rules of the CommonMark specification, with no renderer involved:
//   - section 4.3 (setext headings): an underline is a line of only "=" or only
//     "-" characters after up to three spaces, with optional trailing spaces; and
//   - section 2.4 (backslash escapes): a backslash before an ASCII punctuation
//     character ("=" and "-" are) escapes it, so the character is literal text.
//
// So the rewritten line is not an underline (it holds a backslash), the
// backslash escapes the character after it, and what is left of the paragraph
// has no heading. The rendered result was not checked with an independent
// renderer.
func TestModelTextUnderlineBecomesText(t *testing.T) {
	for name, in := range map[string]string{
		"leading thematic break": "***\n\nT\nU\n=",
		"leading html block":     "<div>x</div>\n\nT\nU\n=",
		"dash underline":         "***\n\nT\nU\n---",
	} {
		before := readMarkdown(in)
		if len(before.Underlines) == 0 {
			t.Fatalf("%s: the reader no longer misses this heading", name)
		}
		out, constructs := neutralizeHTML(in)
		if !strings.Contains(strings.Join(constructs, ";"), "setext underline") {
			t.Errorf("%s: constructs = %v", name, constructs)
		}
		inLines, outLines := strings.Split(in, "\n"), strings.Split(out, "\n")
		if len(inLines) != len(outLines) {
			t.Fatalf("%s: line count changed: %q", name, out)
		}
		for i, line := range inLines {
			got := outLines[i]
			if !isSetextUnderline(line) {
				if got != line {
					t.Errorf("%s: line %d changed from %q to %q", name, i, line, got)
				}
				continue
			}
			if isSetextUnderline(got) {
				t.Errorf("%s: line %d is still an underline: %q", name, i, got)
			}
			if got != `\`+line {
				t.Errorf("%s: line %d = %q, want a backslash before %q", name, i, got, line)
			}
			if c := line[0]; !(c == '=' || c == '-') || !isASCIIPunct(c) {
				t.Errorf("%s: %q does not start with an escapable character", name, line)
			}
		}
		if d := readMarkdown(out); len(d.Underlines) != 0 || len(d.topHeadings()) != 0 {
			t.Errorf("%s: the reader still finds a hazard in %q", name, out)
		}
	}
}

// isSetextUnderline is CommonMark's definition (section 4.3): up to three
// spaces, then only "=" or only "-", then optional trailing spaces.
func isSetextUnderline(line string) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return false
	}
	t = strings.TrimRight(t, " ")
	return t != "" && (strings.Trim(t, "=") == "" || strings.Trim(t, "-") == "")
}

// isASCIIPunct is the ASCII punctuation set of CommonMark section 2.4.
func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// The model's text is warned about, once per field.
func TestModelTextUnderlineIsWarned(t *testing.T) {
	_, ws := PRResult{Summary: "***\n\nT\nU\n="}.withoutStructuralHTML()
	for _, w := range ws {
		if strings.Contains(w.Text, "setext underline") {
			return
		}
	}
	t.Errorf("warnings = %v, want one about the setext underline", ws)
}

// Review of #478, round eight, finding 1. A blank line is a line of only spaces
// and tabs (CommonMark section 2.1); U+00A0 and the other Unicode spaces are
// content. So "T", a line holding one U+00A0, and "=" are one paragraph over an
// underline: a level-1 heading. The parser drops the U+00A0 line from its block
// ranges, and strings.TrimSpace calls it blank, so the guard missed it.
func TestNonBreakingSpaceLineIsParagraphText(t *testing.T) {
	for name, gap := range map[string]string{"U+00A0": "\u00a0", "U+2003": "\u2003"} {
		model := "***\n\nT\n" + gap + "\n="
		if d := readMarkdown(model); !reflect.DeepEqual(d.Underlines, []int{4}) {
			t.Errorf("%s: Underlines = %v for %q", name, d.Underlines, model)
		}
		// The review's pair: the template, and the model's summary.
		template := "<!-- c -->\n\n## Summary\n<!-- describe -->\n"
		body, ws := shapeBody(t, template, "", PRResult{Sections: map[string]string{"summary": model}})
		if !hasWarning(ws, "setext underline", false) {
			t.Errorf("%s: no warning about the rewritten line; warnings %v", name, ws)
		}
		lines := strings.Split(body, "\n")
		if got := lines[len(lines)-1]; got != "" && isSetextUnderline(got) {
			t.Errorf("%s: the body still ends with an underline line %q", name, got)
		}
		if !strings.Contains(body, "\n"+gap+"\n\\=") {
			t.Errorf("%s: the underline is not escaped in\n%q", name, body)
		}
		// The assembled body itself, as the final check sees it.
		assembled := "<!-- c -->\n\n## Summary\n\n" + model
		if got := blockingWarning(ParsePRTemplate(template).finalBodyWarnings(assembled, nil)); !strings.Contains(got, "setext underline") {
			t.Errorf("%s: final check = %q", name, got)
		}
	}
	// A line of spaces and tabs is blank: the underline is a thematic break or
	// text after a blank line, as before.
	if d := readMarkdown("T\n \t \n---"); len(d.Underlines) != 0 {
		t.Errorf("a space-and-tab line is blank: Underlines = %v", d.Underlines)
	}
	if !blankText(" \t\n") || blankText("\u00a0") || blankText("\u2003") {
		t.Error("blankText")
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
