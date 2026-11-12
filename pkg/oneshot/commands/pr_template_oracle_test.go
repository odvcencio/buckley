package commands

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// An oracle that does not use the reader: the notes and the model text are
// built from a small model in which each generated line is known to be a
// heading, a code line or text, each with a unique token. The body is then
// checked with plain string rules and the known template lines, never with
// readMarkdown, headingKey or any parser.
type oracleFragment struct {
	text  string
	kind  string // atx, setext, fenced, plain
	token string
}

func oracleFragments(r *rand.Rand, n *int, minLevel int) []oracleFragment {
	var out []oracleFragment
	for i := 0; i < 1+r.Intn(5); i++ {
		*n++
		tok := fmt.Sprintf("tok%d", *n)
		switch r.Intn(5) {
		case 0, 1:
			level := minLevel + r.Intn(7-minLevel)
			out = append(out, oracleFragment{strings.Repeat("#", level) + " " + tok + " words", "atx", tok})
		case 2:
			under := []string{"===", "---", "=", "-----"}[r.Intn(4)]
			out = append(out, oracleFragment{tok + " words\n" + under, "setext", tok})
		case 3:
			char := []string{"`", "~"}[r.Intn(2)]
			fence := strings.Repeat(char, 3+r.Intn(3))
			out = append(out, oracleFragment{fence + "\n# fenced" + tok + "\n" + fence, "fenced", "fenced" + tok})
		default:
			out = append(out, oracleFragment{tok + " plain words with `code`", "plain", tok})
		}
	}
	return out
}

func joinFragments(fs []oracleFragment) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.text
	}
	return strings.Join(parts, "\n\n")
}

var (
	hashLineRe  = regexp.MustCompile(`^ {0,3}(#{1,6})( |$)`)
	underlineRe = regexp.MustCompile(`^ {0,3}(=+|-+) *$`)
	boxLineRe   = regexp.MustCompile(`^\s*- \[[ xX]\] `)
)

func TestPropertyWithAnIndependentOracle(t *testing.T) {
	const template = "## Summary\n<!-- describe -->\n### Details\n<!-- more -->\n## Checks\n- [ ] Ran it\n- [ ] Read it\n"
	headingLines := []string{"## Summary", "### Details", "## Checks"}
	items := []string{"- [ ] Ran it", "- [ ] Read it"}
	tmpl := ParsePRTemplate(template)
	r := rand.New(rand.NewSource(5))
	counter := 0
	for n := 0; n < 1200; n++ {
		author := oracleFragments(r, &counter, 3) // under "## Evidence", a heading of level 2 ends the section
		model := oracleFragments(r, &counter, 1)
		tickRan := r.Intn(2) == 0
		notes := "## Evidence\n\n" + joinFragments(author)
		if tickRan {
			notes += "\n\n## Checklist\n\n- [x] Ran it\n- [ ] Read it"
		}
		sep := "\n"
		if r.Intn(3) == 0 {
			sep = []string{"\r\n", "\r"}[r.Intn(2)]
		}
		notes = strings.ReplaceAll(notes, "\n", sep)
		note, ok, err := NewPRNote(RawPRNote{Path: "n.md", Text: notes})
		if err != nil || !ok {
			t.Fatalf("case %d: %v", n, err)
		}
		modelText := strings.ReplaceAll(joinFragments(model), "\n", sep)
		pr := PRResult{Action: "fix", Title: "x", Sections: map[string]string{"summary": modelText}}
		body, ws := pr.BodyWithWarnings(PRBodyOptions{Template: tmpl, Sections: TemplateAuthorSections([]PRNote{note}, tmpl)})
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("case %d: "+format+"\nnotes: %q\nmodel: %q\nbody:\n%s\nwarnings: %v", append(append([]any{n}, args...), notes, modelText, body, ws)...)
		}
		lines := strings.Split(body, "\n")
		if strings.Contains(body, "\r") {
			fail("carriage return in the body")
		}
		// Heading lines of level 1 to 3 are the template's, in order, and nothing else.
		var got []string
		for _, l := range lines {
			if m := hashLineRe.FindStringSubmatch(l); m != nil && len(m[1]) <= 3 && !strings.Contains(l, "fenced") {
				got = append(got, strings.TrimRight(l, " "))
			}
			if underlineRe.MatchString(l) {
				fail("an underline line is left: %q", l)
			}
		}
		if strings.Join(got, "|") != strings.Join(headingLines, "|") {
			fail("heading lines at levels 1 to 3 = %q, want %q", got, headingLines)
		}
		// The checklist: the template's lines, ticked only when the author ticked.
		var boxes []string
		for _, l := range lines {
			if boxLineRe.MatchString(l) {
				boxes = append(boxes, l)
			}
		}
		want := []string{items[0], items[1]}
		if tickRan {
			want[0] = "- [x] Ran it"
		}
		if strings.Join(boxes, "|") != strings.Join(want, "|") {
			fail("checkbox lines = %q, want %q", boxes, want)
		}
		// Every generated heading is a heading below the template's, every fenced
		// line is untouched, every plain line is there once.
		for _, f := range append(append([]oracleFragment{}, author...), model...) {
			var hits []string
			for _, l := range lines {
				if strings.Contains(l, f.token) {
					hits = append(hits, l)
				}
			}
			if len(hits) != 1 {
				fail("token %s appears on %d lines: %q", f.token, len(hits), hits)
			}
			switch f.kind {
			case "atx", "setext":
				if m := hashLineRe.FindStringSubmatch(hits[0]); !(m != nil && len(m[1]) >= 4) && !strings.HasPrefix(hits[0], "**") {
					fail("heading %s is not below the template: %q", f.token, hits[0])
				}
			case "fenced":
				if hits[0] != "# "+f.token {
					fail("fenced line changed: %q", hits[0])
				}
			}
		}
	}
}
