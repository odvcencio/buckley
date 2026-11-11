package commands

import (
	"strings"
	"testing"
	"time"
)

// Review of #478, round five, finding 2 (a): raw HTML in model text that a
// renderer would act on is neutralized, with one warning that names the field
// and the construct.
func TestModelTextCannotHideOrForgeStructure(t *testing.T) {
	// A closed comment, then an unclosed one in the same field: the second hides
	// the rest of the page once rendered.
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n## Checks\n- [ ] A\n", "", PRResult{Sections: map[string]string{"summary": "<!-- closed --> <!-- unclosed"}})
	if !strings.Contains(body, "&lt;!-- unclosed") || strings.Contains(body, "<!-- closed") || strings.Contains(body, "<!-- unclosed") || !strings.Contains(body, "## Checks\n- [ ] A") {
		t.Errorf("body =\n%s", body)
	}
	if !hasWarning(ws, "section_summary", false) || !hasWarning(ws, "unclosed HTML comment opener", false) {
		t.Errorf("warnings = %v", ws)
	}
	// A Summary-only template, and an author section that follows it: the
	// model's unclosed comment must not hide the placed evidence.
	body, ws = shapeBody(t, "## Summary\n<!-- describe -->\n", "## Evidence\nObserved output.\n", PRResult{Sections: map[string]string{"summary": "<!-- unclosed"}})
	if !strings.Contains(body, "Observed output.") || strings.Contains(body, "<!-- unclosed") || !hasWarning(ws, "unclosed HTML comment opener", false) {
		t.Errorf("body =\n%s\nwarnings = %v", body, ws)
	}
	// A raw heading element, in any case and with attributes, is escaped.
	for _, in := range []string{"<h2>Extra</h2>", "<H3 class=x>Extra</H3>", "text\n\n<h1\nid=a>Extra</h1>"} {
		body, ws = shapeBody(t, "## Summary\n<!-- describe -->\n", "", PRResult{Sections: map[string]string{"summary": in}})
		if strings.Contains(strings.ToLower(body), "<h") || !strings.Contains(body, "&lt;") || !hasWarning(ws, "a raw heading element", false) || !hasWarning(ws, "section_summary", false) {
			t.Errorf("%q: body =\n%s\nwarnings = %v", in, body, ws)
		}
	}
	// The same rules apply to the other model fields; code stays as it is.
	body, ws = shapeBody(t, "## Review\nRead.\n- [ ] Ran\n", "", PRResult{Summary: "<h2>X</h2>", Changes: StringList{"use `<h2>` and <!-- c --> done"}})
	if strings.Contains(body, "<h2>X") || !hasWarning(ws, "summary", false) || !strings.Contains(body, "`<h2>`") || strings.Contains(body, "<!-- c -->") {
		t.Errorf("body =\n%s\nwarnings = %v", body, ws)
	}
}

// (b): the author's text is not rewritten; raw HTML in it that would hide or
// forge structure is a blocking warning that names the section and the line.
// (c): the final check reports it on the whole body as well.
func TestAuthorTextWithStructuralHTMLBlocks(t *testing.T) {
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n", "## Evidence\nSeen.\n<h2>Extra</h2>\n", PRResult{Sections: map[string]string{"summary": "Change"}})
	if !strings.Contains(body, "<h2>Extra</h2>") {
		t.Errorf("the author's text was rewritten:\n%s", body)
	}
	if !hasWarning(ws, `notes section "Evidence" has a raw <h2> heading element at its line 2`, true) || !hasWarning(ws, "the body has a raw <h2> heading element", true) {
		t.Errorf("warnings = %v", ws)
	}
	// Below the template's level it is no forged structure.
	_, ws = shapeBody(t, "## Summary\n<!-- describe -->\n", "## Evidence\nSeen.\n<h3>Detail</h3>\n", PRResult{Sections: map[string]string{"summary": "Change"}})
	if hasWarning(ws, "raw <h3>", true) {
		t.Errorf("a raw h3 under a level-2 template warns: %v", ws)
	}
	// A template that has the element itself is not accused of it.
	tmpl := ParsePRTemplate("<h2>Title</h2>\n\n## Summary\n<!-- describe -->\n")
	if ws := tmpl.finalBodyWarnings("<h2>Title</h2>\n\n## Summary\n\nText"); len(ws) != 0 {
		t.Errorf("the template's own element: %v", ws)
	}
	if ws := tmpl.finalBodyWarnings("<h2>Title</h2>\n\n## Summary\n\n<h2>More</h2>"); !hasWarning(ws, "raw <h2>", true) {
		t.Errorf("one element more than the template: %v", ws)
	}
}

// Finding 1: a bare label directly above a table or a numbered list is a label by
// the rule of the notes splitter, whatever blocks the parser makes of it; with
// a template, without one and with --template=false alike.
func TestBareLabelAboveATableOrAListIsKept(t *testing.T) {
	for name, notes := range map[string]string{
		"table":         "Evidence\n| Check | Result |\n| --- | --- |\n| smoke | passed |\n",
		"numbered list": "Evidence\n2. smoke passed\n3. lint passed\n",
		"bullets":       "Evidence\n- smoke passed\n",
	} {
		note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: notes})
		if err != nil || !ok {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(strings.SplitN(notes, "\n", 2)[1]), "\n")
		for mode, opts := range map[string]PRBodyOptions{
			"no template":    {Sections: AuthorSections([]PRNote{note})},
			"template false": {Sections: AuthorSections([]PRNote{note}), Template: nil},
			"template":       {Template: ParsePRTemplate("## Summary\n<!-- describe -->\n"), Sections: TemplateAuthorSections([]PRNote{note}, ParsePRTemplate("## Summary\n<!-- describe -->\n"))},
			"checklist-only": {Template: ParsePRTemplate("## Gate\n- [ ] Ready\n"), Sections: TemplateAuthorSections([]PRNote{note}, ParsePRTemplate("## Gate\n- [ ] Ready\n"))},
		} {
			body, ws := PRResult{Action: "fix", Title: "x", Summary: "Change", Sections: map[string]string{"summary": "Change"}}.BodyWithWarnings(opts)
			for _, line := range lines {
				if !strings.Contains(body, line) && len(ws) == 0 {
					t.Errorf("%s/%s: the author's line %q is not in the body and no warning names it:\n%s", name, mode, line, body)
				}
				if !strings.Contains(body, line) {
					t.Errorf("%s/%s: the author's line %q is not in the body:\n%s", name, mode, line, body)
				}
			}
		}
	}
}

// No author line is absent from the body unless a warning names it. The
// expectation is written by hand for each sample (the lines of its carried
// sections), not taken from what the splitter returned; a warning excuses a
// missing line only when it names that line.
func TestNoAuthorLineIsLostSilently(t *testing.T) {
	samples := []struct {
		notes string
		lines []string // the lines of the carried sections, as the author wrote them
	}{
		{"## Testing\n\n- `go test ./a` passed\n- second\n\n## Evidence\n\nTitle\n---\nobserved\n\n```\n# not a heading\n```\n",
			[]string{"- `go test ./a` passed", "- second", "Title", "observed", "```", "# not a heading"}},
		{"Testing:\n- ran it\n\nEvidence\n| a | b |\n|---|---|\n| 1 | 2 |\n\nNot verified:\nWindows\n",
			[]string{"- ran it", "| a | b |", "|---|---|", "| 1 | 2 |", "Windows"}},
		{"## Counters\n\n| step | n |\n|---|---|\n| lex | 3 |\n\n### Detail\n\n1. one\n2. two\n\n## Known gaps\n\n> quote\n> more\n",
			[]string{"| step | n |", "| lex | 3 |", "Detail", "1. one", "2. two", "> quote", "> more"}},
		{"**Evidence**\n1. first\n2. second\n\nKnown gaps\n- gap one\n",
			[]string{"1. first", "2. second", "- gap one"}},
		{"> [!NOTE]\nx\n\n## Evidence\nObserved.\n", []string{"Observed."}},
	}
	templates := []string{"", "## Summary\n<!-- describe -->\n## Gate\n- [ ] Ready\n", "## Review\nRead.\n- [ ] Ran\n"}
	for si, sample := range samples {
		note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: sample.notes})
		if err != nil || !ok {
			t.Fatal(err)
		}
		for ti, template := range templates {
			var tmpl *PRTemplate
			var sections []AuthorSection
			if template != "" {
				tmpl = ParsePRTemplate(template)
				sections = TemplateAuthorSections([]PRNote{note}, tmpl)
			} else {
				sections = AuthorSections([]PRNote{note})
			}
			body, ws := PRResult{Action: "fix", Title: "x", Summary: "Change", Sections: map[string]string{"summary": "Change"}}.BodyWithWarnings(PRBodyOptions{Template: tmpl, Sections: sections})
			for _, want := range sample.lines {
				found := false
				for _, b := range strings.Split(body, "\n") {
					// A heading inside a placed section moves down: compare its words.
					if strings.TrimSpace(b) == want || strings.TrimSpace(strings.TrimLeft(b, "# ")) == strings.TrimSpace(strings.TrimLeft(want, "# ")) {
						found = true
					}
				}
				named := false
				for _, w := range ws {
					named = named || strings.Contains(w.Text, want)
				}
				if !found && !named {
					t.Errorf("sample %d, template %d: the author's line %q is absent and no warning names it:\n%s\nwarnings %v", si, ti, want, body, ws)
				}
			}
		}
	}
}

// Finding 3: a notes file of 100,000 short paragraphs must not take quadratic
// time to split into sections.
func TestLargeNotesFileIsSplitInTime(t *testing.T) {
	notes := "## Evidence\n\n" + strings.Repeat("x\n\n", 100000)
	start := time.Now()
	note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: notes})
	if err != nil || !ok {
		t.Fatal(err)
	}
	parse := time.Since(start)
	start = time.Now()
	sections := AuthorSections([]PRNote{note})
	took := time.Since(start)
	t.Logf("%d bytes: sanitize %v, split (mdpp parse and sections) %v, %d sections", len(notes), parse, took, len(sections))
	if len(sections) != 1 {
		t.Fatalf("sections = %d", len(sections))
	}
	if took > 20*time.Second {
		t.Errorf("splitting took %v", took)
	}
}

// The lone carriage return is a line ending at the notes boundary.
func TestNotesBoundaryNormalizesLoneCR(t *testing.T) {
	note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: "## Testing\rran it\r\nsecond\r"})
	if err != nil || !ok || strings.Contains(note.Text(), "\r") {
		t.Fatalf("text = %q ok=%v err=%v", note.Text(), ok, err)
	}
}
