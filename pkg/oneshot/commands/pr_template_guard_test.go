package commands

import (
	"strings"
	"testing"
)

// Review of #478, round six, finding 3: the raw-HTML check covers the complete
// body, Buckley's own sections included. Here the model's Summary is written
// before the checklist-only template and its opener would hide both sections.
func TestFinalCheckCoversBuckleysOwnSections(t *testing.T) {
	template := "## Evidence\n- [ ] A\n## Not verified\n- [ ] B\n"
	notes := "## Evidence\nObserved.\n## Not verified\nNo other checks.\n"
	for name, summary := range map[string]string{
		"in a quote":    "> <!-- open",
		"plain opener":  "<!-- open",
		"after a block": "Text\n\n<div>\n<!-- open\n</div>",
	} {
		body, ws := shapeBody(t, template, notes, PRResult{Summary: summary})
		blocked := false
		for _, w := range ws {
			blocked = blocked || w.Blocking
		}
		if strings.Contains(body, "<!-- open") && !blocked {
			t.Errorf("%s: the opener reached the body and nothing blocks:\n%s\nwarnings %v", name, body, ws)
		}
	}
}

// Finding 5: a template that Markdown++ cannot read exactly is not used. A
// 20 KB checklist line makes the parser keep the block as text.
func TestUnreadableTemplateStopsGeneration(t *testing.T) {
	template := "## Summary\n<!-- describe -->\n## Checks\n- [X] " + strings.Repeat("a*b_", 5000) + "\n"
	tmpl := ParsePRTemplate(template)
	why := tmpl.Unreadable()
	if !strings.Contains(why, "MDPP-PARSE") {
		t.Fatalf("Unreadable() = %q, want a parser diagnostic", why)
	}
	_, ws := PRResult{Sections: map[string]string{"summary": "x"}}.BodyWithWarnings(PRBodyOptions{Template: tmpl})
	if !hasWarning(ws, "cannot be read exactly", true) {
		t.Errorf("warnings = %v", ws)
	}
}

// Finding 2: a range that cannot be used is rejected, never trusted.
func TestUnusableRangesAreRejected(t *testing.T) {
	// The task item inside a block quote is in the whole-template inventory, so
	// it is cleared with its warning, not left ticked silently.
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n## Checks\n\n> - [X] A\n", "", PRResult{})
	if !strings.Contains(body, "> - [ ] A") || !hasWarning(ws, `"A" is ticked in the template, cleared`, false) {
		t.Errorf("body =\n%s\nwarnings %v", body, ws)
	}
	// A block quote at the start of a document gets no source ranges from the
	// parser: the reading is refused, with a problem, not trusted.
	if d := readMarkdown("> - [X] A"); len(d.Problems) == 0 || len(d.Tasks) != 0 {
		t.Errorf("problems %v, tasks %v", d.Problems, d.Tasks)
	}
	// Notes with an alert: the parser shifts the ranges after it; nothing of the
	// notes is lost, with a template, without one and with --template=false.
	notes := "> [!NOTE]\nx\n\n## Evidence\nObserved.\n"
	body, ws = shapeBody(t, "## Summary\n<!-- describe -->\n", notes, PRResult{Sections: map[string]string{"summary": "Change"}})
	if !strings.Contains(body, "Observed.") || !hasWarning(ws, "carried as written", false) {
		t.Errorf("template: body =\n%s\nwarnings %v", body, ws)
	}
	note, _, _ := NewPRNote(RawPRNote{Path: "n.md", Text: notes})
	body, _ = PRResult{Action: "fix", Title: "x", Summary: "Change"}.BodyWithWarnings(PRBodyOptions{Sections: AuthorSections([]PRNote{note})})
	if !strings.Contains(body, "Observed.") {
		t.Errorf("no template: body =\n%s", body)
	}
}

// Findings 1 and 4 of round six, and the guard for Markdown++-only constructs:
// a construct that the parser reads differently from GitHub is a blocking
// warning in the template, the model's text and the final body, and a warning in
// the author's notes.
func TestMarkdownPlusPlusOnlyConstructsAreGuarded(t *testing.T) {
	template := "## Summary\n<!-- describe -->\n## Checks\n- [ ] A\n"
	for name, model := range map[string]string{
		"uppercase heading after prose": "Before.\n\n<H2 class=\"x\">Extra</H2>",
		"container directive":           ":::note\n## Extra\n:::",
		"expression":                    "uses {name} here",
	} {
		body, ws := shapeBody(t, template, "", PRResult{Sections: map[string]string{"summary": model}})
		if !hasWarning(ws, "Markdown++ reads this differently from GitHub", true) {
			t.Errorf("%s: warnings %v, body =\n%s", name, ws, body)
		}
	}
	// The author's form: a warning that names the section and the line; the
	// final check blocks as well, because the text reaches the body.
	body, ws := shapeBody(t, template, "## Evidence\nSeen.\n<H2 class=\"x\">Extra</H2>\n", PRResult{Sections: map[string]string{"summary": "Change"}})
	if !hasWarning(ws, `notes section "Evidence" has a Markdown++ component`, false) || !hasWarning(ws, "the body has a Markdown++ component", true) {
		t.Errorf("warnings %v, body =\n%s", ws, body)
	}
	// A template that has the construct itself is not accused of it.
	tmpl := ParsePRTemplate("## Summary\n<!-- describe -->\n\nUse {name} in the title.\n")
	if ws := tmpl.finalBodyWarnings("## Summary\n\nText\n\nUse {name} in the title.\n"); len(ws) != 0 {
		t.Errorf("the template's own construct: %v", ws)
	}
}

// Round six, can follow: a closed comment inside another HTML block of the
// model's text is removed, with a warning.
func TestClosedCommentInsideAnHTMLBlockOfModelTextIsRemoved(t *testing.T) {
	body, ws := shapeBody(t, "## Summary\n<!-- describe -->\n", "", PRResult{Sections: map[string]string{"summary": "<div>\n<!-- hidden -->\n</div>"}})
	if strings.Contains(body, "hidden") || !hasWarning(ws, "a closed HTML comment", false) {
		t.Errorf("body =\n%s\nwarnings %v", body, ws)
	}
}
