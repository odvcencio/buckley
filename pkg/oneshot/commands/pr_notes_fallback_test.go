package commands

import (
	"strings"
	"testing"
)

// Review of #478, round seven, finding 4. Without a template the layout is the
// layout of main: a note that Markdown++ reads exactly gives the same carried
// sections and the same body as before, with no warning. A note that it cannot
// split exactly is carried whole (main carried nothing from this one), and the
// run says so.
func TestNoTemplateLayoutKeepsMainsOutputAndWarnsOnTheWholeNoteFallback(t *testing.T) {
	pr := PRResult{Action: "fix", Title: "x", Summary: "Change"}
	read := func(text string) []PRNote {
		note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: text})
		if err != nil || !ok {
			t.Fatalf("NewPRNote(%q): %v %v", text, ok, err)
		}
		return []PRNote{note}
	}

	// Output of main for this note, written by hand: Buckley's Summary, then the
	// author's Testing section word for word; the other section is not carried.
	notes := read("## Testing\n\nran it\n\n## Other\n\nnot carried\n")
	body, ws := pr.BodyWithWarnings(PRBodyOptions{Sections: AuthorSections(notes)})
	if want := "## Summary\n\nChange\n\n## Testing\n\nran it"; body != want {
		t.Errorf("body =\n%s\nwant (as on main)\n%s", body, want)
	}
	if len(ws) != 0 {
		t.Errorf("warnings = %v, want none", ws)
	}

	// The review's input: on main this note supplies no carried section.
	notes = read("> - [X] A")
	if got := AuthorSections(notes); len(got) != 1 || got[0].problem == "" {
		t.Fatalf("the reading problem is gone; sections = %v", got)
	}
	body, ws = pr.BodyWithWarnings(PRBodyOptions{Sections: AuthorSections(notes)})
	if !strings.Contains(body, "## Notes from n.md\n\n> - [X] A") {
		t.Errorf("body =\n%s", body)
	}
	if !hasWarning(ws, `the notes section "Notes from n.md" could not be split exactly`, false) {
		t.Errorf("warnings = %v, want the whole-note warning", ws)
	}
}
